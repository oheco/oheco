package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

func TestPublishedIndexesKeepV1UpgradePath(t *testing.T) {
	a := catalog.Artifact{URL: "https://example.com/oo.tar.gz", SHA256: strings.Repeat("a", 64), Size: 1, Format: "tar.gz", Binaries: map[string]string{"oo": "bin/oo"}}
	p := catalog.Package{SchemaVersion: 1, Name: "oheco", Description: "package manager", Upstream: "https://github.com/oheco/oheco", Repository: "https://github.com/oheco/oheco", Maintainers: []catalog.Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{"ohos-arm64": "0.2.0"}, Versions: []catalog.Version{{Version: "0.2.0", Artifacts: map[string]catalog.Artifact{"ohos-arm64": a}}}}
	data := p
	data.SchemaVersion = 2
	data.Name = "sdk-data"
	a.Format = "zip"
	a.Binaries = map[string]string{}
	data.Versions = []catalog.Version{{Version: "26-Beta", Artifacts: map[string]catalog.Artifact{"ohos-arm64": a}}}
	data.Latest = map[string]string{"ohos-arm64": "26-Beta"}
	idx := catalog.Index{SchemaVersion: 2, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []catalog.Package{p, data}}
	dir := t.TempDir()
	if err := writeIndexes(dir, idx); err != nil {
		t.Fatal(err)
	}
	for version, count := range map[string]int{"v1": 1, "v2": 2} {
		b, err := os.ReadFile(filepath.Join(dir, "index", version, "index.json"))
		if err != nil {
			t.Fatal(err)
		}
		var result catalog.Index
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		if len(result.Packages) != count {
			t.Fatalf("%s: got %d packages", version, len(result.Packages))
		}
		oo, err := result.Find("oheco")
		if err != nil {
			t.Fatal(err)
		}
		if oo.Latest["ohos-arm64"] != "0.2.0" {
			t.Fatal("old client cannot find new oo")
		}
	}
}
