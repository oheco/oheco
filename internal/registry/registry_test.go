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
func fileRecord(address, name string, b []byte) catalog.File {
	h := sha256.Sum256(b)
	return catalog.File{URL: address, SHA256: hex.EncodeToString(h[:]), Size: int64(len(b)), Filename: name}
}
func testClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
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

func TestNpmClientDependencyLockReuseAndRemoval(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm unavailable")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	rootManifest := `{"name":"@oheco-test/root","version":"1.0.0","main":"index.js","dependencies":{"oo-fixture-dep":"^1.0.0"},"scripts":{"install":"exit 99"}}`
	depManifest := `{"name":"oo-fixture-dep","version":"1.0.0","main":"index.js"}`
	rootBytes := npmArchive(t, rootManifest, map[string]string{"index.js": "module.exports = require('oo-fixture-dep')"})
	depBytes := npmArchive(t, depManifest, map[string]string{"index.js": "module.exports = 42"})
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/root.tgz":
			downloads.Add(1)
			w.Write(rootBytes)
		case "/dep.tgz":
			downloads.Add(1)
			w.Write(depBytes)
		case "/oo-fixture-dep":
			var manifest map[string]any
			json.Unmarshal([]byte(depManifest), &manifest)
			f := fileRecord(upstream.URL+"/dep.tgz", "dep.tgz", depBytes)
			manifest["dist"] = map[string]any{"tarball": f.URL, "integrity": sha256Integrity(f.SHA256)}
			json.NewEncoder(w).Encode(map[string]any{"name": "oo-fixture-dep", "dist-tags": map[string]string{"latest": "1.0.0"}, "versions": map[string]any{"1.0.0": manifest}})
		default:
			t.Errorf("unexpected upstream request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := fixturePackage("root", "npm", "@oheco-test/root", "1.0.0")
	p.Versions = []catalog.Version{{Version: "1.0.0", NpmArtifacts: &catalog.NpmArtifact{File: fileRecord(upstream.URL+"/root.tgz", "root.tgz", rootBytes), PackageJSON: json.RawMessage(rootManifest)}}}
	cfg := Config{Index: fixtureIndex(p), Platform: "ohos-arm64", Cache: t.TempDir(), Client: testClient(), NpmURL: upstream.URL}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"consumer","version":"1.0.0","private":true}`), 0644)
	npmrc := filepath.Join(dir, "empty.npmrc")
	os.WriteFile(npmrc, nil, 0600)
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
		args := append(operation, "--registry="+s.URL+"/npm/", "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier", "--omit-lockfile-registry-resolved", "--fetch-retries=0", "--cache="+filepath.Join(dir, fmt.Sprint("npm-cache-", iteration)), "--userconfig="+npmrc, "--globalconfig="+os.DevNull)
		runClient(t, dir, npm, args...)
		s.Close()
		lock, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(lock, []byte("127.0.0.1")) || bytes.Contains(lock, []byte(`"resolved"`)) {
			t.Fatalf("lock contains temporary URL: %s", lock)
		}
		if got := strings.TrimSpace(runClient(t, dir, node, "-e", "console.log(require('@oheco-test/root'))")); got != "42" {
			t.Fatal(got)
		}
	}
	if downloads.Load() != 2 {
		t.Fatalf("verified cache not reused: %d downloads", downloads.Load())
	}
	runClient(t, dir, npm, "uninstall", "@oheco-test/root", "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier", "--userconfig="+npmrc, "--globalconfig="+os.DevNull)
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@oheco-test", "root")); !os.IsNotExist(err) {
		t.Fatalf("package remains after npm removal: %v", err)
	}
}

func TestPipClientWheelSelectionDependencyAndRemoval(t *testing.T) {
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
		case "/root.whl":
			w.Write(rootBytes)
		case "/dep.whl":
			w.Write(depBytes)
		default:
			t.Errorf("unexpected artifact: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	root := fixturePackage("py-root", "pip", "oo-fixture-root", "1.0.0")
	dep := fixturePackage("py-dep", "pip", "oo-fixture-dep", "1.0.0")
	a := catalog.PipArtifact{File: fileRecord(upstream.URL+"/root.whl", "oo_fixture_root-1.0.0-py3-none-any.whl", rootBytes), RequiresPython: ">=3.8", RequiresDist: []string{"oo-fixture-dep==1.0.0"}}
	wrong := a
	wrong.Filename = "oo_fixture_root-1.0.0-cp310-cp310-win_amd64.whl"
	wrong.URL = upstream.URL + "/must-not-download.whl"
	root.Versions = []catalog.Version{{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{wrong, a}}}
	dep.Versions = []catalog.Version{{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{{File: fileRecord(upstream.URL+"/dep.whl", "oo_fixture_dep-1.0.0-py3-none-any.whl", depBytes), RequiresPython: ">=3.8"}}}}
	s, err := Start(context.Background(), Config{Index: fixtureIndex(root, dep), Platform: "ohos-arm64", Cache: t.TempDir(), Client: testClient()})
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

func TestIntegrityFailureAndReadOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "corrupt") }))
	defer upstream.Close()
	p := fixturePackage("bad", "npm", "bad", "1.0.0")
	p.Versions = []catalog.Version{{Version: "1.0.0", NpmArtifacts: &catalog.NpmArtifact{File: fileRecord(upstream.URL+"/bad+ohos.1.tgz", "bad+ohos.1.tgz", []byte("correct")), PackageJSON: json.RawMessage(`{"name":"bad","version":"1.0.0"}`)}}}
	s, err := Start(context.Background(), Config{Index: fixtureIndex(p), Platform: "ohos-arm64", Cache: t.TempDir(), Client: testClient()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := testClient()
	resp, err := client.Get(s.URL + "/npm/bad")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	address := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)["tarball"].(string)
	if !strings.HasPrefix(address, s.URL+"/files/") {
		t.Fatal("external artifact URL leaked")
	}
	resp, err = client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || !strings.Contains(string(body), "integrity mismatch") {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest("PUT", s.URL+"/npm/bad", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatal(resp.Status)
	}
	req, _ = http.NewRequest("GET", s.URL+"/npm/bad", nil)
	req.Host = "example.com"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal(resp.Status)
	}
}
