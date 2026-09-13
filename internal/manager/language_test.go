package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

func TestLanguageInstallUsesNpmAndPreservesUserConfig(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm unavailable")
	}
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // Keep npm's cache/logs out of the user's home.
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"consumer","version":"1.0.0"}`), 0644)
	config := []byte("save-exact=true\n")
	os.WriteFile(filepath.Join(dir, ".npmrc"), config, 0600)
	manifest := `{"name":"oo-language-fixture","version":"1.0.0","main":"index.js","scripts":{"install":"exit 99"}}`
	data := archive(t, entry{name: "package/package.json", content: manifest, mode: 0644}, entry{name: "package/index.js", content: "module.exports = 42", mode: 0644})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture.tgz" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	var out bytes.Buffer
	m, err := New(filepath.Join(dir, "oo"), server.URL+"/index.json", &out)
	if err != nil {
		t.Fatal(err)
	}
	a := artifact(data, server.URL+"/fixture.tgz", nil)
	p := catalog.Package{SchemaVersion: 3, Name: "fixture", PackageManager: "npm", PackageName: "oo-language-fixture", Description: "fixture", Upstream: "https://example.com/fixture", Repository: "https://github.com/oheco/fixture", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{m.Platform: "1.0.0"}, Versions: []catalog.Version{{Version: "1.0.0", NpmArtifacts: &catalog.NpmArtifact{File: catalog.File{URL: a.URL, SHA256: a.SHA256, Size: a.Size, Filename: "fixture.tgz"}, PackageJSON: json.RawMessage(manifest)}}}}
	idx := catalog.Index{SchemaVersion: 3, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Packages: []catalog.Package{p}}
	if err := m.prepare(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(idx)
	os.WriteFile(filepath.Join(m.Root, "index", "index.json"), b, 0644)
	if err := m.InstallLanguage(context.Background(), idx, "npm", []string{"oo-language-fixture@1.0.0"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	cmd := exec.Command("node", "-e", "console.log(require('oo-language-fixture'))")
	cmd.Dir = dir
	got, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(got)) != "42" {
		t.Fatalf("import: %v %s", err, got)
	}
	lock, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(lock, []byte("127.0.0.1")) {
		t.Fatal("ephemeral registry in lockfile")
	}
	got, err = os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil || !bytes.Equal(got, config) {
		t.Fatal("user configuration changed")
	}
	if err := m.RemoveLanguage(context.Background(), "npm", []string{"oo-language-fixture"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "oo-language-fixture")); !os.IsNotExist(err) {
		t.Fatal("npm package not removed")
	}
	leftovers, _ := filepath.Glob(filepath.Join(m.Root, "tmp", "npmrc-*"))
	if len(leftovers) != 0 {
		t.Fatal("temporary configuration remains")
	}
}

func TestLanguagePipResolvesWheelDependenciesInIsolatedTarget(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	if err := exec.Command(python, "-B", "-m", "pip", "--version").Run(); err != nil {
		t.Skip("pip unavailable")
	}
	t.Setenv("OHECO_PYTHON", python)
	t.Setenv("HOME", t.TempDir())
	rootBytes := languageWheel(t, "oo_language_root", "oo-language-dep==1.0.0")
	depBytes := languageWheel(t, "oo_language_dep", "")
	// pip derives the candidate wheel name from the link URL's basename, so the
	// catalog artifact URL must end in the real wheel filename.
	rootFilename := "oo_language_root-1.0.0-py3-none-any.whl"
	depFilename := "oo_language_dep-1.0.0-py3-none-any.whl"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + rootFilename:
			w.Write(rootBytes)
		case "/" + depFilename:
			w.Write(depBytes)
		default:
			t.Errorf("unexpected artifact request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	var out bytes.Buffer
	m, err := New(filepath.Join(dir, "oo"), server.URL+"/unused-index.json", &out)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != strings.TrimPrefix(server.URL, "http://") {
			return nil, fmt.Errorf("external network forbidden in language test: %s", r.URL.Host)
		}
		return transport.RoundTrip(r)
	})}
	idx := languageTestIndex()
	for i, name := range []string{"oo-language-root", "oo-language-dep"} {
		data, filename := rootBytes, rootFilename
		if i == 1 {
			data, filename = depBytes, depFilename
		}
		address := server.URL + "/" + filename
		a := artifact(data, address, nil)
		wheel := catalog.PipArtifact{File: catalog.File{URL: address, SHA256: a.SHA256, Size: a.Size, Filename: filename}, RequiresPython: ">=3.8"}
		if i == 0 {
			wheel.RequiresDist = []string{"oo-language-dep==1.0.0"}
		}
		p := catalog.Package{SchemaVersion: 3, Name: name, PackageManager: "pip", PackageName: name, Description: "isolated wheel fixture", Upstream: "https://example.com/fixture", Repository: "https://github.com/oheco/fixture", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{m.Platform: "1.0.0"}, Versions: []catalog.Version{{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{wheel}}}}
		idx.Packages = append(idx.Packages, p)
	}
	target := filepath.Join(dir, "target with spaces")
	if err := m.InstallLanguage(context.Background(), idx, "pip", []string{"oo-language-root==1.0.0"}, LanguageOptions{Args: []string{"--target", target, "--no-cache-dir"}, Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	cmd := exec.Command(python, "-B", "-c", "import sys; sys.path.insert(0, sys.argv[1]); import oo_language_root, oo_language_dep; print(oo_language_root.value + oo_language_dep.value)", target)
	got, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(got)) != "84" {
		t.Fatalf("dependency import: %v %s", err, got)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "state")); !os.IsNotExist(err) {
		t.Fatalf("pip install created oo records: %v", err)
	}
}

func languageWheel(t *testing.T, name, dependency string) []byte {
	t.Helper()
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	dist := name + "-1.0.0.dist-info/"
	metadata := "Metadata-Version: 2.1\nName: " + strings.ReplaceAll(name, "_", "-") + "\nVersion: 1.0.0\nRequires-Python: >=3.8\n"
	if dependency != "" {
		metadata += "Requires-Dist: " + dependency + "\n"
	}
	files := map[string]string{name + "/__init__.py": "value = 42\n", dist + "METADATA": metadata + "\n", dist + "WHEEL": "Wheel-Version: 1.0\nGenerator: oo-tests\nRoot-Is-Purelib: true\nTag: py3-none-any\n", dist + "RECORD": ""}
	for path, content := range files {
		w, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestLanguageRejectsSourceAndScriptBypasses(t *testing.T) {
	for _, args := range [][]string{
		{"--registry=https://evil.invalid"},
		{"--userconfig", "npmrc"},
		{"--globalconfig=/tmp/npmrc"},
		{"--ignore-scripts=false"},
		{"--script-shell=/tmp/script"},
		{"--workspace=other"},
		{"--node-options=--require=/tmp/evil.js"},
		{"https://evil.invalid/a.tgz"},
		{"other-root"},
	} {
		if err := validateLanguageOptions("npm", args); err == nil {
			t.Fatalf("npm accepted %v", args)
		}
	}
	for _, args := range [][]string{
		{"--index-url=https://evil.invalid"},
		{"--extra-index-url=https://evil.invalid"},
		{"--find-links=./wheels"},
		{"-r", "requirements.txt"},
		{"--only-binary=:none:"},
		{"--no-binary=:all:"},
		{"--isolated"},
		{"--python=/other/python"},
		{"pkg @ https://evil.invalid/a.whl"},
		{"./pkg.whl"},
	} {
		if err := validateLanguageOptions("pip", args); err == nil {
			t.Fatalf("pip accepted %v", args)
		}
	}
	if err := validateLanguageTargets("npm", []string{"https://evil.invalid/a.tgz", "git+https://evil.invalid/x.git", "file:/tmp/a"}, true); err == nil {
		t.Fatal("accepted an npm target that is not a registry package name")
	}
	if err := validateLanguageTargets("pip", []string{"pkg @ https://evil.invalid/a.whl", "./pkg.whl"}, true); err == nil {
		t.Fatal("accepted a pip target that is not a named requirement")
	}
}
