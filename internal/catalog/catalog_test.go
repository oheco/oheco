package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPackage(schema int, a Artifact) Package {
	return Package{SchemaVersion: schema, Name: "sdk", Description: "SDK", Upstream: "https://example.com/sdk", Repository: "https://github.com/oheco/sdk", Maintainers: []Maintainer{{GitHub: "kdada"}}, License: "MIT", Latest: map[string]string{"ohos-arm64": "26-Beta"}, Versions: []Version{{Version: "26-Beta", Artifacts: map[string]Artifact{"ohos-arm64": a}}}}
}

func TestSchemaV2AndLegacyUpgradeIndex(t *testing.T) {
	a := Artifact{URL: "https://example.com/sdk.tar.gz", SHA256: strings.Repeat("a", 64), Size: 1, Format: "tar.gz", Binaries: map[string]string{"tool": "bin/tool"}}
	legacy := testPackage(1, a)
	sdk := testPackage(2, a)
	sdk.Name = "sdk-native"
	a.Format = "zip"
	a.Launchers = []string{"tool"}
	sdk.Versions[0].Artifacts["ohos-arm64"] = a
	legacy.Name = "oheco"
	oldArtifact := legacy.Versions[0].Artifacts["ohos-arm64"]
	oldArtifact.Binaries = map[string]string{"oo": "bin/oo"}
	legacy.Versions[0].Artifacts["ohos-arm64"] = oldArtifact
	idx := Index{SchemaVersion: 2, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []Package{legacy, sdk}}
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	old := idx.Legacy()
	if err := old.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(old.Packages) != 1 || old.Packages[0].Name != "oheco" {
		t.Fatalf("legacy upgrade index: %+v", old)
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "launchers") || strings.Contains(string(data), "zip") {
		t.Fatal("v2 features leaked into old index")
	}
	idx.SchemaVersion = 1
	if idx.Validate() == nil {
		t.Fatal("v1 index accepted v2 package")
	}
	sdk.SchemaVersion = 1
	if sdk.Validate() == nil {
		t.Fatal("v1 package accepted ZIP/launcher")
	}
	a.Format = "tar.gz"
	a.Launchers = nil
	a.Binaries = map[string]string{}
	if testPackage(1, a).Validate() == nil {
		t.Fatal("v1 accepted data-only package")
	}
	if err := testPackage(2, a).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherReferences(t *testing.T) {
	a := Artifact{URL: "https://example.com/sdk.zip", SHA256: strings.Repeat("a", 64), Size: 1, Format: "zip", Binaries: map[string]string{"ld.lld": "llvm/bin/ld.lld"}}
	for _, names := range [][]string{{"missing"}, {"ld.lld", "ld.lld"}, {"../escape"}} {
		a.Launchers = names
		if a.Validate() == nil {
			t.Fatalf("accepted launchers %v", names)
		}
	}
	a.Launchers = []string{"ld.lld"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	a.Binaries["ld.lld"] = ".oo-launchers/ld.lld"
	if a.Validate() == nil {
		t.Fatal("accepted reserved binary path")
	}
	a.Binaries = nil
	a.Launchers = nil
	if a.Validate() == nil {
		t.Fatal("accepted missing binaries object")
	}
}

func TestURLPolicy(t *testing.T) {
	for _, s := range []string{"https://github.com/oheco/go", "http://127.0.0.1:1234/index.json", "http://[::1]/index"} {
		if err := ValidateURL(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"http://github.com/oheco/go", "file:///etc/passwd", "https://user:password@github.com/file", "http://127.0.0.1.example.com/index"} {
		if ValidateURL(s) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestPaths(t *testing.T) {
	for _, s := range []string{"../bin/go", "/bin/go", "bin/../go", "bin//go", "bin\\go", "."} {
		if SafePath(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"bin/go", "go", "pkg/tool/ohos_arm64/compile"} {
		if !SafePath(s) {
			t.Fatalf("rejected %q", s)
		}
	}
}
func TestDecodeRejectsUnknownAndTrailingData(t *testing.T) {
	for _, s := range []string{`{"schema_version":1,"typo":true}`, `{"schema_version":1} {}`} {
		var idx Index
		if Decode(strings.NewReader(s), &idx) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
