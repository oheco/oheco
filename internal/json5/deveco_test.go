package json5_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/json5"
)

// Exercise the same payload through typed fields and generic maps. In
// particular, comment markers inside signing paths and URLs must remain data.
func TestDevEcoConfiguration(t *testing.T) {
	const input = `/* 工程设置 */ {
		app: {
			bundleName: 'org.oheco.示例',
			versionCode: 0x10,
			label: '中文应用',
		},
		module: {name: 'entry', type: 'entry', deviceTypes: ['phone', 'tablet',],},
		modules: [{name: 'entry', srcPath: './entry', targets: [{name: 'default',},],},],
		signingConfigs: [{
			name: 'default',
			material: {
				storeFile: '/项目//密钥/*保留*/.p12',
				profile: 'https://example.test/中文/profile',
				keyAlias: 'O\'Brien',
				certpath: 'C:\\签名\\证书.cer',
			},
		},], // 行注释
		'说明': '保留中文键和值',
	}`
	type target struct {
		App struct {
			BundleName  string `json:"bundleName"`
			VersionCode int    `json:"versionCode"`
			Label       string `json:"label"`
		} `json:"app"`
		Module struct {
			Name        string   `json:"name"`
			Type        string   `json:"type"`
			DeviceTypes []string `json:"deviceTypes"`
		} `json:"module"`
		Modules []struct {
			Name    string `json:"name"`
			SrcPath string `json:"srcPath"`
			Targets []struct {
				Name string `json:"name"`
			} `json:"targets"`
		} `json:"modules"`
		SigningConfigs []struct {
			Name     string            `json:"name"`
			Material map[string]string `json:"material"`
		} `json:"signingConfigs"`
		Description string `json:"说明"`
	}
	var got target
	if err := json5.Unmarshal([]byte(input), &got); err != nil {
		t.Fatal(err)
	}
	if got.App.BundleName != "org.oheco.示例" || got.App.Label != "中文应用" || got.App.VersionCode != 16 || got.Description != "保留中文键和值" {
		t.Fatalf("app or Unicode fields changed: %+v", got)
	}
	if got.Module.Name != "entry" || got.Module.Type != "entry" || !reflect.DeepEqual(got.Module.DeviceTypes, []string{"phone", "tablet"}) {
		t.Fatalf("module fields changed: %+v", got.Module)
	}
	if len(got.Modules) != 1 || got.Modules[0].SrcPath != "./entry" || len(got.Modules[0].Targets) != 1 || got.Modules[0].Targets[0].Name != "default" {
		t.Fatalf("build-profile modules changed: %+v", got.Modules)
	}
	wantMaterial := map[string]string{
		"storeFile": "/项目//密钥/*保留*/.p12",
		"profile":   "https://example.test/中文/profile",
		"keyAlias":  "O'Brien",
		"certpath":  `C:\签名\证书.cer`,
	}
	if len(got.SigningConfigs) != 1 || got.SigningConfigs[0].Name != "default" || !reflect.DeepEqual(got.SigningConfigs[0].Material, wantMaterial) {
		t.Fatalf("signing fields changed: %+v", got.SigningConfigs)
	}
	var generic map[string]interface{}
	if err := json5.Unmarshal([]byte(input), &generic); err != nil {
		t.Fatal(err)
	}
	if generic["说明"] != "保留中文键和值" || generic["app"].(map[string]interface{})["label"] != "中文应用" {
		t.Fatalf("map Unicode fields changed: %#v", generic)
	}
	material := generic["signingConfigs"].([]interface{})[0].(map[string]interface{})["material"].(map[string]interface{})
	for key, want := range wantMaterial {
		if material[key] != want {
			t.Errorf("map material[%q] = %q; want %q", key, material[key], want)
		}
	}
}

func TestRejectMalformedConfiguration(t *testing.T) {
	for _, input := range []string{
		`{app: {bundleName: 'unfinished}}`,
		`{a: 1 b: 2}`,
		`{a: }`,
		`{a: 1,,}`,
		`{a: arbitraryExpression()}`,
		`{a: 1} {b: 2}`,
		`/* unterminated`,
	} {
		t.Run(input, func(t *testing.T) {
			var got map[string]interface{}
			if err := json5.Unmarshal([]byte(input), &got); err == nil {
				t.Fatalf("accepted malformed input %q", input)
			}
		})
	}
	var typed struct {
		VersionCode int `json:"versionCode"`
	}
	if err := json5.Unmarshal([]byte(`{versionCode: 'wrong type'}`), &typed); err == nil {
		t.Fatal("accepted string as integer")
	}
}

func TestDecoderUseNumber(t *testing.T) {
	dec := json5.NewDecoder(strings.NewReader(`{versionCode: 9007199254740993,}`))
	dec.UseNumber()
	var got map[string]interface{}
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["versionCode"] != json5.Number("9007199254740993") {
		t.Fatalf("large integer changed: %#v", got)
	}
	if err := dec.Decode(&got); err != io.EOF {
		t.Fatalf("second decode = %v; want EOF", err)
	}
}

// Run the upstream acceptance/rejection corpus without the JavaScript VM and
// pretty-diff dependencies used by upstream's original oracle-based test.
func TestUpstreamSyntaxCorpus(t *testing.T) {
	root := filepath.Join("..", "..", "tpr", "json5", "testdata")
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".json" && ext != ".json5" && ext != ".js" && ext != ".txt" {
			return nil
		}
		count++
		t.Run(strings.TrimPrefix(path, root+string(filepath.Separator)), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got interface{}
			err = json5.Unmarshal(data, &got)
			valid := ext == ".json" || ext == ".json5"
			if valid && err != nil {
				t.Fatalf("rejected valid upstream fixture: %v", err)
			}
			if !valid && err == nil {
				t.Fatal("accepted invalid upstream fixture")
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("upstream corpus is empty")
	}
	t.Logf("checked %d upstream syntax fixtures", count)
}
