package manager

import (
	"bytes"
	"context"
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
	if got := out.String(); strings.Count(got, "\n") != 2 || !strings.Contains(got, "1.0.0*, 1.9.0, 1.10.0") || !strings.Contains(got, "Alice Example, @bob") {
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
	if err := f.m.List(); err != nil || !strings.Contains(out.String(), "1.0.0*, 1.9.0, 1.10.0") || !strings.HasSuffix(out.String(), "-\n") {
		t.Fatalf("list must work without an index: %q, %v", out.String(), err)
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
