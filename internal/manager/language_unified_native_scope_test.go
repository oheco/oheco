package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

// Avoid inheriting destinations, sources or behavior from a real user's backend
// environment. All files, caches, installed packages and venvs are temporary.
func languageNativeScopeEnv(t *testing.T, prefix string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), prefix) {
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			k, v := key, value
			t.Cleanup(func() {
				if err := os.Setenv(k, v); err != nil {
					t.Error(err)
				}
			})
		}
	}
	t.Setenv("HOME", t.TempDir())
}

func TestLanguageDefaultNpmGlobalInIsolatedPrefix(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm unavailable")
	}
	languageNativeScopeEnv(t, "NPM_CONFIG_")
	prefix := filepath.Join(t.TempDir(), "isolated global prefix")
	userconfig, globalconfig := filepath.Join(t.TempDir(), "user.npmrc"), filepath.Join(prefix, "etc", "npmrc")
	if err := os.MkdirAll(filepath.Dir(globalconfig), 0755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{userconfig, globalconfig} {
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NPM_CONFIG_PREFIX", prefix)
	t.Setenv("NPM_CONFIG_CACHE", filepath.Join(t.TempDir(), "npm cache"))
	t.Setenv("NPM_CONFIG_USERCONFIG", userconfig)
	t.Setenv("NPM_CONFIG_GLOBALCONFIG", globalconfig)
	manifest := `{"name":"oo-default-global-fixture","version":"1.0.0","main":"index.js","scripts":{"install":"exit 99"}}`
	data := archive(t, entry{name: "package/package.json", content: manifest, mode: 0644}, entry{name: "package/index.js", content: "module.exports = 42", mode: 0644})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture.tgz" {
			t.Errorf("unexpected artifact request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	var out bytes.Buffer
	m, err := New(filepath.Join(t.TempDir(), "oo absent"), "https://index.invalid/index.json", &out)
	if err != nil {
		t.Fatal(err)
	}
	m.Client = &http.Client{Transport: languageNativeArtifactOnly{t: t, host: strings.TrimPrefix(server.URL, "http://"), base: http.DefaultTransport}}
	idx := languageTestIndex()
	a := artifact(data, server.URL+"/fixture.tgz", nil)
	idx.Packages = append(idx.Packages, catalog.Package{SchemaVersion: 3, Name: "global-fixture", PackageManager: "npm", PackageName: "oo-default-global-fixture", Description: "global scope fixture", Upstream: "https://example.com/fixture", Repository: "https://github.com/oheco/fixture", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{m.Platform: "1.0.0"}, Versions: []catalog.Version{{Version: "1.0.0", NpmArtifacts: &catalog.NpmArtifact{File: catalog.File{URL: a.URL, SHA256: a.SHA256, Size: a.Size, Filename: "fixture.tgz"}, PackageJSON: json.RawMessage(manifest)}}}})
	if err := m.InstallLanguage(context.Background(), idx, "npm", []string{"oo-default-global-fixture@1.0.0"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	installed := filepath.Join(prefix, "lib", "node_modules", "oo-default-global-fixture")
	cmd := exec.Command("node", "-e", "console.log(require(process.argv[1]))", installed)
	if got, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(got)) != "42" {
		t.Fatalf("global import: %v %s", err, got)
	}
	if err := m.RemoveLanguage(context.Background(), "npm", []string{"oo-default-global-fixture"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("default global package not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "state")); !os.IsNotExist(err) {
		t.Fatalf("external install wrote receipts: %v", err)
	}
}

type languageNativeArtifactOnly struct {
	t    *testing.T
	host string
	base http.RoundTripper
}

func (tr languageNativeArtifactOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != tr.host {
		tr.t.Errorf("unexpected external metadata request %s", r.URL.Host)
		return nil, http.ErrUseLastResponse
	}
	return tr.base.RoundTrip(r)
}

func TestLanguageDefaultPipGlobalInExplicitIsolatedVenv(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	if err := exec.Command(python, "-B", "-c", "import pip").Run(); err != nil {
		t.Skip("pip unavailable")
	}
	languageNativeScopeEnv(t, "PIP_")
	for _, key := range []string{"PYTHONPATH", "PYTHONHOME", "PYTHONNOUSERSITE"} {
		t.Setenv(key, "")
	}
	t.Setenv("PIP_CONFIG_FILE", os.DevNull)
	t.Setenv("PIP_CACHE_DIR", filepath.Join(t.TempDir(), "pip cache"))
	venv := filepath.Join(t.TempDir(), "explicit venv")
	// Borrow the base interpreter's pip module without installing pip or anything
	// else there. Pip still selects this venv's own installation scheme.
	if got, err := exec.Command(python, "-B", "-m", "venv", "--without-pip", "--system-site-packages", venv).CombinedOutput(); err != nil {
		t.Fatalf("create isolated venv: %v %s", err, got)
	}
	selected := filepath.Join(venv, "bin", "python3")
	t.Setenv("OHECO_PYTHON", selected)
	rootName, depName := "oo-default-global-root", "oo-default-global-dep"
	rootFilename, depFilename := "oo_default_global_root-1.0.0-py3-none-any.whl", "oo_default_global_dep-1.0.0-py3-none-any.whl"
	rootBytes, depBytes := languageWheel(t, "oo_default_global_root", depName+"==1.0.0"), languageWheel(t, "oo_default_global_dep", "")
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
	var out bytes.Buffer
	m, err := New(filepath.Join(t.TempDir(), "oo absent"), "https://index.invalid/index.json", &out)
	if err != nil {
		t.Fatal(err)
	}
	m.Client = &http.Client{Transport: languageNativeArtifactOnly{t: t, host: strings.TrimPrefix(server.URL, "http://"), base: http.DefaultTransport}}
	idx := languageTestIndex()
	for _, tc := range []struct {
		name, filename string
		data           []byte
		deps           []string
	}{
		{rootName, rootFilename, rootBytes, []string{depName + "==1.0.0"}}, {depName, depFilename, depBytes, nil},
	} {
		a := artifact(tc.data, server.URL+"/"+tc.filename, nil)
		wheel := catalog.PipArtifact{File: catalog.File{URL: a.URL, SHA256: a.SHA256, Size: a.Size, Filename: tc.filename}, RequiresPython: ">=3.8", RequiresDist: tc.deps}
		idx.Packages = append(idx.Packages, catalog.Package{SchemaVersion: 3, Name: tc.name, PackageManager: "pip", PackageName: tc.name, Description: "global scope fixture", Upstream: "https://example.com/fixture", Repository: "https://github.com/oheco/fixture", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{m.Platform: "1.0.0"}, Versions: []catalog.Version{{Version: "1.0.0", PipArtifacts: []catalog.PipArtifact{wheel}}}})
	}
	if err := m.InstallLanguage(context.Background(), idx, "pip", []string{rootName + "==1.0.0"}, LanguageOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	cmd := exec.Command(selected, "-B", "-c", "import sys,oo_default_global_root,oo_default_global_dep; print(oo_default_global_root.value + oo_default_global_dep.value); print(oo_default_global_root.__file__)")
	got, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(got), "84\n") || !strings.Contains(string(got), venv+string(os.PathSeparator)) {
		t.Fatalf("packages not installed into explicit venv global site: %v %s", err, got)
	}
	if err := m.RemoveLanguage(context.Background(), "pip", []string{rootName}, LanguageOptions{Yes: true}); err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	cmd = exec.Command(selected, "-B", "-c", "import importlib.util,oo_default_global_dep; assert importlib.util.find_spec('oo_default_global_root') is None; print(oo_default_global_dep.value)")
	if got, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(got)) != "42" {
		t.Fatalf("root removal/dependency retention: %v %s", err, got)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "state")); !os.IsNotExist(err) {
		t.Fatalf("external install wrote receipts: %v", err)
	}
}
