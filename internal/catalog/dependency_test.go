package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func dependencyPackage(name string, versions ...string) Package {
	a := Artifact{URL: "https://example.com/native.tar.gz", SHA256: strings.Repeat("a", 64), Size: 1, Format: "tar.gz", Binaries: map[string]string{}}
	p := testPackage(5, a)
	p.Name = name
	p.Versions = nil
	for _, version := range versions {
		p.Versions = append(p.Versions, Version{Version: version, Artifacts: map[string]Artifact{"ohos-arm64": a}})
	}
	p.Latest = map[string]string{"ohos-arm64": versions[len(versions)-1]}
	return p
}

func dependencyIndex() Index {
	app := dependencyPackage("app", "1.0")
	app.Versions[0].Dependencies = []Dependency{{Name: "runtime", Constraint: ">=2 <3"}}
	runtime := dependencyPackage("runtime", "1.0", "2.0", "2.10-ohos.2")
	return Index{SchemaVersion: 5, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []Package{app, runtime}}
}

func TestDependencyAPI(t *testing.T) {
	d := Dependency{Name: "runtime", Constraint: ">=2 <3"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !d.AppliesTo("ohos-arm64") || !d.AppliesTo("linux-amd64") || d.AppliesTo("bad") {
		t.Fatal("unrestricted platform filter")
	}
	d.Platforms = []string{"ohos-arm64"}
	if !d.AppliesTo("ohos-arm64") || d.AppliesTo("linux-amd64") {
		t.Fatal("restricted platform filter")
	}
	v := Version{Version: "2.10-ohos.2", UpstreamVersion: "1.9"}
	for _, basis := range []string{"", "package"} {
		d.VersionBasis = basis
		if ok, err := d.Matches(v); !ok || err != nil {
			t.Fatalf("package basis %q: %v %v", basis, ok, err)
		}
	}
	d.VersionBasis = "upstream"
	if ok, err := d.Matches(v); ok || err != nil {
		t.Fatalf("upstream basis: %v %v", ok, err)
	}
	v.UpstreamVersion = "2.10"
	if ok, err := d.Matches(v); !ok || err != nil {
		t.Fatalf("upstream match: %v %v", ok, err)
	}
	v.UpstreamVersion = ""
	for _, constraint := range []string{">=2", "*"} {
		d.Constraint = constraint
		if _, err := d.Matches(v); err == nil || !strings.Contains(err.Error(), "no upstream_version") {
			t.Fatalf("missing upstream must not fallback: %v", err)
		}
	}
}

func TestDependencyRejectsInvalidFields(t *testing.T) {
	for _, d := range []Dependency{
		{Name: "", Constraint: "*"},
		{Name: "../runtime", Constraint: "*"},
		{Name: "@npm/package", Constraint: "*"},
		{Name: "runtime", Constraint: ""},
		{Name: "runtime", Constraint: "^2"},
		{Name: "runtime", Constraint: ">nightly"},
		{Name: "runtime", Constraint: "*", VersionBasis: "guess"},
		{Name: "runtime", Constraint: "*", Platforms: []string{"ohos_arm64"}},
		{Name: "runtime", Constraint: "*", Platforms: []string{"ohos-arm64", "ohos-arm64"}},
	} {
		if err := d.Validate(); err == nil {
			t.Fatalf("accepted dependency %+v", d)
		}
		if _, err := d.Matches(Version{Version: "2"}); err == nil {
			t.Fatalf("Matches accepted dependency %+v", d)
		}
	}
}

func TestDependencyJSONAndSchemaV5(t *testing.T) {
	if SchemaVersion != 5 || DefaultURL != "https://oheco.org/index/v5/index.json" {
		t.Fatal("schema and default URL must point to v5")
	}
	d := Dependency{Name: "runtime", Constraint: "*"}
	data, err := json.Marshal(d)
	if err != nil || string(data) != `{"name":"runtime","constraint":"*"}` {
		t.Fatalf("optional dependency fields: %s %v", data, err)
	}
	idx := dependencyIndex()
	idx.Packages[0].Versions[0].Dependencies[0].VersionBasis = "upstream"
	idx.Packages[0].Versions[0].Dependencies[0].Platforms = []string{"ohos-arm64"}
	idx.Packages[1].Versions[2].UpstreamVersion = "2.10"
	data, err = json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Index
	if err := Decode(strings.NewReader(string(data)), &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	got := decoded.Packages[0].Versions[0].Dependencies[0]
	if got.Name != "runtime" || got.Constraint != ">=2 <3" || got.VersionBasis != "upstream" || len(got.Platforms) != 1 {
		t.Fatalf("dependency roundtrip: %+v", got)
	}
	for _, invalid := range []string{
		`{"version":"1","dependencies":[{"name":"runtime","constraint":"*","typo":true}]}`,
		`{"version":"1","dependencies":{"runtime":"*"}}`,
		`{"version":"1","dependencies":[{"name":"runtime","constraint":1}]}`,
		`{"version":"1","dependencies":null}`,
		`{"version":"1","Dependencies":null}`,
		`{"version":"1","typo":true}`,
	} {
		var version Version
		if Decode(strings.NewReader(invalid), &version) == nil {
			t.Fatalf("accepted malformed dependency JSON: %s", invalid)
		}
	}
}

func TestPackageDependencyValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*Package)
		want string
	}{
		{"old schema", func(p *Package) { p.SchemaVersion = 4 }, "schema_version=5"},
		{"empty field old schema", func(p *Package) { p.SchemaVersion = 4; p.Versions[0].Dependencies = []Dependency{} }, "schema_version=5"},
		{"self", func(p *Package) { p.Versions[0].Dependencies[0].Name = p.Name }, "itself"},
		{"duplicate", func(p *Package) {
			p.Versions[0].Dependencies = append(p.Versions[0].Dependencies, p.Versions[0].Dependencies[0])
		}, "duplicate dependency"},
		{"unknown source platform", func(p *Package) { p.Versions[0].Dependencies[0].Platforms = []string{"linux-amd64"} }, "no source artifact"},
		{"invalid field", func(p *Package) { p.Versions[0].Dependencies[0].VersionBasis = "upstream_version" }, "version_basis"},
		{"project only", func(p *Package) {
			p.Versions[0].Artifacts = nil
			p.Versions[0].Projects = projectPackage().Versions[0].Projects
		}, "installable native"},
		{"npm", func(p *Package) { p.PackageManager = "npm"; p.PackageName = "app" }, "installable native"},
		{"pip", func(p *Package) { p.PackageManager = "pip"; p.PackageName = "app" }, "installable native"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := dependencyIndex().Packages[0]
			tt.edit(&p)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want %q", err, tt.want)
			}
		})
	}
	p := dependencyIndex().Packages[0]
	p.Versions[0].Dependencies = []Dependency{}
	if err := p.Validate(); err != nil {
		t.Fatal("v5 may explicitly have no dependencies:", err)
	}
}

func TestIndexDependencyValidation(t *testing.T) {
	if err := dependencyIndex().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		edit func(*Index)
		want string
	}{
		{"old index", func(i *Index) { i.SchemaVersion = 4 }, "requires index schema 5"},
		{"missing target", func(i *Index) { i.Packages = i.Packages[:1] }, "not in the catalog"},
		{"no matching version", func(i *Index) { i.Packages[0].Versions[0].Dependencies[0].Constraint = ">=3" }, "no matching native artifact"},
		{"no target platform", func(i *Index) {
			for j := range i.Packages[1].Versions {
				v := &i.Packages[1].Versions[j]
				v.Artifacts = map[string]Artifact{"linux-amd64": v.Artifacts["ohos-arm64"]}
			}
			i.Packages[1].Latest = map[string]string{"linux-amd64": "2.0"}
		}, "no matching native artifact for ohos-arm64"},
		{"project only target", func(i *Index) {
			p := projectPackage()
			p.Name = "runtime"
			i.Packages[1] = p
			i.Packages[0].Versions[0].Dependencies[0].Constraint = "*"
		}, "no matching native artifact"},
		{"external target", func(i *Index) {
			p := &i.Packages[1]
			p.PackageManager, p.PackageName = "npm", "runtime"
			p.Latest = map[string]string{"ohos-arm64": "2.0.0"}
			p.Versions = []Version{{Version: "2.0.0", NpmArtifacts: &NpmArtifact{File: File{URL: "https://example.com/runtime.tgz", SHA256: strings.Repeat("a", 64), Size: 1, Filename: "runtime.tgz"}, PackageJSON: json.RawMessage(`{"name":"runtime","version":"2.0.0"}`)}}}
		}, "not a native package"},
		{"missing upstream", func(i *Index) { i.Packages[0].Versions[0].Dependencies[0].VersionBasis = "upstream" }, "no upstream_version"},
		{"opaque range candidates", func(i *Index) { i.Packages[1] = dependencyPackage("runtime", "nightly") }, "opaque version"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			i := dependencyIndex()
			tt.edit(&i)
			if err := i.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestIndexDependencyChecksEachPlatform(t *testing.T) {
	idx := dependencyIndex()
	v := &idx.Packages[0].Versions[0]
	v.Artifacts["linux-amd64"] = v.Artifacts["ohos-arm64"]
	if err := idx.Validate(); err == nil || !strings.Contains(err.Error(), "linux-amd64") {
		t.Fatal("unrestricted dependency ignored second platform:", err)
	}
	v.Dependencies[0].Platforms = []string{"ohos-arm64"}
	if err := idx.Validate(); err != nil {
		t.Fatal("restricted dependency should not require Linux target:", err)
	}
	v.Dependencies[0].Platforms = nil
	target := &idx.Packages[1].Versions[0]
	target.Artifacts["linux-amd64"] = target.Artifacts["ohos-arm64"]
	if err := idx.Validate(); err == nil {
		t.Fatal("accepted wrong-version target on second platform")
	}
	target = &idx.Packages[1].Versions[1]
	target.Artifacts["linux-amd64"] = target.Artifacts["ohos-arm64"]
	if err := idx.Validate(); err != nil {
		t.Fatal("each platform can match a different target version:", err)
	}
}

func TestIndexDependencySkipsUnusableCandidates(t *testing.T) {
	idx := dependencyIndex()
	idx.Packages[1] = dependencyPackage("runtime", "nightly", "2.0")
	if err := idx.Validate(); err != nil {
		t.Fatal("opaque historical version blocked ordered candidate:", err)
	}
	idx.Packages[0].Versions[0].Dependencies[0].VersionBasis = "upstream"
	idx.Packages[1].Versions[1].UpstreamVersion = "2.0"
	if err := idx.Validate(); err != nil {
		t.Fatal("historical version without upstream blocked explicit candidate:", err)
	}
}

func TestIndexDoesNotMergeAlternativeVersionCycles(t *testing.T) {
	a := dependencyPackage("a", "1", "2")
	b := dependencyPackage("b", "1", "2")
	a.Versions[1].Dependencies = []Dependency{{Name: "b", Constraint: "=1"}}
	b.Versions[1].Dependencies = []Dependency{{Name: "a", Constraint: "=1"}}
	idx := Index{SchemaVersion: 5, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []Package{a, b}}
	if err := idx.Validate(); err != nil {
		t.Fatal("union graph cycle is not a selection cycle:", err)
	}
	// Even a true selection cycle is the install resolver's responsibility.
	idx.Packages[0].Versions[1].Dependencies[0].Constraint = "=2"
	idx.Packages[1].Versions[1].Dependencies[0].Constraint = "=2"
	if err := idx.Validate(); err != nil {
		t.Fatal("catalog validation must not implement install selection:", err)
	}
}

func TestDependenciesStayOutOfOldCompatibilityIndexes(t *testing.T) {
	idx := dependencyIndex()
	legacy := dependencyPackage("legacy", "1")
	legacy.SchemaVersion = 1
	a := legacy.Versions[0].Artifacts["ohos-arm64"]
	a.Binaries = map[string]string{"old": "bin/old"}
	legacy.Versions[0].Artifacts["ohos-arm64"] = a
	bootstrap := dependencyPackage("oheco", "0.6.0")
	bootstrap.SchemaVersion = 1
	a.Binaries = map[string]string{"oo": "bin/oo"}
	bootstrap.Versions[0].Artifacts["ohos-arm64"] = a
	idx.Packages = append(idx.Packages, legacy, bootstrap)
	if err := idx.Validate(); err != nil {
		t.Fatal(err)
	}
	for schema := 1; schema <= 4; schema++ {
		old := idx.Compatible(schema)
		if len(old.Packages) != 2 {
			t.Fatalf("v%d must drop entire v5 packages, not just dependency metadata: %+v", schema, old)
		}
		if err := old.Validate(); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(old)
		if err != nil || strings.Contains(string(data), "dependencies") || strings.Contains(string(data), `"app"`) {
			t.Fatalf("v%d leaked dependency package: %s %v", schema, data, err)
		}
		var readOld Index
		if err := Decode(strings.NewReader(string(data)), &readOld); err != nil {
			t.Fatal(err)
		}
		if err := readOld.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	bootstrap.SchemaVersion = 5
	if bootstrap.Validate() == nil {
		t.Fatal("oheco bootstrap accepted schema5")
	}
}

func TestLanguageDependenciesRemainBackendMetadata(t *testing.T) {
	for _, manager := range []string{"npm", "pip"} {
		p := dependencyPackage("language", "1.0.0")
		p.PackageManager, p.PackageName = manager, "language"
		f := File{URL: "https://example.com/language.tgz", SHA256: strings.Repeat("a", 64), Size: 1, Filename: "language.tgz"}
		v := Version{Version: "1.0.0"}
		if manager == "npm" {
			v.NpmArtifacts = &NpmArtifact{File: f, PackageJSON: json.RawMessage(`{"name":"language","version":"1.0.0","dependencies":{"backend-only":"^2"}}`)}
		} else {
			f.Filename = "language-1.0.0-py3-none-any.whl"
			v.PipArtifacts = []PipArtifact{{File: f, RequiresDist: []string{"backend-only>=2"}}}
		}
		p.Versions = []Version{v}
		idx := Index{SchemaVersion: 5, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []Package{p}}
		if err := idx.Validate(); err != nil {
			t.Fatalf("%s backend dependency incorrectly requires native target: %v", manager, err)
		}
		if p.Versions[0].Dependencies != nil {
			t.Fatal("language dependencies were copied into native metadata")
		}
		p.Versions[0].Dependencies = []Dependency{}
		if p.Validate() == nil {
			t.Fatalf("%s accepted even an empty native dependencies field", manager)
		}
	}
}
