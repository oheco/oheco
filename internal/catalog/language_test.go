package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLanguageArtifactExclusivityAndCompatibility(t *testing.T) {
	native := testPackage(1, Artifact{URL: "https://example.com/tool.tgz", SHA256: strings.Repeat("a", 64), Size: 1, Format: "tar.gz", Binaries: map[string]string{"tool": "bin/tool"}})
	data := testPackage(2, Artifact{URL: "https://example.com/data.zip", SHA256: strings.Repeat("b", 64), Size: 1, Format: "zip", Binaries: map[string]string{}})
	data.Name = "data"
	npm := native
	npm.SchemaVersion = 3
	npm.Name = "node-library"
	npm.PackageManager = "npm"
	npm.PackageName = "@oheco/library"
	npm.Latest = map[string]string{"ohos-arm64": "1.0.0"}
	npm.Versions = []Version{{Version: "1.0.0", NpmArtifacts: &NpmArtifact{File: File{URL: "https://example.com/library.tgz", SHA256: strings.Repeat("c", 64), Size: 1, Filename: "library.tgz"}, PackageJSON: json.RawMessage(`{"name":"@oheco/library","version":"1.0.0","dependencies":{"other":"^2.0.0"}}`)}}}
	idx := Index{SchemaVersion: 3, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []Package{native, data, npm}}
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	for schema, want := range map[int]int{1: 1, 2: 2, 3: 3} {
		old := idx.Compatible(schema)
		if err := old.Validate(); err != nil {
			t.Fatal(err)
		}
		if len(old.Packages) != want {
			t.Fatalf("v%d has %d packages", schema, len(old.Packages))
		}
		b, _ := json.Marshal(old)
		if schema < 3 && strings.Contains(string(b), "package_manager") {
			t.Fatal("language fields leaked into old index")
		}
	}
	npm.Versions[0].Artifacts = map[string]Artifact{}
	if npm.Validate() == nil {
		t.Fatal("accepted mixed artifact fields")
	}
	npm.Versions[0].Artifacts = nil
	npm.SchemaVersion = 2
	if npm.Validate() == nil {
		t.Fatal("accepted language package in v2")
	}
	npm.SchemaVersion = 3
	npm.PackageName = "@oheco/wrong"
	if npm.Validate() == nil {
		t.Fatal("accepted mismatched tarball manifest name")
	}
}
