package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func projectPackage() Package {
	a := Artifact{URL: "https://example.com/app.zip", SHA256: strings.Repeat("a", 64), Size: 123, Format: "zip", Binaries: map[string]string{}}
	p := testPackage(4, a)
	p.Versions[0].Artifacts = nil
	p.Versions[0].Projects = map[string]Project{"editor": {URL: a.URL, SHA256: a.SHA256, Size: a.Size, Format: a.Format, StripComponents: 1}}
	return p
}

func TestProjectDescriptorsAndLegacyIndexes(t *testing.T) {
	p := projectPackage()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	idx := Index{SchemaVersion: 4, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []Package{p}}
	for schema := 1; schema <= 3; schema++ {
		old := idx.Compatible(schema)
		if err := old.Validate(); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(old)
		if len(old.Packages) != 0 || strings.Contains(string(data), "projects") {
			t.Fatal("projects leaked into an old index")
		}
	}
	p.Versions[0].Artifacts = map[string]Artifact{"ohos-arm64": p.Versions[0].Projects["editor"].Archive()}
	if err := p.Validate(); err != nil {
		t.Fatal("mixed artifacts/projects:", err)
	}
	for _, change := range []func(*Package){
		func(p *Package) { p.SchemaVersion = 3 },
		func(p *Package) { p.Versions[0].Projects["../bad"] = p.Versions[0].Projects["editor"] },
		func(p *Package) {
			a := p.Versions[0].Projects["editor"]
			a.SHA256 = "bad"
			p.Versions[0].Projects["editor"] = a
		},
		func(p *Package) {
			a := p.Versions[0].Projects["editor"]
			a.StripComponents = 9
			p.Versions[0].Projects["editor"] = a
		},
		func(p *Package) { p.Versions[0].Projects = map[string]Project{} },
		func(p *Package) { p.Name = "oheco" },
	} {
		bad := projectPackage()
		change(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted invalid descriptor: %+v", bad)
		}
	}
}

func TestResolveNamedProjectsAndVersions(t *testing.T) {
	p := projectPackage()
	for _, platform := range []string{"ohos-arm64", "linux-amd64"} {
		version, name, _, err := p.ResolveProject("", "", platform)
		if err != nil || version != "26-Beta" || name != "editor" {
			t.Fatalf("resolve on %s: %s %s %v", platform, version, name, err)
		}
	}
	p.Versions[0].Projects["game"] = p.Versions[0].Projects["editor"]
	if _, _, _, err := p.ResolveProject("", "", "ohos-arm64"); err == nil || !strings.Contains(err.Error(), "editor, game") {
		t.Fatal("missing project choice:", err)
	}
	if _, _, _, err := p.ResolveProject("26-Beta", "game", "linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.ResolveProject("26-Beta", "absent", "ohos-arm64"); err == nil {
		t.Fatal("missing name accepted")
	}
	if _, _, _, err := p.ResolveProject("missing", "game", "ohos-arm64"); err == nil {
		t.Fatal("missing version accepted")
	}
	p.Latest["ohos-x64"] = "different"
	if _, _, _, err := p.ResolveProject("", "game", "linux-amd64"); err == nil {
		t.Fatal("ambiguous cross-platform latest accepted")
	}
	if _, _, _, err := p.ResolveProject("26-Beta", "game", "linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Resolve("26-Beta", "ohos-arm64"); err == nil || !strings.Contains(err.Error(), "oo export") {
		t.Fatal("missing export hint:", err)
	}
}
