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
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

func TestLanguageInstallUsesNpmAndPreservesUserConfig(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm unavailable")
	}
	dir := t.TempDir()
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
	if err := m.Install(context.Background(), "fixture", false); err != nil {
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
	if err := m.Language(context.Background(), "npm", []string{"uninstall", "oo-language-fixture"}); err != nil {
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

func TestLanguageRejectsRegistryBypasses(t *testing.T) {
	for _, args := range [][]string{{"install", "https://example.com/a.tgz"}, {"install", "foo", "--registry=https://example.com"}, {"install", "foo", "--ignore-scripts=false"}, {"install", "foo@git+https://example.com/foo.git"}, {"publish"}} {
		if validateLanguageArgs("npm", args) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"install", "pkg @ https://example.com/a.whl"}, {"install", "-r", "requirements.txt"}, {"install", "pkg", "--extra-index-url=https://example.com"}} {
		if validateLanguageArgs("pip", args) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("@scope:registry=https://example.com\n"), 0600)
	if checkNpmProject(dir) == nil {
		t.Fatal("accepted scoped external registry")
	}
	os.Remove(filepath.Join(dir, ".npmrc"))
	os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{"packages":{"node_modules/foo":{"resolved":"http://127.0.0.1:9999/foo.tgz"}}}`), 0600)
	if checkNpmProject(dir) == nil {
		t.Fatal("accepted stale temporary registry")
	}
}
