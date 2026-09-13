package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

func TestInstalledSearchAndListTables(t *testing.T) {
	f := setup(t)
	f.idx.Packages[0].Maintainers = []catalog.Maintainer{{GitHub: "alice", Name: "Alice Example"}, {GitHub: "bob"}}
	if err := writeJSON(filepath.Join(f.m.Root, "index", "index.json"), f.idx); err != nil {
		t.Fatal(err)
	}
	p := InstalledPackage{Active: "1.0.0", Versions: map[string]Receipt{}}
	a := f.idx.Packages[0].Versions[0].Artifacts[f.m.Platform]
	for _, version := range []string{"1.0.0", "1.9.0", "1.10.0"} {
		p.Versions[version] = Receipt{Platform: f.m.Platform, Artifact: a}
	}
	s := emptyState()
	s.Packages["demo"] = p
	if err := writeJSON(filepath.Join(f.m.Root, "state", "installed.json"), s); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.Search(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	start, end := strings.Index(lines[0], "INSTALLED"), strings.Index(lines[0], "SIZE")
	if got := strings.TrimSpace(lines[1][start:end]); got != "1.10.0 (3)" {
		t.Fatalf("installed summary = %q, want newest installed version and count", got)
	}
	start, end = strings.Index(lines[0], "MAINTAINERS"), strings.Index(lines[0], "DESCRIPTION")
	searchMaintainers := strings.TrimSpace(lines[1][start:end])
	out.Reset()
	if err := f.m.List(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Count(got, "\n") != 3 || !strings.Contains(got, "MANAGER") || !strings.Contains(got, "oheco") || !strings.Contains(got, "1.0.0*, 1.9.0, 1.10.0") || !strings.Contains(got, "Alice Example, @bob") {
		t.Fatalf("incorrect grouped list: %q", got)
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	listMaintainers := strings.TrimSpace(lines[1][strings.Index(lines[0], "MAINTAINERS"):])
	if searchMaintainers != listMaintainers {
		t.Fatalf("maintainer display differs: search=%q, list=%q", searchMaintainers, listMaintainers)
	}
	if err := os.Remove(filepath.Join(f.m.Root, "index", "index.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := f.m.List(); err != nil || !strings.Contains(out.String(), "1.0.0*, 1.9.0, 1.10.0") || !strings.HasSuffix(strings.Split(out.String(), "\n")[1], "-") || !strings.Contains(out.String(), "npm/pip") {
		t.Fatalf("list must work without an index: %q, %v", out.String(), err)
	}
}

func TestExternalQueryShowsOwnershipNotInstalledState(t *testing.T) {
	f := setup(t)
	f.idx.SchemaVersion = catalog.SchemaVersion
	base := f.idx.Packages[0]
	a := base.Versions[0].Artifacts[f.m.Platform]
	for _, backend := range []string{"npm", "pip"} {
		p := base
		p.SchemaVersion = 3
		p.Name = "fixture-" + backend
		p.PackageManager = backend
		p.PackageName = "fixture-" + backend
		p.Latest = map[string]string{f.m.Platform: "1.0.0"}
		v := catalog.Version{Version: "1.0.0"}
		file := catalog.File{URL: a.URL, SHA256: a.SHA256, Size: a.Size}
		if backend == "npm" {
			file.Filename = "fixture-npm-1.0.0.tgz"
			v.NpmArtifacts = &catalog.NpmArtifact{File: file, PackageJSON: json.RawMessage(`{"name":"fixture-npm","version":"1.0.0"}`)}
		} else {
			file.Filename = "fixture_pip-1.0.0-py3-none-any.whl"
			v.PipArtifacts = []catalog.PipArtifact{{File: file}}
		}
		p.Versions = []catalog.Version{v}
		f.idx.Packages = append(f.idx.Packages, p)
	}
	if err := writeJSON(filepath.Join(f.m.Root, "index", "index.json"), f.idx); err != nil {
		t.Fatal(err)
	}
	requests := f.requests
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "MANAGER") || !strings.Contains(out.String(), "<由 npm 管理>") || !strings.Contains(out.String(), "<由 pip 管理>") {
		t.Fatalf("missing ownership columns: %s", out.String())
	}
	if f.requests != requests {
		t.Fatal("query fetched remote language state")
	}
	for _, backend := range []string{"npm", "pip"} {
		out.Reset()
		if err := f.m.Info("fixture-" + backend); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Package manager: "+backend) || !strings.Contains(out.String(), "not an installed-state assertion") {
			t.Fatalf("ambiguous info: %s", out.String())
		}
	}
	out.Reset()
	if err := f.m.List(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "fixture-npm") || strings.Contains(out.String(), "fixture-pip") {
		t.Fatal("uninstalled catalog packages listed as installed")
	}
	out.Reset()
	if err := f.m.Info("demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Package manager: oheco") {
		t.Fatal("native manager missing")
	}
}

func TestInstalledVersionOrdering(t *testing.T) {
	p := InstalledPackage{Versions: map[string]Receipt{}}
	for _, v := range []string{"1.10.0", "1.9.0-ohos.10", "1.9.0", "1.9.0-rc.10", "1.9.0-ohos.2", "1.9.0-rc.2"} {
		p.Versions[v] = Receipt{Platform: "ohos-arm64"}
	}
	p.Versions["99"] = Receipt{Platform: "other-arm64"}
	want := []string{"1.9.0-rc.2", "1.9.0-rc.10", "1.9.0", "1.9.0-ohos.2", "1.9.0-ohos.10", "1.10.0"}
	if got := installedVersions(p, "ohos-arm64"); !reflect.DeepEqual(got, want) {
		t.Fatalf("installed versions: got %v, want %v", got, want)
	}
}
