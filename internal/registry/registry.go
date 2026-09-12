// Package registry exposes an ephemeral, read-only pip/npm registry. Catalog
// packages shadow the entire upstream name; other dependencies use upstream
// metadata, with every artifact downloaded and checked by oo before serving.
package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
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
	Cache    string
	Client   *http.Client
	// Upstream base URLs are injectable for protocol tests.
	NpmURL string
	PipURL string
}

type Server struct {
	Config
	URL         string
	server      *http.Server
	done        chan struct{}
	cancel      context.CancelFunc
	connections sync.WaitGroup
	mu          sync.Mutex
	blobs       map[string]*blob
	npm         map[string]map[string]any
	pip         map[string][]pipFile
}

type pipFile struct {
	Filename        string               `json:"filename"`
	URL             string               `json:"url"`
	Hashes          map[string]string    `json:"hashes"`
	RequiresPython  string               `json:"requires-python"`
	Yanked          any                  `json:"yanked"`
	Size            int64                `json:"size"`
	CatalogArtifact *catalog.PipArtifact `json:"-"`
}

func Start(ctx context.Context, cfg Config) (*Server, error) {
	if err := cfg.Index.Validate(); err != nil {
		return nil, err
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("registry requires a proxy-aware HTTP client")
	}
	if cfg.NpmURL == "" {
		cfg.NpmURL = "https://registry.npmjs.org"
	}
	if cfg.PipURL == "" {
		cfg.PipURL = "https://pypi.org/simple"
	}
	for _, u := range []string{cfg.NpmURL, cfg.PipURL} {
		if err := catalog.ValidateURL(u); err != nil {
			return nil, err
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
	s := &Server{Config: cfg, URL: "http://" + l.Addr().String() + "/" + hex.EncodeToString(nonce[:]), done: make(chan struct{}), cancel: cancel, blobs: map[string]*blob{}, npm: map[string]map[string]any{}, pip: map[string][]pipFile{}}
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

// Close cancels active transfers and joins the listener before returning.
func (s *Server) Close() {
	s.cancel()
	// Close interrupts slow clients as well as upstream transfers. Server.Close
	// joins the accept loop; connection states join all request handlers.
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
	path := strings.TrimPrefix(r.URL.Path, u.Path+"/")
	var err error
	switch {
	case strings.HasPrefix(path, "files/"):
		err = s.serveBlob(w, r, strings.TrimPrefix(path, "files/"))
	case strings.HasPrefix(path, "npm/"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "npm/"), "/")
		if !catalog.ValidEcosystemName("npm", name) {
			http.NotFound(w, r)
			return
		}
		var meta map[string]any
		meta, err = s.npmMetadata(r.Context(), name)
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			err = json.NewEncoder(w).Encode(meta)
		}
	case strings.HasPrefix(path, "simple/"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "simple/"), "/")
		if !catalog.ValidEcosystemName("pip", name) {
			http.NotFound(w, r)
			return
		}
		var files []pipFile
		files, err = s.pipMetadata(r.Context(), catalog.NormalizePythonName(name))
		if err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintln(w, "<!doctype html><html><body>")
			for _, f := range files {
				yanked := ""
				if f.Yanked != nil && f.Yanked != false {
					yanked = ` data-yanked="` + html.EscapeString(fmt.Sprint(f.Yanked)) + `"`
				}
				fmt.Fprintf(w, "<a href=\"%s#sha256=%s\" data-requires-python=\"%s\"%s>%s</a>\n", html.EscapeString(f.URL), f.Hashes["sha256"], html.EscapeString(f.RequiresPython), yanked, html.EscapeString(f.Filename))
			}
			fmt.Fprintln(w, "</body></html>")
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 64<<20 {
		return fmt.Errorf("upstream metadata too large")
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

// addBlob is called with mu held. URLs are never accepted from client requests.
func (s *Server) addBlob(b *blob) (string, error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(b.URL + "\n" + b.SHA256 + "\n" + b.Integrity + "\n" + b.SHA1))
	id := hex.EncodeToString(key[:]) + "/" + b.Filename
	if _, ok := s.blobs[id]; !ok {
		s.blobs[id] = b
	}
	return s.URL + "/files/" + hex.EncodeToString(key[:]) + "/" + url.PathEscape(b.Filename), nil
}

func (s *Server) pipMetadata(ctx context.Context, name string) ([]pipFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if files, ok := s.pip[name]; ok {
		return files, nil
	}
	var files []pipFile
	if p, ok := s.find("pip", name); ok {
		if p.Latest[s.Platform] == "" {
			return nil, fmt.Errorf("%s is unavailable for %s", name, s.Platform)
		}
		for _, v := range p.Versions {
			for _, a := range v.PipArtifacts {
				artifact := a
				files = append(files, pipFile{Filename: a.Filename, URL: a.URL, Hashes: map[string]string{"sha256": a.SHA256}, RequiresPython: a.RequiresPython, Size: a.Size, CatalogArtifact: &artifact})
			}
		}
	} else {
		var doc struct {
			Files []pipFile `json:"files"`
		}
		if err := s.getJSON(ctx, s.PipURL+"/"+name+"/", "application/vnd.pypi.simple.v1+json", &doc); err != nil {
			return nil, err
		}
		files = doc.Files
	}
	for i := range files {
		f := &files[i]
		base, _ := url.Parse(s.PipURL + "/" + name + "/")
		u, err := url.Parse(f.URL)
		if err != nil {
			return nil, err
		}
		u = base.ResolveReference(u)
		u.Fragment = ""
		f.URL, err = s.addBlob(&blob{URL: u.String(), SHA256: f.Hashes["sha256"], Size: f.Size, Filename: f.Filename, PipArtifact: f.CatalogArtifact})
		if err != nil {
			return nil, err
		}
	}
	s.pip[name] = files
	return files, nil
}

func (s *Server) npmMetadata(ctx context.Context, name string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta, ok := s.npm[name]; ok {
		return meta, nil
	}
	var meta map[string]any
	if p, ok := s.find("npm", name); ok {
		if p.Latest[s.Platform] == "" {
			return nil, fmt.Errorf("%s is unavailable for %s", name, s.Platform)
		}
		versions := map[string]any{}
		for _, v := range p.Versions {
			a := v.NpmArtifacts
			var manifest map[string]any
			if err := json.Unmarshal(a.PackageJSON, &manifest); err != nil {
				return nil, err
			}
			manifest["dist"] = map[string]any{"tarball": a.URL, "integrity": sha256Integrity(a.SHA256), "size": float64(a.Size)}
			versions[v.Version] = manifest
		}
		meta = map[string]any{"name": name, "versions": versions, "dist-tags": map[string]any{"latest": p.Latest[s.Platform]}}
	} else if err := s.getJSON(ctx, s.NpmURL+"/"+url.PathEscape(name), "application/json", &meta); err != nil {
		return nil, err
	}
	versions, ok := meta["versions"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("npm metadata has no versions for %s", name)
	}
	for version, value := range versions {
		manifest, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid npm manifest")
		}
		if manifest["name"] != name || manifest["version"] != version {
			return nil, fmt.Errorf("npm manifest name/version mismatch")
		}
		// Old upstream versions sometimes use Git dependencies. Keep them out of
		// the candidate set so the client cannot bypass the verified registry.
		if err := ValidateNpmDependencies(manifest); err != nil {
			delete(versions, version)
			continue
		}
		dist, ok := manifest["dist"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("npm manifest has no dist")
		}
		address, _ := dist["tarball"].(string)
		integrity, _ := dist["integrity"].(string)
		shasum, _ := dist["shasum"].(string)
		size, _ := dist["size"].(float64)
		u, err := url.Parse(address)
		if err != nil {
			return nil, err
		}
		filename := strings.TrimPrefix(u.Path[strings.LastIndex(u.Path, "/")+1:], "/")
		var adapted *catalog.NpmArtifact
		if p, ok := s.find("npm", name); ok {
			for _, v := range p.Versions {
				if v.Version == version {
					adapted = v.NpmArtifacts
				}
			}
		}
		local, err := s.addBlob(&blob{URL: address, Integrity: integrity, SHA1: shasum, Size: int64(size), Filename: filename, NpmArtifact: adapted})
		if err != nil {
			return nil, err
		}
		// Other dist metadata can contain external signatures/attestation URLs.
		manifest["dist"] = map[string]any{"tarball": local, "integrity": integrity, "shasum": shasum}
	}
	s.npm[name] = meta
	return meta, nil
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
