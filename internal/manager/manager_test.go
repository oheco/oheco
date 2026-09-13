package manager

import (
	"archive/tar"
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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

type entry struct {
	name, content, link string
	kind                byte
	mode                int64
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if e.kind == 0 {
			e.kind = tar.TypeReg
		}
		if e.mode == 0 {
			e.mode = 0755
		}
		h := &tar.Header{Name: e.name, Typeflag: e.kind, Linkname: e.link, Mode: e.mode}
		if e.kind == tar.TypeReg {
			h.Size = int64(len(e.content))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func artifact(data []byte, address string, bins map[string]string) catalog.Artifact {
	digest := sha256.Sum256(data)
	return catalog.Artifact{URL: address, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data)), Format: "tar.gz", Binaries: bins}
}

type fixture struct {
	m         *Manager
	idx       catalog.Index
	files     map[string][]byte
	server    *httptest.Server
	requests  int
	indexData []byte
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{files: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		if r.URL.Path == "/index.json" {
			if f.indexData != nil {
				w.Write(f.indexData)
			} else {
				json.NewEncoder(w).Encode(f.idx)
			}
			return
		}
		if data, ok := f.files[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.server.Close)
	var err error
	f.m, err = New(t.TempDir(), f.server.URL+"/index.json", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	f.idx = catalog.Index{SchemaVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	f.add(t, "demo", "1.0.0", map[string]string{"demo": "bin/demo", "old": "bin/old"})
	f.add(t, "demo", "2.0.0", map[string]string{"demo": "bin/demo", "new": "bin/new"})
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) add(t *testing.T, name, version string, bins map[string]string) {
	var entries []entry
	for _, p := range bins {
		entries = append(entries, entry{name: p, content: "#!/bin/sh\necho " + version + "\n"})
	}
	data := archive(t, entries...)
	address := "/" + name + "-" + version + ".tar.gz"
	f.files[address] = data
	v := catalog.Version{Version: version, Artifacts: map[string]catalog.Artifact{f.m.Platform: artifact(data, f.server.URL+address, bins)}}
	for i, p := range f.idx.Packages {
		if p.Name == name {
			f.idx.Packages[i].Versions = append(p.Versions, v)
			f.idx.Packages[i].Latest[f.m.Platform] = version
			return
		}
	}
	f.idx.Packages = append(f.idx.Packages, catalog.Package{SchemaVersion: 1, Name: name, Description: "test package", Upstream: "https://example.com/project", Repository: "https://github.com/oheco/" + name, Maintainers: []catalog.Maintainer{{GitHub: "oheco"}}, License: "MIT", Latest: map[string]string{f.m.Platform: version}, Versions: []catalog.Version{v}})
}
func linkIs(t *testing.T, root, name, target string) {
	t.Helper()
	got, err := os.Readlink(filepath.Join(root, "bin", name))
	if err != nil || got != target {
		t.Fatalf("%s: got %q, %v; want %q", name, got, err, target)
	}
}
func missing(t *testing.T, filename string) {
	t.Helper()
	if _, err := os.Lstat(filename); !os.IsNotExist(err) {
		t.Fatalf("expected %s absent, got %v", filename, err)
	}
}

func TestMultiVersionLifecycleOffline(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.m.Install(ctx, "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(ctx, "demo@2.0.0", true); err != nil {
		t.Fatal(err)
	}
	linkIs(t, f.m.Root, "demo", "demo@1.0.0")
	missing(t, filepath.Join(f.m.Root, "bin", "new"))
	requests := f.requests
	if err := f.m.Install(ctx, "demo@2.0.0", false); err != nil {
		t.Fatal(err)
	}
	if f.requests != requests {
		t.Fatal("idempotent install downloaded again")
	}
	linkIs(t, f.m.Root, "demo", "demo@2.0.0")
	missing(t, filepath.Join(f.m.Root, "bin", "old"))
	linkIs(t, f.m.Root, "old@1.0.0", "../packages/demo/1.0.0/bin/old")
	f.server.Close()
	os.Remove(filepath.Join(f.m.Root, "index", "index.json"))
	if err := f.m.Switch("demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	linkIs(t, f.m.Root, "old", "old@1.0.0")
	missing(t, filepath.Join(f.m.Root, "bin", "new"))
	if err := f.m.Remove("demo", false); err != nil {
		t.Fatal(err)
	}
	missing(t, filepath.Join(f.m.Root, "bin", "demo"))
	missing(t, filepath.Join(f.m.Root, "packages", "demo", "1.0.0"))
	if err := f.m.Switch("demo", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Remove("demo", true); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.LoadState()
	if err != nil || len(s.Packages) != 0 {
		t.Fatalf("state: %#v %v", s, err)
	}
	missing(t, filepath.Join(f.m.Root, "bin", "demo@2.0.0"))
}

func TestBadIndexPreservesCache(t *testing.T) {
	f := setup(t)
	filename := filepath.Join(f.m.Root, "index", "index.json")
	before, _ := os.ReadFile(filename)
	f.indexData = []byte(`{"schema_version":99,"generated_at":"2026-09-11T00:00:00Z","packages":[]}`)
	if err := f.m.Update(context.Background()); err == nil {
		t.Fatal("accepted incompatible index")
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("overwrote usable index")
	}
}
func TestBadDownloadPreservesActiveVersion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.m.Install(ctx, "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	data := f.files["/demo-2.0.0.tar.gz"]
	data[len(data)/2] ^= 1
	if err := f.m.Install(ctx, "demo@2.0.0", false); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected checksum failure: %v", err)
	}
	linkIs(t, f.m.Root, "demo", "demo@1.0.0")
	missing(t, filepath.Join(f.m.Root, "packages", "demo", "2.0.0"))
}
func TestUnmanagedAndModifiedLinksArePreserved(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	filename := filepath.Join(f.m.Root, "bin", "demo")
	os.WriteFile(filename, []byte("user file"), 0644)
	if err := f.m.Install(ctx, "demo", false); err == nil {
		t.Fatal("overwrote user file")
	}
	data, _ := os.ReadFile(filename)
	if string(data) != "user file" {
		t.Fatal("user file changed")
	}
	os.Remove(filename)
	if err := f.m.Install(ctx, "demo", false); err != nil {
		t.Fatal(err)
	}
	os.Remove(filename)
	os.Symlink("/user/command", filename)
	if err := f.m.Remove("demo", true); err == nil {
		t.Fatal("removed modified symlink")
	}
	linkIs(t, f.m.Root, "demo", "/user/command")
}
func TestCrossPackageCollisionRejected(t *testing.T) {
	f := setup(t)
	if err := f.m.Install(context.Background(), "demo", false); err != nil {
		t.Fatal(err)
	}
	f.idx.Packages = nil
	f.add(t, "other", "1", map[string]string{"demo": "bin/demo"})
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(context.Background(), "other", false); err == nil {
		t.Fatal("accepted command collision with installed package missing from index")
	}
	linkIs(t, f.m.Root, "demo", "demo@2.0.0")
}

func TestArchiveBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
	}{
		{"traversal", []entry{{name: "../escape", content: "x"}}},
		{"absolute", []entry{{name: "/escape", content: "x"}}},
		{"symlink escape", []entry{{name: "bin/demo", kind: tar.TypeSymlink, link: "../../escape"}}},
		{"symlink parent", []entry{{name: "bin", kind: tar.TypeSymlink, link: "lib"}, {name: "bin/demo", content: "x"}, {name: "lib/demo", content: "x"}}},
		{"hardlink", []entry{{name: "bin/demo", kind: tar.TypeLink, link: "other"}}},
		{"duplicate", []entry{{name: "bin/demo", content: "a"}, {name: "bin/demo", content: "b"}}},
		{"non executable", []entry{{name: "bin/demo", content: "x", mode: 0644}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := archive(t, tt.entries...)
			a := artifact(data, "https://example.com/a.tar.gz", map[string]string{"demo": "bin/demo"})
			root := t.TempDir()
			file := filepath.Join(root, "a.tar.gz")
			os.WriteFile(file, data, 0644)
			dest := filepath.Join(root, "out")
			os.Mkdir(dest, 0755)
			if err := extract(context.Background(), file, dest, a); err == nil {
				t.Fatal("accepted unsafe archive")
			}
			missing(t, filepath.Join(root, "escape"))
		})
	}
}
func TestArchiveStripAndInternalSymlink(t *testing.T) {
	data := archive(t, entry{name: "pkg/", kind: tar.TypeDir}, entry{name: "pkg/lib/demo", content: "hello"}, entry{name: "pkg/bin/demo", kind: tar.TypeSymlink, link: "../lib/demo"})
	a := artifact(data, "https://example.com/a.tar.gz", map[string]string{"demo": "bin/demo"})
	a.StripComponents = 1
	root := t.TempDir()
	file := filepath.Join(root, "a.tar.gz")
	os.WriteFile(file, data, 0644)
	dest := filepath.Join(root, "out")
	os.Mkdir(dest, 0755)
	if err := extract(context.Background(), file, dest, a); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "bin", "demo"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestRecoveryOfInterruptedSwitch(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if err := f.m.Install(ctx, "demo@1.0.0", false); err != nil {
				t.Fatal(err)
			}
			if err := f.m.Install(ctx, "demo@2.0.0", true); err != nil {
				t.Fatal(err)
			}
			before, _ := f.m.LoadState()
			after := cloneState(before)
			p := after.Packages["demo"]
			p.Active = "2.0.0"
			after.Packages["demo"] = p
			if err := writeJSON(filepath.Join(f.m.Root, "state", "transaction.json"), transaction{Before: before, After: after, Committed: committed}); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(f.m.Root, "bin", "demo"))
			os.Symlink("demo@2.0.0", filepath.Join(f.m.Root, "bin", "demo"))
			os.Remove(filepath.Join(f.m.Root, "bin", "old"))
			// Simulate state already written but final commit marker not yet persisted.
			writeJSON(filepath.Join(f.m.Root, "state", "installed.json"), after)
			if err := f.m.Recover(); err != nil {
				t.Fatal(err)
			}
			want := "1.0.0"
			if committed {
				want = "2.0.0"
			}
			linkIs(t, f.m.Root, "demo", "demo@"+want)
			s, _ := f.m.LoadState()
			if s.Packages["demo"].Active != want {
				t.Fatal("state and links disagree")
			}
			missing(t, filepath.Join(f.m.Root, "state", "transaction.json"))
		})
	}
}

func TestRecoveryOfInterruptedInstallAndRemoval(t *testing.T) {
	f := setup(t)
	if err := f.m.Install(context.Background(), "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	installed, _ := f.m.LoadState()
	empty := emptyState()
	move, err := transactionDirectory(filepath.Join("demo", "1.0.0"), filepath.Join(f.m.Root, "packages", "demo", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(filepath.Join(f.m.Root, "state", "transaction.json"), transaction{Before: empty, After: installed, Moves: []transactionMove{move}})
	if err := f.m.Recover(); err != nil {
		t.Fatal(err)
	}
	missing(t, filepath.Join(f.m.Root, "packages", "demo", "1.0.0"))
	if err := f.m.Install(context.Background(), "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	installed, _ = f.m.LoadState()
	writeJSON(filepath.Join(f.m.Root, "state", "transaction.json"), transaction{Before: installed, After: empty, Committed: true})
	if err := f.m.Recover(); err != nil {
		t.Fatal(err)
	}
	missing(t, filepath.Join(f.m.Root, "packages", "demo", "1.0.0"))
	missing(t, filepath.Join(f.m.Root, "bin", "demo"))
}
func TestConcurrentLock(t *testing.T) {
	f := setup(t)
	if err := f.m.withLock(func() error {
		other, err := New(f.m.Root, f.m.IndexURL, io.Discard)
		if err != nil {
			return err
		}
		if err := other.Recover(); err == nil {
			return fmt.Errorf("second writer acquired lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReplacedPackageDirectoryCannotRedirectRemoval(t *testing.T) {
	f := setup(t)
	if err := f.m.Install(context.Background(), "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(f.m.Root, "packages", "demo")
	backup := filepath.Join(f.m.Root, "packages", "saved")
	if err := os.Rename(packageDir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, packageDir); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Remove("demo", true); err == nil {
		t.Fatal("followed user-replaced package directory")
	}
	if _, err := os.Stat(filepath.Join(backup, "1.0.0", "bin", "demo")); err != nil {
		t.Fatal("removed redirected contents:", err)
	}
}
