package registry

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

// The registry no longer serves artifacts: catalog metadata points straight at
// the URLs described in the catalog, and everything else is forwarded to the
// sources the user configured. These fixtures only need a plausible URL for the
// forwarded case, so no bytes are ever fetched by the tests themselves.

func fixturePackage(name, manager, ecosystem, version string) catalog.Package {
	return catalog.Package{SchemaVersion: 3, Name: name, PackageManager: manager, PackageName: ecosystem, Description: "fixture", Upstream: "https://example.com/upstream", Repository: "https://github.com/oheco/fixture", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{"ohos-arm64": version}}
}
func fixtureIndex(packages ...catalog.Package) catalog.Index {
	return catalog.Index{SchemaVersion: 3, GeneratedAt: "2026-09-12T00:00:00Z", Packages: packages}
}
func npmArchive(t *testing.T, manifest string, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	files["package.json"] = manifest
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func wheelArchive(t *testing.T, name, dependency string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	meta := "Metadata-Version: 2.1\nName: " + name + "\nVersion: 1.0.0\nRequires-Python: >=3.8\n"
	if dependency != "" {
		meta += "Requires-Dist: " + dependency + "\n"
	}
	meta += "\n"
	files := map[string]string{name + ".py": "value = 42\n", name + "-1.0.0.dist-info/METADATA": meta, name + "-1.0.0.dist-info/WHEEL": "Wheel-Version: 1.0\nGenerator: oo-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n", name + "-1.0.0.dist-info/RECORD": ""}
	for name, body := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func fileRecord(address string, b []byte) catalog.File {
	h := sha256.Sum256(b)
	name := address[strings.LastIndex(address, "/")+1:]
	if i := strings.IndexAny(name, "#?"); i >= 0 {
		name = name[:i]
	}
	return catalog.File{URL: address, SHA256: hex.EncodeToString(h[:]), Size: int64(len(b)), Filename: name}
}

// npmFixturePackage builds a catalog npm package whose artifact is addressed by
// an external URL, as the A1 model requires. The recorded SHA-256 and size come
// from the real bytes so npm's own integrity check can verify them.
func npmFixturePackage(t *testing.T, name, ecosystem, version, address, manifest string, artifact []byte) catalog.Package {
	t.Helper()
	p := fixturePackage(name, "npm", ecosystem, version)
	p.Latest = map[string]string{"ohos-arm64": version}
	p.Versions = []catalog.Version{{Version: version, NpmArtifacts: &catalog.NpmArtifact{File: fileRecord(address, artifact), PackageJSON: json.RawMessage(manifest)}}}
	return p
}

// syntheticArtifact stands in for bytes no test downloads.
func syntheticArtifact(ecosystem, version, manifest string) []byte {
	return []byte("artifact:" + ecosystem + "@" + version + "\n" + manifest)
}

// pipFixtureName returns a distributable wheel filename for a project/version.
func pipFixtureName(project, version, platform string) string {
	return strings.ReplaceAll(project, "-", "_") + "-" + version + "-py3-none-" + platform + ".whl"
}

func testClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

// etagFor derives a strong validator the way an artifact CDN would.
func etagFor(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

func getBody(t *testing.T, s *Server, path string) (*http.Response, string) {
	t.Helper()
	resp, err := testClient().Get(s.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestNpmCatalogPackumentUsesDescribedArtifact(t *testing.T) {
	var tarball, other, skipped = "https://github.com/oheco/fixture/releases/download/v1.0.0/pkg@1.0.0.tgz", "https://github.com/oheco/fixture/releases/download/v1.0.0/pkg@2.0.0.tgz", "https://github.com/oheco/fixture/releases/download/v1.0.0/pkg@3.0.0.tgz"
	good := `{"name":"oo-lang-npm","version":"1.0.0","dependencies":{"left-pad":"^1.0.0"}}`
	other1 := `{"name":"oo-lang-npm","version":"2.0.0"}`
	bad := `{"name":"oo-lang-npm","version":"3.0.0","dependencies":{"evil":"git+https://github.com/oheco/evil"}}`
	pkg := npmFixturePackage(t, "pkg-npm", "oo-lang-npm", "1.0.0", tarball, good, syntheticArtifact("oo-lang-npm", "1.0.0", good))
	pkg.Latest = map[string]string{"ohos-arm64": "2.0.0"}
	pkg.Versions = append(pkg.Versions,
		catalog.Version{Version: "2.0.0", NpmArtifacts: &catalog.NpmArtifact{File: fileRecord(other, syntheticArtifact("oo-lang-npm", "2.0.0", other1)), PackageJSON: json.RawMessage(other1)}},
		catalog.Version{Version: "3.0.0", NpmArtifacts: &catalog.NpmArtifact{File: fileRecord(skipped, syntheticArtifact("oo-lang-npm", "3.0.0", bad)), PackageJSON: json.RawMessage(bad)}},
	)
	// A non-catalog name must never be fetched from the catalog; fail loudly if
	// the registry tries to forward instead of failing.
	var forwarded atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	s, err := Start(context.Background(), Config{Index: fixtureIndex(pkg), Platform: "ohos-arm64", Client: testClient(), NpmRegistry: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/npm/oo-lang-npm")
	if resp.StatusCode != 200 {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type: %s", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache control: %q", cc)
	}
	var doc struct {
		Name     string                    `json:"name"`
		DistTags map[string]string         `json:"dist-tags"`
		Versions map[string]map[string]any `json:"versions"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "oo-lang-npm" || doc.DistTags["latest"] != "2.0.0" {
		t.Fatalf("%+v", doc)
	}
	if _, ok := doc.Versions["3.0.0"]; ok {
		t.Fatal("a version with a Git dependency must be dropped")
	}
	if len(doc.Versions) != 2 {
		t.Fatalf("versions: %v", doc.Versions)
	}
	artifactBytes := syntheticArtifact("oo-lang-npm", "1.0.0", good)
	want := sha256Integrity(fileRecord(tarball, artifactBytes).SHA256)
	dist, ok := doc.Versions["1.0.0"]["dist"].(map[string]any)
	if !ok {
		t.Fatalf("no dist: %v", doc.Versions["1.0.0"])
	}
	if dist["tarball"] != tarball {
		t.Fatalf("tarball must point at the described URL, got %v", dist["tarball"])
	}
	if dist["integrity"] != want {
		t.Fatalf("integrity = %v, want %s", dist["integrity"], want)
	}
	if dist["size"] != float64(len(artifactBytes)) {
		t.Fatalf("size = %v", dist["size"])
	}
	if dist2 := doc.Versions["2.0.0"]["dist"].(map[string]any); dist2["tarball"] != other {
		t.Fatalf("second version tarball = %v", dist2["tarball"])
	}
	// Each version must own its own dist map: sharing one would make npm verify
	// a tarball against another version's integrity.
	versionTwo := syntheticArtifact("oo-lang-npm", "2.0.0", other1)
	if want := sha256Integrity(fileRecord(other, versionTwo).SHA256); doc.Versions["2.0.0"]["dist"].(map[string]any)["integrity"] != want {
		t.Fatalf("second version integrity = %v, want %s", doc.Versions["2.0.0"]["dist"].(map[string]any)["integrity"], want)
	}
	if want := float64(len(versionTwo)); doc.Versions["2.0.0"]["dist"].(map[string]any)["size"] != want {
		t.Fatalf("second version size = %v, want %v", doc.Versions["2.0.0"]["dist"].(map[string]any)["size"], want)
	}
	if strings.Contains(body, "/files/") {
		t.Fatal("metadata still references the removed /files/ route")
	}
	if forwarded.Load() != 0 {
		t.Fatalf("catalog package hit the upstream %d times", forwarded.Load())
	}
}

func TestNpmCatalogUnavailablePlatform(t *testing.T) {
	manifest := `{"name":"oo-lang-npm","version":"1.0.0"}`
	pkg := npmFixturePackage(t, "pkg-npm", "oo-lang-npm", "1.0.0", "https://github.com/oheco/fixture/releases/download/v1.0.0/pkg.tgz", manifest, syntheticArtifact("oo-lang-npm", "1.0.0", manifest))
	pkg.Latest = map[string]string{"windows-x64": "1.0.0"}
	s, err := Start(context.Background(), Config{Index: fixtureIndex(pkg), Platform: "ohos-arm64", Client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/npm/oo-lang-npm")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "unavailable for ohos-arm64") {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
}

func TestNpmForwardingPreservesScopedEscaping(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("primary registry must not receive a scoped override: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer primary.Close()
	scoped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("scoped registry must not be contacted; only the redirect target matters: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer scoped.Close()
	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), NpmRegistry: primary.URL, NpmScoped: map[string]string{"@vscode": scoped.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct{ path, want string }{
		{"/npm/@vscode%2fripgrep", scoped.URL + "/@vscode%2fripgrep"},
		{"/npm/@other%2fripgrep", primary.URL + "/@other%2fripgrep"},
		{"/npm/left-pad", primary.URL + "/left-pad"},
	} {
		resp, err := client.Get(s.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s: %d", tc.path, resp.StatusCode)
		}
		if got := resp.Header.Get("Location"); got != tc.want {
			t.Fatalf("%s: location = %q, want %q", tc.path, got, tc.want)
		}
	}
	// A scope override is enough on its own for scoped names; only "no registry
	// and no matching scope" is an error.
	s2, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), NpmScoped: map[string]string{"@vscode": scoped.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	resp, err := client.Get(s2.URL + "/npm/@vscode%2fripgrep")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != scoped.URL+"/@vscode%2fripgrep" {
		t.Fatalf("scope-only forwarding: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = client.Get(s2.URL + "/npm/left-pad")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unscoped name without a registry: %d", resp.StatusCode)
	}
}

func TestNpmForwardingRequiresConfiguration(t *testing.T) {
	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/npm/left-pad")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "npm registry") {
		t.Fatalf("error must name the missing npm registry: %s", body)
	}
}

func TestPipCatalogServesOnlyAdaptedWheels(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		t.Errorf("an upstream index must not be consulted for a catalog package: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	goodName := pipFixtureName("oo-lang-root", "1.0.0", "any")
	goodURL := "https://github.com/oheco/fixture/releases/download/v1.0.0/" + goodName
	good := []byte("wheel-one")
	otherName := pipFixtureName("oo-lang-root", "2.0.0", "linux_aarch64")
	otherURL := "https://github.com/oheco/fixture/releases/download/v1.0.0/" + otherName
	secondName := pipFixtureName("oo-lang-root", "2.0.0", "any")
	secondURL := "https://github.com/oheco/fixture/releases/download/v2.0.0/" + secondName
	// Never published by the catalog: the registry must not invent candidates.
	notInCatalog := pipFixtureName("oo-lang-root", "9.9.9", "win_amd64")
	p := fixturePackage("pkg-py-root", "pip", "oo-lang-root", "1.0.0")
	p.Latest = map[string]string{"ohos-arm64": "2.0.0"}
	p.Versions = []catalog.Version{
		{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{{File: fileRecord(goodURL, good), RequiresPython: ">=3.8"}}},
		{Version: "2.0.0", PipArtifacts: []catalog.PipArtifact{
			{File: fileRecord(otherURL, []byte("wheel-two")), RequiresPython: ">=3.10"},
			{File: fileRecord(secondURL, []byte("wheel-three")), RequiresPython: ">=3.9"},
		}},
	}
	s, err := Start(context.Background(), Config{Index: fixtureIndex(p), Platform: "ohos-arm64", Client: testClient(), PipIndexes: []string{upstream.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/simple/oo_lang_root/")
	if resp.StatusCode != 200 {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type: %s", ct)
	}
	wantHash := fileRecord(goodURL, good).SHA256
	if !strings.Contains(body, `<a href="`+goodURL+`#sha256=`+wantHash+`" data-requires-python="&gt;=3.8">`+goodName+`</a>`) {
		t.Fatalf("catalog wheel link missing: %s", body)
	}
	if !strings.Contains(body, secondName) || !strings.Contains(body, otherName) {
		t.Fatalf("an adapted wheel is missing: %s", body)
	}
	if strings.Contains(body, notInCatalog) || strings.Contains(body, "/files/") {
		t.Fatalf("a wheel outside the catalog or the removed /files/ route leaked: %s", body)
	}
	if got := strings.Count(body, "<a "); got != 3 {
		t.Fatalf("expected exactly the adapted wheels, got %d links: %s", got, body)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("catalog package consulted the upstream index")
	}
}

func TestPipForwardingAggregatesAndPreservesMetadata(t *testing.T) {
	sumOne := sha256.Sum256([]byte("one"))
	sumTwo := sha256.Sum256([]byte("two"))
	jsonIndex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/left-pad/" {
			t.Errorf("unexpected json index path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Accept"); !strings.Contains(got, "application/vnd.pypi.simple.v1+json") || !strings.Contains(got, "text/html") {
			t.Errorf("accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		fmt.Fprintf(w, `{"files":[{"filename":"left_pad-1.0.0-py3-none-any.whl","url":"packages/one.whl","hashes":{"sha256":"%s"},"requires-python":">=3.8","size":11},{"filename":"left_pad-2.0.0-py3-none-any.whl","url":"https://cdn.example.com/two.whl#sha256=%s","requires-python":">=3.9"},{"filename":"left_pad-0.9.0-py3-none-any.whl","url":"packages/dup.whl"}]}`,
			hex.EncodeToString(sumOne[:]), hex.EncodeToString(sumTwo[:]))
	}))
	defer jsonIndex.Close()
	htmlIndex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/simple/left-pad/" {
			t.Errorf("unexpected html index path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Alibaba Cloud style relative hrefs -- "../../packages/...".
		fmt.Fprint(w, `<!doctype html><html><body>`+"\n"+
			`<a href="../../packages/one.whl#sha256=`+hex.EncodeToString(sumOne[:])+`" data-requires-python="&gt;=3.8">left_pad-1.0.0-py3-none-any.whl</a>`+"\n"+
			`<a href="../../packages/three.whl" data-requires-python="&gt;=3.7" data-yanked="bad build">left_pad-1.1.0-py3-none-any.whl</a>`+"\n"+
			`</body></html>`)
	}))
	defer htmlIndex.Close()
	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), PipIndexes: []string{jsonIndex.URL + "/simple/", htmlIndex.URL + "/simple/"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/simple/left-pad/")
	if resp.StatusCode != 200 {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	// The JSON href was relative to the PEP 691 page; the HTML href climbed two
	// directories. Both must be absolute in the merged page.
	wantOne := jsonIndex.URL + "/simple/left-pad/packages/one.whl#sha256=" + hex.EncodeToString(sumOne[:])
	wantTwo := "https://cdn.example.com/two.whl#sha256=" + hex.EncodeToString(sumTwo[:])
	wantThree := htmlIndex.URL + "/packages/three.whl"
	if !strings.Contains(body, wantOne) {
		t.Fatalf("relative json href not resolved: %s", body)
	}
	if !strings.Contains(body, wantTwo) {
		t.Fatalf("absolute json href/fragment not preserved: %s", body)
	}
	if !strings.Contains(body, wantThree) {
		t.Fatalf("relative html href not resolved: %s", body)
	}
	if !strings.Contains(body, `<a href="`+wantThree+`" data-requires-python="&gt;=3.7" data-yanked="bad build">left_pad-1.1.0-py3-none-any.whl</a>`) {
		t.Fatalf("requires-python/yanked not passed through: %s", body)
	}
	if got := strings.Count(body, "left_pad-1.0.0-py3-none-any.whl"); got != 1 {
		t.Fatalf("duplicate filename not de-duplicated (%d): %s", got, body)
	}
	if !strings.Contains(body, `<a href="`+wantOne+`" data-requires-python="&gt;=3.8">`) {
		t.Fatalf("missing requires-python on the json link: %s", body)
	}
	// left_pad-0.9.0 had no sha256: it is still listed, without a fragment.
	if !strings.Contains(body, `>left_pad-0.9.0-py3-none-any.whl</a>`) {
		t.Fatalf("hash-less link missing: %s", body)
	}
	if strings.Contains(body, "0.9.0-py3-none-any.whl#") {
		t.Fatalf("hash-less link gained a fragment: %s", body)
	}
	if got := strings.Count(body, "<a "); got != 4 {
		t.Fatalf("expected 4 de-duplicated links, got %d: %s", got, body)
	}
	// Links must resolve against the upstream index page, never against oo.
	if strings.Contains(body, s.URL) {
		t.Fatalf("forwarded link resolved against the local registry: %s", body)
	}
}

func TestPipForwardingFailsWhenOneIndexFails(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="packages/ok.whl">left_pad-1.0.0-py3-none-any.whl</a>`)
	}))
	defer healthy.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()
	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), PipIndexes: []string{healthy.URL, broken.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/simple/left-pad/")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "HTTP 500") {
		t.Fatalf("error must identify the failing index: %s", body)
	}
}

// TestPipForwardingSkipsIndexesWithoutTheProject pins that a 404 from one
// configured index means "this index does not host the project", which is the
// normal case with extra-index-url and must not fail the whole request.
func TestPipForwardingSkipsIndexesWithoutTheProject(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<a href="packages/ok.whl#sha256=%s">left_pad-1.0.0-py3-none-any.whl</a>`, digest)
	}))
	defer healthy.Close()
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer missing.Close()

	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), PipIndexes: []string{missing.URL, healthy.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/simple/left-pad/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a 404 from one index must not fail aggregation: %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "left_pad-1.0.0-py3-none-any.whl") || !strings.Contains(body, digest) {
		t.Fatalf("files from the healthy index are missing: %s", body)
	}

	// When every configured index answers 404 the project simply has no files,
	// which the client reports as "no matching distribution".
	empty, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), PipIndexes: []string{missing.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	resp, body = getBody(t, empty, "/simple/left-pad/")
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "whl") {
		t.Fatalf("a project missing everywhere must render an empty index: %d: %s", resp.StatusCode, body)
	}
}

func TestPipForwardingRequiresConfiguration(t *testing.T) {
	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resp, body := getBody(t, s, "/simple/left-pad/")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "pip index") {
		t.Fatalf("error must name the missing pip index: %s", body)
	}
}

func TestServerSecurityBoundaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="packages/ok.whl">left_pad-1.0.0-py3-none-any.whl</a>`)
	}))
	defer upstream.Close()
	s, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64", Client: testClient(), NpmRegistry: upstream.URL, PipIndexes: []string{upstream.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := testClient()
	req, _ := http.NewRequest("PUT", s.URL+"/npm/left-pad", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
	// The host check runs before the method check, as before.
	req, _ = http.NewRequest("PUT", s.URL+"/npm/left-pad", nil)
	req.Host = "example.com"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("host check: %d", resp.StatusCode)
	}
	for _, path := range []string{"/", "/files/abc/anything.tgz", "/files/", "/packages/x.tgz", "/npm/", "/simple/"} {
		resp, err := client.Get(s.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	// The nonce prefix is required.
	u := strings.SplitN(strings.TrimPrefix(s.URL, "http://"), "/", 2)
	if len(u) != 2 {
		t.Fatalf("unexpected server URL %s", s.URL)
	}
	resp, err = client.Get("http://" + u[0] + "/npm/left-pad")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("missing nonce: %d", resp.StatusCode)
	}
	// HEAD is allowed and bodyless.
	resp, err = client.Head(s.URL + "/simple/left-pad/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
}

func TestServerRejectsMissingClientAndInvalidSources(t *testing.T) {
	if _, err := Start(context.Background(), Config{Index: fixtureIndex(), Platform: "ohos-arm64"}); err == nil {
		t.Fatal("missing client accepted")
	}
	makePkg := func() catalog.Index { return fixtureIndex() }
	// HTTPS is the normal case and must be accepted.
	if _, err := Start(context.Background(), Config{Index: makePkg(), Platform: "ohos-arm64", Client: testClient(), NpmRegistry: "https://registry.npmjs.org"}); err != nil {
		t.Fatalf("https npm registry rejected: %v", err)
	}
	if _, err := Start(context.Background(), Config{Index: makePkg(), Platform: "ohos-arm64", Client: testClient(), PipIndexes: []string{"ftp://mirror.invalid/simple"}}); err == nil {
		t.Fatal("non-http pip index accepted")
	}
	if _, err := Start(context.Background(), Config{Index: makePkg(), Platform: "ohos-arm64", Client: testClient(), NpmScoped: map[string]string{"vscode": "http://127.0.0.1:1"}}); err == nil {
		t.Fatal("scope without @ accepted")
	}
}

func runClient(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "NPM_CONFIG_") || strings.HasPrefix(upper, "PIP_") || strings.Contains(upper, "PROXY") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "PIP_CONFIG_FILE="+os.DevNull, "PIP_DISABLE_PIP_VERSION_CHECK=1", "NO_PROXY=127.0.0.1,localhost")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
	}
	return string(out)
}

// TestNpmClientUsesDescribedArtifactAndCachesIt checks that npm itself fetches
// the tarball from the catalog URL and reuses its own cache; oo is no longer in
// the download path.
func TestNpmClientUsesDescribedArtifactAndCachesIt(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm unavailable")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	rootManifest := `{"name":"@oheco-test/root","version":"1.0.0","main":"index.js","dependencies":{"@oheco-test/dep":"^1.0.0"},"scripts":{"install":"exit 99"}}`
	depManifest := `{"name":"@oheco-test/dep","version":"1.0.0","main":"index.js"}`
	rootBytes := npmArchive(t, rootManifest, map[string]string{"index.js": "module.exports = require('@oheco-test/dep')"})
	depBytes := npmArchive(t, depManifest, map[string]string{"index.js": "module.exports = 42"})
	// A release CDN serves immutable artifacts with validators, which is what
	// lets npm satisfy a second install from its own cache.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		switch r.URL.Path {
		case "/root.tgz":
			body = rootBytes
		case "/dep.tgz":
			body = depBytes
		default:
			http.NotFound(w, r)
			return
		}
		etag := etagFor(body)
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		downloads.Add(1)
		w.Write(body)
	}))
	defer upstream.Close()
	root := npmFixturePackage(t, "pkg-npm-root", "@oheco-test/root", "1.0.0", upstream.URL+"/root.tgz", rootManifest, rootBytes)
	dep := npmFixturePackage(t, "pkg-npm-dep", "@oheco-test/dep", "1.0.0", upstream.URL+"/dep.tgz", depManifest, depBytes)
	cfg := Config{Index: fixtureIndex(root, dep), Platform: "ohos-arm64", Client: testClient()}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"consumer","version":"1.0.0","private":true}`), 0644)
	npmrc := filepath.Join(dir, "empty.npmrc")
	os.WriteFile(npmrc, nil, 0600)
	// One cache directory across both runs: npm, not oo, owns artifact reuse.
	cache := filepath.Join(dir, "npm-cache")
	for iteration := 0; iteration < 2; iteration++ {
		s, err := Start(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		operation := []string{"install", "@oheco-test/root@1.0.0"}
		if iteration == 1 {
			os.RemoveAll(filepath.Join(dir, "node_modules"))
			operation = []string{"ci"}
		}
		args := append(operation, "--registry="+s.URL+"/npm/", "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier", "--omit-lockfile-registry-resolved", "--fetch-retries=0", "--cache="+cache, "--userconfig="+npmrc, "--globalconfig="+os.DevNull)
		runClient(t, dir, npm, args...)
		s.Close()
		lock, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(lock, []byte(s.URL)) {
			t.Fatalf("lock contains the ephemeral registry URL: %s", lock)
		}
		if got := strings.TrimSpace(runClient(t, dir, node, "-e", "console.log(require('@oheco-test/root'))")); got != "42" {
			t.Fatal(got)
		}
	}
	if downloads.Load() != 2 {
		t.Fatalf("npm cache not reused: %d downloads for 2 packages", downloads.Load())
	}
	runClient(t, dir, npm, "uninstall", "@oheco-test/root", "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier", "--userconfig="+npmrc, "--globalconfig="+os.DevNull)
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@oheco-test", "root")); !os.IsNotExist(err) {
		t.Fatalf("package remains after npm removal: %v", err)
	}
}

// TestPipClientDownloadsCatalogWheels checks that pip selects the adapted wheel
// and downloads it from the catalog URL.
func TestPipClientDownloadsCatalogWheels(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	if err := exec.Command(python, "-m", "pip", "--version").Run(); err != nil {
		t.Skip("pip unavailable")
	}
	rootBytes := wheelArchive(t, "oo_fixture_root", "oo-fixture-dep==1.0.0")
	depBytes := wheelArchive(t, "oo_fixture_dep", "")
	var downloads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		switch r.URL.Path {
		case "/oo_fixture_root-1.0.0-py3-none-any.whl":
			w.Write(rootBytes)
		case "/oo_fixture_dep-1.0.0-py3-none-any.whl":
			w.Write(depBytes)
		default:
			t.Errorf("unexpected artifact: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	root := fixturePackage("py-root", "pip", "oo-fixture-root", "1.0.0")
	dep := fixturePackage("py-dep", "pip", "oo-fixture-dep", "1.0.0")
	a := catalog.PipArtifact{File: fileRecord(upstream.URL+"/oo_fixture_root-1.0.0-py3-none-any.whl", rootBytes), RequiresPython: ">=3.8", RequiresDist: []string{"oo-fixture-dep==1.0.0"}}
	wrong := a
	wrong.Filename = "oo_fixture_root-1.0.0-cp310-cp310-win_amd64.whl"
	// The URL must still end with the wheel filename (catalog rule), and the
	// upstream handler fails the test if this incompatible wheel is ever
	// fetched, so keeping the URL realistic preserves the canary behaviour.
	wrong.URL = upstream.URL + "/" + wrong.Filename
	root.Versions = []catalog.Version{{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{wrong, a}}}
	dep.Versions = []catalog.Version{{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{{File: fileRecord(upstream.URL+"/oo_fixture_dep-1.0.0-py3-none-any.whl", depBytes), RequiresPython: ">=3.8"}}}}
	s, err := Start(context.Background(), Config{Index: fixtureIndex(root, dep), Platform: "ohos-arm64", Client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	venv := filepath.Join(dir, "venv")
	runClient(t, dir, python, "-m", "venv", venv)
	py := filepath.Join(venv, "bin", "python3")
	runClient(t, dir, py, "-m", "pip", "install", "oo-fixture-root==1.0.0", "--index-url="+s.URL+"/simple/", "--only-binary=:all:", "--no-cache-dir")
	if got := strings.TrimSpace(runClient(t, dir, py, "-c", "import oo_fixture_root, oo_fixture_dep; print(oo_fixture_root.value + oo_fixture_dep.value)")); got != "84" {
		t.Fatal(got)
	}
	if downloads.Load() != 2 {
		t.Fatalf("wrong wheel selection: %d downloads", downloads.Load())
	}
	runClient(t, dir, py, "-m", "pip", "uninstall", "-y", "oo-fixture-root", "oo-fixture-dep")
	if got := strings.TrimSpace(runClient(t, dir, py, "-c", "import importlib.util; print(importlib.util.find_spec('oo_fixture_root'))")); got != "None" {
		t.Fatal(got)
	}
}

// TestParsePipHTMLVariants pins down the PEP 503 scanner: quoted and unquoted
// attributes, entities, nested markup, and content that must not be mistaken
// for a link.
func TestParsePipHTMLVariants(t *testing.T) {
	page := "https://mirror.example.com/simple/left-pad/"
	doc := `<!doctype html>
<html><head><style>a { color: red }</style>
<script>var href="https://evil.example.com/x.whl";</script></head><body>
<a href="../../packages/one.whl#sha256=ABCDEF" data-requires-python="&gt;=3.8">left_pad-1.0.0-py3-none-any.whl</a>
<a
  href="two.whl"
  data-yanked="broken"> left_pad-2.0.0-py3-none-any.whl </a>
<a href='three.whl'>left_pad&amp;co-3.0.0-py3-none-any.whl</a>
<a href=https://cdn.example.com/four.whl>left_pad-4.0.0-py3-none-any.whl</a>
<a href="javascript:alert(1)">bogus-5.0.0-py3-none-any.whl</a>
<span>not a link</span>
</body></html>`
	files, err := parsePipHTML([]byte(doc), page)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("parsed %d links: %+v", len(files), files)
	}
	one := files[0]
	if one.Filename != "left_pad-1.0.0-py3-none-any.whl" || one.URL != "https://mirror.example.com/packages/one.whl" {
		t.Fatalf("first link: %+v", one)
	}
	if one.Hashes["sha256"] != "abcdef" || one.RequiresPython != ">=3.8" || one.Yanked != nil {
		t.Fatalf("first link metadata: %+v", one)
	}
	two := files[1]
	if two.Filename != "left_pad-2.0.0-py3-none-any.whl" || two.URL != "https://mirror.example.com/simple/left-pad/two.whl" {
		t.Fatalf("second link: %+v", two)
	}
	if two.Yanked != "broken" || two.Hashes["sha256"] != "" {
		t.Fatalf("second link metadata: %+v", two)
	}
	if files[2].Filename != "left_pad&co-3.0.0-py3-none-any.whl" {
		t.Fatalf("entity not decoded: %+v", files[2])
	}
	if files[3].URL != "https://cdn.example.com/four.whl" || files[3].Hashes["sha256"] != "" {
		t.Fatalf("fourth link: %+v", files[3])
	}
}

// TestParsePipJSONFallback ensures a non-JSON body falls back to HTML instead of
// being read as an empty PEP 691 document, and that fragments survive.
func TestParsePipJSONFallback(t *testing.T) {
	page := "https://mirror.example.com/simple/left-pad/"
	if files := parsePipJSON([]byte(`<!doctype html><a href="x.whl">x.whl</a>`), page); files != nil {
		t.Fatalf("html parsed as json: %+v", files)
	}
	doc := `{"files":[{"filename":"x.whl","url":"../../packages/x.whl#sha256=AA","requires-python":">=3.9","size":7},{"filename":"y.whl","url":"packages/y.whl","hashes":{"sha256":"BB"}}]}`
	files := parsePipJSON([]byte(doc), page)
	if len(files) != 2 {
		t.Fatalf("parsed %d files", len(files))
	}
	if files[0].URL != "https://mirror.example.com/packages/x.whl" || files[0].Hashes["sha256"] != "aa" || files[0].Size != 7 {
		t.Fatalf("fragment file: %+v", files[0])
	}
	if files[1].URL != "https://mirror.example.com/simple/left-pad/packages/y.whl" || files[1].Hashes["sha256"] != "bb" {
		t.Fatalf("hashes-only file: %+v", files[1])
	}
	// An empty files array is a valid, successful response.
	if files := parsePipJSON([]byte(`{"files":[]}`), page); files == nil || len(files) != 0 {
		t.Fatalf("empty files array: %+v", files)
	}
}
