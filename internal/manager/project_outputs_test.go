package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func projectPackageOutputsFixture(t *testing.T, product, modules string) *projectInstaller {
	t.Helper()
	var profile map[string]any
	if err := decodeProjectJSON([]byte(`{modules:`+modules+`}`), &profile); err != nil {
		t.Fatal(err)
	}
	return &projectInstaller{directory: t.TempDir(), metadata: projectMetadata{productName: product, profile: profile}}
}

func projectPackageOutputsModule(t *testing.T, installer *projectInstaller, src, kind, profile string) {
	t.Helper()
	projectTestFile(t, installer.directory, filepath.Join(src, "src/main/module.json5"), []byte(`{module:{type:'`+kind+`'}}`))
	if profile != "" {
		projectTestFile(t, installer.directory, filepath.Join(src, "build-profile.json5"), []byte(profile))
	}
}

func projectPackageOutputsWant(t *testing.T, installer *projectInstaller, relative ...string) {
	t.Helper()
	want := make([]string, len(relative))
	for index, path := range relative {
		want[index] = filepath.Join(installer.directory, filepath.FromSlash(path))
	}
	got, err := installer.packageOutputs()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("packageOutputs() = %v, %v; want %v", got, err, want)
	}
	for _, path := range got {
		if !filepath.IsAbs(path) {
			t.Fatalf("output is not absolute: %s", path)
		}
	}
}

func TestProjectPackageOutputsDefault(t *testing.T) {
	i := projectPackageOutputsFixture(t, "default", `[{name:'entry',srcPath:'./entry'}]`)
	projectPackageOutputsModule(t, i, "entry", "entry", "")
	// Existing unsigned, unrelated and other-product files cannot influence the
	// answer; the exact expected signed output has not been built yet.
	for _, path := range []string{
		"entry/build/default/outputs/default/entry-default-unsigned.hap",
		"entry/build/default/outputs/default/unrelated-signed.hap",
		"entry/build/other/outputs/default/entry-default-signed.hap",
	} {
		projectTestFile(t, i.directory, path, []byte("old output"))
	}
	projectPackageOutputsWant(t, i, "entry/build/default/outputs/default/entry-default-signed.hap")
	if _, err := os.Lstat(filepath.Join(i.directory, "entry/build/default/outputs/default/entry-default-signed.hap")); !os.IsNotExist(err) {
		t.Fatalf("expected output was created: %v", err)
	}
}

func TestProjectPackageOutputsProductsAndTargets(t *testing.T) {
	i := projectPackageOutputsFixture(t, "phone", `[
		{name:'entry',srcPath:'app',targets:[
			{name:'phoneMain',applyToProducts:['phone','tablet']},
			{name:'phoneExtra',applyToProducts:['phone']},
			{name:'tabletOnly',applyToProducts:['tablet']},
			{name:'phoneMain',applyToProducts:['phone','phone']} ]},
		{name:'feature',srcPath:'feature'},
		{name:'shared',srcPath:'shared',targets:[{name:'sharedTarget',applyToProducts:['phone']}]},
		{name:'archive',srcPath:'archive'},
		{name:'excluded',srcPath:'excluded',targets:[{name:'default',applyToProducts:['tablet']}]},
		{name:'unmapped',srcPath:'unmapped',targets:[{name:'default'}]},
		{name:'empty',srcPath:'empty',targets:[]}
	]`)
	projectPackageOutputsModule(t, i, "app", "entry", `{targets:[{name:'phoneMain'},{name:'phoneExtra',output:{artifactName:'phone extra'}}]}`)
	projectPackageOutputsModule(t, i, "feature", "feature", "")
	projectPackageOutputsModule(t, i, "shared", "shared", `{targets:[{name:'sharedTarget',output:{artifactName:'public library'}}]}`)
	projectPackageOutputsModule(t, i, "archive", "har", "")
	// Excluded modules intentionally have no files: explicit mappings do not
	// implicitly fall back to default for the selected product.
	projectPackageOutputsWant(t, i,
		"app/build/phone/outputs/phoneMain/entry-phoneMain-signed.hap",
		"app/build/phone/outputs/phoneExtra/phone extra.hap",
		"feature/build/phone/outputs/default/feature-default-signed.hap",
		"shared/build/phone/outputs/sharedTarget/public library.hsp")

	i.metadata.productName = "tablet"
	projectPackageOutputsModule(t, i, "excluded", "entry", "")
	projectPackageOutputsWant(t, i,
		"app/build/tablet/outputs/phoneMain/entry-phoneMain-signed.hap",
		"app/build/tablet/outputs/tabletOnly/entry-tabletOnly-signed.hap",
		"feature/build/tablet/outputs/default/feature-default-signed.hap",
		"excluded/build/tablet/outputs/default/excluded-default-signed.hap")
}

func TestProjectPackageOutputsCustomArtifactNames(t *testing.T) {
	for _, artifact := range []string{"custom", "custom-unsigned", "自定义 app name", "name.hap", " leading and trailing ", ""} {
		t.Run(artifact, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, "default", `[{name:'entry',srcPath:'entry'}]`)
			encoded, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			projectPackageOutputsModule(t, i, "entry", "entry", `{targets:[{name:'default',output:{artifactName:`+string(encoded)+`}}]}`)
			filename := artifact + ".hap"
			if artifact == "" {
				filename = "entry-default-signed.hap"
			}
			projectPackageOutputsWant(t, i, "entry/build/default/outputs/default/"+filename)
		})
	}
}

func TestProjectPackageOutputsNoHAP(t *testing.T) {
	for _, kind := range []string{"har", "shared"} {
		t.Run(kind, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, "default", `[{name:'library',srcPath:'library'}]`)
			projectPackageOutputsModule(t, i, "library", kind, "")
			if _, err := i.packageOutputs(); err == nil || !strings.Contains(err.Error(), "at least one entry or feature") {
				t.Fatalf("expected clear HAP requirement, got %v", err)
			}
		})
	}
	i := projectPackageOutputsFixture(t, "default", `[]`)
	if _, err := i.packageOutputs(); err == nil || !strings.Contains(err.Error(), "no installable HAP") {
		t.Fatal(err)
	}
}

func TestProjectPackageOutputsDeduplicate(t *testing.T) {
	i := projectPackageOutputsFixture(t, "default", `[
		{name:'entry',srcPath:'entry',targets:[{name:'default',applyToProducts:['default']},{name:'default',applyToProducts:['default']}]},
		{name:'entry',srcPath:'./entry'}
	]`)
	projectPackageOutputsModule(t, i, "entry", "entry", "")
	projectPackageOutputsWant(t, i, "entry/build/default/outputs/default/entry-default-signed.hap")
}

func TestProjectPackageOutputsInvalidIdentifiers(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a..b", "../outside", "a/b", `a\b`, "/absolute", "has space", "bad\nname"} {
		t.Run(name, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, "default", `[{name:'entry',srcPath:'entry'}]`)
			projectPackageOutputsModule(t, i, "entry", "entry", "")
			module := projectMap(projectArray(i.metadata.profile["modules"])[0])
			module["targets"] = []any{map[string]any{"name": name, "applyToProducts": []any{"default"}}}
			if _, err := i.packageOutputs(); err == nil || !strings.Contains(err.Error(), "target identifier") {
				t.Fatalf("accepted invalid target %q: %v", name, err)
			}
		})
	}
	for _, tc := range []struct{ label, product, module string }{
		{"product", "../outside", "entry"},
		{"module", "default", "../outside"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, tc.product, `[{name:'entry',srcPath:'entry'}]`)
			projectMap(projectArray(i.metadata.profile["modules"])[0])["name"] = tc.module
			if _, err := i.packageOutputs(); err == nil {
				t.Fatal("accepted invalid identifier")
			}
		})
	}
}

func TestProjectPackageOutputsInvalidArtifactNames(t *testing.T) {
	for _, artifact := range []string{".", "..", "a..b", "../outside", "dir/file", `dir\file`, "/absolute", "bad\nname", "bad\x00name", "bad\u007fname", "bad\u0085name"} {
		t.Run(artifact, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, "default", `[{name:'entry',srcPath:'entry'}]`)
			encoded, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			projectPackageOutputsModule(t, i, "entry", "entry", `{targets:[{name:'default',output:{artifactName:`+string(encoded)+`}}]}`)
			if _, err := i.packageOutputs(); err == nil || !strings.Contains(err.Error(), "artifactName") {
				t.Fatalf("accepted invalid artifactName %q: %v", artifact, err)
			}
		})
	}
}

func TestProjectPackageOutputsMalformedDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name, modules, profile, kind, errorText string
	}{
		{"root targets object", `[{name:'entry',srcPath:'entry',targets:{}}]`, "", "entry", "target mappings"},
		{"root products string", `[{name:'entry',srcPath:'entry',targets:[{name:'default',applyToProducts:'default'}]}]`, "", "entry", "applyToProducts"},
		{"root products number", `[{name:'entry',srcPath:'entry',targets:[{name:'default',applyToProducts:[3]}]}]`, "", "entry", "product identifier"},
		{"module targets object", `[{name:'entry',srcPath:'entry'}]`, `{targets:{}}`, "entry", "module targets"},
		{"module target traversal", `[{name:'entry',srcPath:'entry'}]`, `{targets:[{name:'../default'}]}`, "entry", "target identifier"},
		{"duplicate module target", `[{name:'entry',srcPath:'entry'}]`, `{targets:[{name:'default'},{name:'default',output:{artifactName:'other'}}]}`, "entry", "duplicate module target"},
		{"duplicate module sources", `[{name:'entry',srcPath:'entry'},{name:'entry',srcPath:'other'}]`, "", "entry", "duplicate module"},
		{"output string", `[{name:'entry',srcPath:'entry'}]`, `{targets:[{name:'default',output:'bad'}]}`, "entry", "output configuration"},
		{"artifact number", `[{name:'entry',srcPath:'entry'}]`, `{targets:[{name:'default',output:{artifactName:3}}]}`, "entry", "artifactName"},
		{"invalid profile", `[{name:'entry',srcPath:'entry'}]`, `{targets:`, "entry", "build-profile.json5"},
		{"unknown module type", `[{name:'entry',srcPath:'entry'}]`, "", "unknown", "module.type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, "default", tc.modules)
			projectPackageOutputsModule(t, i, "entry", tc.kind, tc.profile)
			if _, err := i.packageOutputs(); err == nil || !strings.Contains(err.Error(), tc.errorText) {
				t.Fatalf("packageOutputs() error = %v; want %q", err, tc.errorText)
			}
		})
	}
}

func TestProjectPackageOutputsPathEscapes(t *testing.T) {
	for _, point := range []string{"source traversal", "absolute source", "module symlink", "descriptor symlink", "profile symlink", "build symlink", "output symlink", "dangling profile symlink"} {
		t.Run(point, func(t *testing.T) {
			i := projectPackageOutputsFixture(t, "default", `[{name:'entry',srcPath:'entry'}]`)
			module := projectMap(projectArray(i.metadata.profile["modules"])[0])
			outside := t.TempDir()
			if point == "source traversal" {
				module["srcPath"] = "../outside"
			} else if point == "absolute source" {
				module["srcPath"] = outside
			} else if point == "module symlink" {
				if err := os.Symlink(outside, filepath.Join(i.directory, "entry")); err != nil {
					t.Fatal(err)
				}
			} else {
				var relative, target string
				switch point {
				case "descriptor symlink":
					relative, target = "entry/src/main/module.json5", projectTestFile(t, outside, "module.json5", []byte(`{module:{type:'entry'}}`))
				case "profile symlink":
					relative, target = "entry/build-profile.json5", projectTestFile(t, outside, "build-profile.json5", []byte(`{}`))
				case "build symlink":
					relative, target = "entry/build", outside
				case "output symlink":
					relative, target = "entry/build/default/outputs/default/entry-default-signed.hap", projectTestFile(t, outside, "output.hap", []byte("old signed output"))
				case "dangling profile symlink":
					relative, target = "entry/build-profile.json5", filepath.Join(outside, "absent")
				}
				if point != "descriptor symlink" {
					projectPackageOutputsModule(t, i, "entry", "entry", "")
				}
				link := filepath.Join(i.directory, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := i.packageOutputs(); err == nil {
				t.Fatal("accepted escaping or dangling package path")
			}
		})
	}
}

func TestProjectPackageOutputsInternalSymlinkDeduplicate(t *testing.T) {
	i := projectPackageOutputsFixture(t, "default", `[{name:'entry',srcPath:'entry'},{name:'entry',srcPath:'alias'}]`)
	projectPackageOutputsModule(t, i, "entry", "entry", "")
	if err := os.Symlink("entry", filepath.Join(i.directory, "alias")); err != nil {
		t.Fatal(err)
	}
	projectPackageOutputsWant(t, i, "entry/build/default/outputs/default/entry-default-signed.hap")
}
