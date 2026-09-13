// Package registry exposes an ephemeral, read-only pip/npm metadata source. It
// never downloads or verifies third-party bytes: catalog packages are answered
// with metadata synthesized from the catalog (artifacts point straight at the
// URLs described there), and every other name is forwarded to the sources the
// user already configured (npm: HTTP 302; pip: aggregated index).
package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

type Config struct {
	Index    catalog.Index
	Platform string
	Client   *http.Client // required; carries the user's proxy configuration
	// NpmRegistry is the user's effective npm registry base URL. A name that is
	// not a catalog package is redirected there. Empty means npm cannot be
	// forwarded.
	NpmRegistry string
	// NpmScoped maps an npm scope (for example "@vscode") to the registry the
	// user configured for that scope, so forwarded scoped names keep their
	// scoped override.
	NpmScoped map[string]string
	// PipIndexes are the user's effective pip indexes, primary first. A name
	// that is not a catalog package is resolved against all of them and the
	// results are merged. Empty means pip cannot be forwarded.
	PipIndexes []string
}

type Server struct {
	Config
	URL         string
	server      *http.Server
	done        chan struct{}
	cancel      context.CancelFunc
	connections sync.WaitGroup
	// mu guards the synthesized metadata caches below.
	mu  sync.Mutex
	npm map[string]map[string]any
	pip map[string][]pipFile
}

// pipFile is one link published by a project detail page. It is either
// synthesized from a catalog pip artifact or parsed from an upstream index.
type pipFile struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Hashes         map[string]string `json:"hashes"`
	RequiresPython string            `json:"requires-python"`
	Yanked         any               `json:"yanked"`
	Size           int64             `json:"size"`
}

func Start(ctx context.Context, cfg Config) (*Server, error) {
	if err := cfg.Index.Validate(); err != nil {
		return nil, err
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("registry requires a proxy-aware HTTP client")
	}
	if cfg.NpmRegistry != "" {
		if err := catalog.ValidateURL(strings.TrimSuffix(cfg.NpmRegistry, "/")); err != nil {
			return nil, fmt.Errorf("npm registry: %w", err)
		}
	}
	for scope, base := range cfg.NpmScoped {
		if !strings.HasPrefix(scope, "@") || !catalog.ValidEcosystemName("npm", scope+"/x") {
			return nil, fmt.Errorf("invalid npm scope %q", scope)
		}
		if err := catalog.ValidateURL(strings.TrimSuffix(base, "/")); err != nil {
			return nil, fmt.Errorf("npm scope %s registry: %w", scope, err)
		}
	}
	for _, base := range cfg.PipIndexes {
		if err := catalog.ValidateURL(strings.TrimSuffix(base, "/")); err != nil {
			return nil, fmt.Errorf("pip index: %w", err)
		}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{Config: cfg, URL: "http://" + l.Addr().String() + "/" + hex.EncodeToString(nonce[:]), done: make(chan struct{}), cancel: cancel, npm: map[string]map[string]any{}, pip: map[string][]pipFile{}}
	s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }, ConnState: func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			s.connections.Add(1)
		}
		if state == http.StateClosed || state == http.StateHijacked {
			s.connections.Done()
		}
	}}
	go func() { defer close(s.done); _ = s.server.Serve(l) }()
	return s, nil
}

// Close cancels active requests and joins the listener before returning.
func (s *Server) Close() {
	s.cancel()
	// Server.Close joins the accept loop; connection states join all request
	// handlers, including in-flight upstream metadata requests.
	_ = s.server.Close()
	<-s.done
	s.connections.Wait()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	u, _ := url.Parse(s.URL)
	if r.Host != u.Host || !strings.HasPrefix(r.URL.Path, u.Path+"/") {
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only registry", 405)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// The escaped path keeps percent-encoded separators (npm scoped names
	// arrive as "@scope%2fname") intact, so a redirect can forward them as-is.
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), u.EscapedPath()+"/")
	switch {
	case strings.HasPrefix(escaped, "npm/"):
		name := strings.TrimSuffix(strings.TrimPrefix(escaped, "npm/"), "/")
		if strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		decoded, err := url.PathUnescape(name)
		if err != nil || !catalog.ValidEcosystemName("npm", decoded) {
			http.NotFound(w, r)
			return
		}
		meta, err := s.npmMetadata(r.Context(), decoded)
		if err != nil {
			s.fail(w, err)
			return
		}
		if meta == nil {
			s.redirectNpm(w, r, decoded, name)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(meta); err != nil {
			s.fail(w, err)
		}
	case strings.HasPrefix(escaped, "simple/"):
		name := strings.TrimSuffix(strings.TrimPrefix(escaped, "simple/"), "/")
		if strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		decoded, err := url.PathUnescape(name)
		if err != nil || !catalog.ValidEcosystemName("pip", decoded) {
			http.NotFound(w, r)
			return
		}
		files, err := s.pipMetadata(r.Context(), catalog.NormalizePythonName(decoded))
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writePipIndex(w, files)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadGateway)
}

// redirectNpm forwards a name that is not a catalog package to the registry the
// user configured for its scope, or to the effective registry.
func (s *Server) redirectNpm(w http.ResponseWriter, r *http.Request, decoded, escaped string) {
	base := s.NpmRegistry
	if scope, _, ok := strings.Cut(decoded, "/"); ok && strings.HasPrefix(decoded, "@") {
		if override := s.NpmScoped[scope]; override != "" {
			base = override
		}
	}
	if base == "" {
		s.fail(w, fmt.Errorf("npm registry for %s is not configured; configure one in npmrc or install it through the catalog", decoded))
		return
	}
	http.Redirect(w, r, strings.TrimSuffix(base, "/")+"/"+escaped, http.StatusFound)
}

func (s *Server) getJSON(ctx context.Context, address, accept string, target any) error {
	if err := catalog.ValidateURL(address); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("upstream metadata: HTTP %d for %s", resp.StatusCode, address)
	}
	data, err := readAtMost(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (s *Server) find(manager, name string) (catalog.Package, bool) {
	for _, p := range s.Index.Packages {
		if p.Manager() == manager && p.EcosystemName() == name {
			return p, true
		}
	}
	return catalog.Package{}, false
}

// npmMetadata returns the synthesized packument for a catalog package. It
// returns (nil, nil) when the name is not a catalog package and must be
// forwarded instead.
func (s *Server) npmMetadata(ctx context.Context, name string) (map[string]any, error) {
	if p, ok := s.find("npm", name); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		if meta, ok := s.npm[name]; ok {
			return meta, nil
		}
		meta, err := synthesizePackument(p, name, s.Platform)
		if err != nil {
			return nil, err
		}
		s.npm[name] = meta
		return meta, nil
	}
	return nil, nil
}

func synthesizePackument(p catalog.Package, name, platform string) (map[string]any, error) {
	if p.Latest[platform] == "" {
		return nil, fmt.Errorf("%s is unavailable for %s", name, platform)
	}
	versions := map[string]any{}
	for _, v := range p.Versions {
		a := v.NpmArtifacts
		if a == nil {
			continue
		}
		var manifest map[string]any
		if err := json.Unmarshal(a.PackageJSON, &manifest); err != nil {
			return nil, fmt.Errorf("%s@%s: %w", name, v.Version, err)
		}
		// Old upstream versions sometimes use Git or direct-URL dependencies.
		// Keep them out of the candidate set so the client cannot bypass the
		// configured registries.
		if err := ValidateNpmDependencies(manifest); err != nil {
			continue
		}
		if manifest["name"] != name || manifest["version"] != v.Version {
			return nil, fmt.Errorf("npm manifest name/version mismatch for %s@%s", name, v.Version)
		}
		manifest["dist"] = map[string]any{"tarball": a.URL, "integrity": sha256Integrity(a.SHA256), "size": float64(a.Size)}
		versions[v.Version] = manifest
	}
	return map[string]any{"name": name, "versions": versions, "dist-tags": map[string]any{"latest": p.Latest[platform]}}, nil
}

func sha256Integrity(value string) string {
	b, err := hex.DecodeString(value)
	if err != nil {
		return ""
	}
	return "sha256-" + base64.StdEncoding.EncodeToString(b)
}

// pipMetadata returns the project detail list. Catalog packages resolve to
// their own adapted artifacts only; other names are aggregated from the
// configured indexes.
func (s *Server) pipMetadata(ctx context.Context, name string) ([]pipFile, error) {
	if p, ok := s.find("pip", name); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		if files, ok := s.pip[name]; ok {
			return files, nil
		}
		files, err := catalogPipFiles(p, name, s.Platform)
		if err != nil {
			return nil, err
		}
		s.pip[name] = files
		return files, nil
	}
	if len(s.PipIndexes) == 0 {
		return nil, fmt.Errorf("pip index for %s is not configured; configure one in pip.conf or install it through the catalog", name)
	}
	return s.aggregatePip(ctx, name)
}

func catalogPipFiles(p catalog.Package, name, platform string) ([]pipFile, error) {
	if p.Latest[platform] == "" {
		return nil, fmt.Errorf("%s is unavailable for %s", name, platform)
	}
	var files []pipFile
	for _, v := range p.Versions {
		for _, a := range v.PipArtifacts {
			files = append(files, pipFile{Filename: a.Filename, URL: a.URL, Hashes: map[string]string{"sha256": a.SHA256}, RequiresPython: a.RequiresPython, Size: a.Size})
		}
	}
	return files, nil
}

func ValidateNpmDependencies(manifest map[string]any) error {
	for _, key := range []string{"dependencies", "optionalDependencies", "peerDependencies", "overrides", "resolutions"} {
		if err := validateSpecs(manifest[key]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}
func validateSpecs(value any) error {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		for _, item := range v {
			if err := validateSpecs(item); err != nil {
				return err
			}
		}
	case string:
		if strings.HasPrefix(v, "npm:") {
			v = strings.TrimPrefix(v, "npm:")
			if i := strings.LastIndex(v, "@"); i > 0 {
				v = v[i+1:]
			}
		}
		if strings.ContainsAny(v, "/:\\\r\n") || strings.HasPrefix(v, ".") {
			return fmt.Errorf("direct URL, Git and local dependencies are not supported through oo: %q", v)
		}
	default:
		return fmt.Errorf("invalid dependency specification")
	}
	return nil
}

// writePipIndex renders the PEP 503 detail page. The SHA-256 fragment and
// data-requires-python are preserved so pip can still verify what it downloads.
func writePipIndex(w http.ResponseWriter, files []pipFile) {
	fmt.Fprintln(w, "<!doctype html><html><body>")
	for _, f := range files {
		href := f.URL
		if sum := strings.ToLower(f.Hashes["sha256"]); sum != "" {
			href += "#sha256=" + sum
		}
		requires := ""
		if f.RequiresPython != "" {
			requires = ` data-requires-python="` + html.EscapeString(f.RequiresPython) + `"`
		}
		yanked := ""
		if f.Yanked != nil && f.Yanked != false {
			yanked = ` data-yanked="` + html.EscapeString(fmt.Sprint(f.Yanked)) + `"`
		}
		fmt.Fprintf(w, "<a href=\"%s\"%s%s>%s</a>\n", html.EscapeString(href), requires, yanked, html.EscapeString(f.Filename))
	}
	fmt.Fprintln(w, "</body></html>")
}
