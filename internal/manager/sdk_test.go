package manager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const sdkBetaPackage = "26.0.0.35-Beta"

func sdkFixture(t *testing.T) *Manager {
	t.Helper()
	m, err := New(filepath.Join(t.TempDir(), "oheco root with spaces"), "https://example.invalid/index.json", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.prepare(); err != nil {
		t.Fatal(err)
	}
	sdkInstallFixture(t, m, sdkBetaPackage, SDKMetadata{APIVersion: 26, PlatformVersion: "26.0.0", Version: "26.0.0.35", ReleaseType: "Beta"})
	return m
}

func sdkInstallFixture(t *testing.T, m *Manager, packageVersion string, meta SDKMetadata) {
	t.Helper()
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sdkComponents {
		pkg := "ohos-sdk-" + name
		dir := filepath.Join(m.Root, "packages", pkg, packageVersion)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		// Match the real upstream packages: apiVersion is a JSON string,
		// although the managed SDKMetadata normalizes it to an integer.
		fields := map[string]any{"path": name, "apiVersion": strconv.Itoa(meta.APIVersion), "version": meta.Version, "releaseType": meta.ReleaseType, "displayName": "fixture SDK"}
		if meta.PlatformVersion != "" {
			fields["platformVersion"] = meta.PlatformVersion
		}
		if meta.FullAPIVersion != "" {
			fields["fullApiVersion"] = meta.FullAPIVersion
		}
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		// Retain unknown upstream fields and noncanonical file whitespace.
		data = append([]byte("\n"), data...)
		data = append(data, '\n')
		sdkWrite(t, filepath.Join(dir, "oh-uni-package.json"), data)
		// The real previewer package may contain metadata and NOTICE only.
		sdkWrite(t, filepath.Join(dir, "NOTICE.txt"), []byte("upstream notice, do not rewrite"))
		if name == "native" {
			sdkWrite(t, filepath.Join(dir, "es2abc"), []byte("signed binary fixture; never patch"))
		}
		installed := state.Packages[pkg]
		if installed.Versions == nil {
			installed.Versions = map[string]Receipt{}
		}
		installed.Versions[packageVersion] = Receipt{Platform: m.Platform, InstalledAt: "2026-09-01T00:00:00Z", Artifact: artifact(data, "https://example.invalid/sdk.zip", map[string]string{})}
		installed.Active = packageVersion
		state.Packages[pkg] = installed
	}
	if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), state); err != nil {
		t.Fatal(err)
	}
}

func sdkWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func sdkRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sdkMutateMetadata(t *testing.T, m *Manager, name string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(m.Root, "packages", "ohos-sdk-"+name, sdkBetaPackage, "oh-uni-package.json")
	var fields map[string]any
	if err := json.Unmarshal(sdkRead(t, path), &fields); err != nil {
		t.Fatal(err)
	}
	mutate(fields)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	sdkWrite(t, path, data)
}

func TestSDKBetaPureLinksAndIdempotence(t *testing.T) {
	m := sdkFixture(t)
	metadata := map[string][]byte{}
	for _, name := range sdkComponents {
		path := filepath.Join(m.Root, "packages", "ohos-sdk-"+name, sdkBetaPackage, "oh-uni-package.json")
		metadata[path] = sdkRead(t, path)
	}
	statePath := filepath.Join(m.Root, "state", "installed.json")
	before := sdkRead(t, statePath)
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != "26.0.0.35.Beta" || view.Root != filepath.Join(m.Root, "sdk", view.ID, "root") || view.Manifest.SDKVersion != "26.0.0" {
		t.Fatalf("wrong view: %+v", view)
	}
	manifestPath := filepath.Join(m.Root, "sdk", view.ID, "view.json")
	manifestBytes := sdkRead(t, manifestPath)
	for _, component := range view.Manifest.Components {
		path := filepath.Join(view.Root, "26.0.0", component.Name)
		target, err := os.Readlink(path)
		want := "../../../../packages/ohos-sdk-" + component.Name + "/" + sdkBetaPackage
		if err != nil || target != want || filepath.IsAbs(target) {
			t.Fatalf("%s: target=%q err=%v", component.Name, target, err)
		}
		if component.MetadataSHA256 != sdkHash(metadata[filepath.Join(m.Root, filepath.FromSlash(component.Source), "oh-uni-package.json")]) || len(component.ReceiptSHA256) != 64 {
			t.Fatal("manifest must bind original metadata and receipt hashes")
		}
	}
	again, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil || !reflect.DeepEqual(view, again) || !reflect.DeepEqual(manifestBytes, sdkRead(t, manifestPath)) {
		t.Fatalf("create is not idempotent: %+v %v", again, err)
	}
	views, err := m.SDKList()
	if err != nil || len(views) != 1 || !reflect.DeepEqual(views[0], view) {
		t.Fatalf("list=%+v err=%v", views, err)
	}
	root, err := m.SDKPath(view.ID)
	if err != nil || root != view.Root {
		t.Fatalf("path=%q err=%v", root, err)
	}
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal("remove must be idempotent", err)
	}
	missing(t, filepath.Dir(view.Root))
	for path, original := range metadata {
		if !reflect.DeepEqual(original, sdkRead(t, path)) {
			t.Fatalf("modified original metadata: %s", path)
		}
	}
	if string(sdkRead(t, filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage, "es2abc"))) != "signed binary fixture; never patch" || !reflect.DeepEqual(before, sdkRead(t, statePath)) {
		t.Fatal("SDK operation changed package payload or installed state")
	}
}

func TestSDKAPIVersionSourceRepresentationsNormalizeWithoutRewrite(t *testing.T) {
	for _, representation := range []string{"upstream-strings", "numeric-compatible", "mixed-representations"} {
		t.Run(representation, func(t *testing.T) {
			m := sdkFixture(t)
			for _, name := range sdkComponents {
				path := filepath.Join(m.Root, "packages", "ohos-sdk-"+name, sdkBetaPackage, "oh-uni-package.json")
				var fields map[string]any
				if err := json.Unmarshal(sdkRead(t, path), &fields); err != nil {
					t.Fatal(err)
				}
				if fields["apiVersion"] != "26" {
					t.Fatalf("default fixture must preserve the real upstream string API: %s: %#v", name, fields["apiVersion"])
				}
				if representation == "numeric-compatible" || representation == "mixed-representations" && name == "native" {
					sdkMutateMetadata(t, m, name, func(fields map[string]any) { fields["apiVersion"] = 26 })
				}
			}
			original := map[string][]byte{}
			for _, name := range sdkComponents {
				path := filepath.Join(m.Root, "packages", "ohos-sdk-"+name, sdkBetaPackage, "oh-uni-package.json")
				original[path] = sdkRead(t, path)
			}
			view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
			if err != nil {
				t.Fatal(err)
			}
			for _, component := range view.Manifest.Components {
				if component.Metadata.APIVersion != 26 {
					t.Fatalf("component API is not normalized: %+v", component)
				}
			}
			manifestData := sdkRead(t, filepath.Join(m.Root, "sdk", view.ID, "view.json"))
			var manifestFields struct {
				Components []struct {
					Metadata map[string]any `json:"metadata"`
				} `json:"components"`
			}
			if err := json.Unmarshal(manifestData, &manifestFields); err != nil {
				t.Fatal(err)
			}
			for _, component := range manifestFields.Components {
				if component.Metadata["apiVersion"] != float64(26) {
					t.Fatalf("manifest must serialize canonical numeric API: %#v", component.Metadata["apiVersion"])
				}
			}
			if root, err := m.SDKPath(view.ID); err != nil || root != view.Root {
				t.Fatalf("normalized source path is unavailable: %q %v", root, err)
			}
			for path, data := range original {
				if !reflect.DeepEqual(data, sdkRead(t, path)) {
					t.Fatalf("API normalization rewrote source metadata: %s", path)
				}
			}
		})
	}
}

func TestSDKAPIVersionRejectsNoncanonicalAndInvalidTypes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"empty", ""}, {"leading-space", " 26"}, {"trailing-space", "26 "},
		{"tab", "\t26"}, {"newline", "26\n"}, {"plus", "+26"}, {"negative-string", "-26"},
		{"leading-zero", "026"}, {"zero-string", "0"}, {"decimal-string", "26.0"}, {"exponent-string", "26e0"},
		{"bool-true", true}, {"bool-false", false}, {"null", nil}, {"zero-number", 0}, {"negative-number", -26},
		{"decimal-number", json.RawMessage("26.0")}, {"exponent-number", json.RawMessage("26e0")},
		{"overflow-string", "999999999999999999999999999"}, {"overflow-number", json.RawMessage("999999999999999999999999999")},
		{"object", map[string]any{"value": 26}}, {"array", []int{26}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{"apiVersion": tc.value, "platformVersion": "26.0.0", "version": "26.0.0.35", "releaseType": "Beta", "path": "native"}
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if meta, err := sdkDecodeMetadata(data); err == nil {
				t.Fatalf("accepted invalid/noncanonical API %#v as %+v", tc.value, meta)
			}
		})
	}
}

func TestSDKOfficialNamingAndAPIRules(t *testing.T) {
	for _, tc := range []struct {
		name, release, platform, full, inner string
		api                                  int
	}{
		{"release26", "Release", "26.0.0", "99", "26.0.0", 26},
		{"stable26", "Stable", "26.0.0", "", "26.0.0", 26},
		{"release23", "Release", "", "23.1", "23.1", 23},
		{"stable23", "Stable", "25.0.0", "", "23", 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sdkFixture(t)
			sdkInstallFixture(t, m, "official-package-1", SDKMetadata{APIVersion: tc.api, PlatformVersion: tc.platform, FullAPIVersion: tc.full, Version: "23.0.0.1", ReleaseType: tc.release})
			view, err := m.SDKCreate(context.Background(), "official-package-1")
			if err != nil {
				t.Fatal(err)
			}
			if view.ID != "23.0.0.1" || view.Manifest.SDKVersion != tc.inner {
				t.Fatalf("wrong official identity: %+v", view)
			}
		})
	}
	meta, err := sdkDecodeMetadata([]byte(`{"apiVersion":23,"fullApiVersion":23,"version":"23.0.0.1","releaseType":"Release","path":"native"}`))
	if err != nil || meta.FullAPIVersion != "23" {
		t.Fatalf("integer fullApiVersion: %+v %v", meta, err)
	}
}

func TestSDKMultipleVersionsIgnoreActiveAndSwitch(t *testing.T) {
	m := sdkFixture(t)
	sdkInstallFixture(t, m, "23.0.0.1", SDKMetadata{APIVersion: 23, Version: "23.0.0.1", ReleaseType: "Release"})
	beta, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := m.SDKCreate(context.Background(), "23.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sdkComponents {
		if err := m.Switch("ohos-sdk-"+name, sdkBetaPackage); err != nil {
			t.Fatal(err)
		}
	}
	for _, view := range []SDKView{beta, stable} {
		if _, err := m.SDKPath(view.ID); err != nil {
			t.Fatal("switch invalidated fixed view", err)
		}
		for _, c := range view.Manifest.Components {
			actual, err := os.Readlink(filepath.Join(view.Root, view.Manifest.SDKVersion, c.Name))
			if err != nil || !strings.HasSuffix(actual, "/"+view.Manifest.PackageVersion) {
				t.Fatalf("view followed active: %q %v", actual, err)
			}
		}
	}
	views, err := m.SDKList()
	if err != nil || len(views) != 2 || views[0].ID != stable.ID || views[1].ID != beta.ID {
		t.Fatalf("multiple views: %+v %v", views, err)
	}
}

func TestSDKMissingComponentsReportsCompleteList(t *testing.T) {
	m := sdkFixture(t)
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	delete(state.Packages, "ohos-sdk-previewer")
	delete(state.Packages, "ohos-sdk-js")
	if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), state); err != nil {
		t.Fatal(err)
	}
	_, err = m.SDKCreate(context.Background(), sdkBetaPackage)
	if err == nil || !strings.Contains(err.Error(), "ohos-sdk-previewer@"+sdkBetaPackage) || !strings.Contains(err.Error(), "ohos-sdk-js@"+sdkBetaPackage) {
		t.Fatalf("incomplete missing list: %v", err)
	}
	// The directories still exist. Their presence alone must not count.
	missing(t, filepath.Join(m.Root, "sdk"))
	_, err = m.SDKCreate(context.Background(), "latest")
	if err == nil || strings.Count(err.Error(), "@latest") != 5 {
		t.Fatalf("latest must not resolve active/latest implicitly: %v", err)
	}
}

func TestSDKRejectMixedAndUnsafeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"mixed-api", "apiVersion", 23},
		{"mixed-platform", "platformVersion", "26.1.0"},
		{"mixed-release", "releaseType", "Release"},
		{"mixed-version", "version", "26.0.0.36"},
		{"mixed-full", "fullApiVersion", "26.1"},
		{"empty-platform26", "platformVersion", ""},
		{"path-traversal", "path", "../previewer"},
		{"path-absolute", "path", "/native"},
		{"path-case", "path", "Native"},
		{"version-traversal", "version", "../26"},
		{"platform-traversal", "platformVersion", "../26"},
		{"unsupported-release", "releaseType", "beta"},
		{"api-string-whitespace", "apiVersion", " 26"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sdkFixture(t)
			sdkMutateMetadata(t, m, "native", func(fields map[string]any) { fields[tc.key] = tc.value })
			if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil {
				t.Fatal("accepted mismatched/unsafe metadata")
			}
			missing(t, filepath.Join(m.Root, "sdk"))
		})
	}
	for _, data := range []string{
		`{"apiVersion":26,"apiVersion":23}`,
		`{"apiVersion":26} {}`,
		`[]`,
	} {
		if _, err := sdkDecodeMetadata([]byte(data)); err == nil {
			t.Fatalf("accepted bad metadata %s", data)
		}
	}
	m := sdkFixture(t)
	for _, version := range []string{"../outside", "/outside", "", "26/evil"} {
		if _, err := m.SDKCreate(context.Background(), version); err == nil {
			t.Fatalf("accepted unsafe packageVersion %q", version)
		}
		if err := m.SDKRemove(context.Background(), version); err == nil {
			t.Fatalf("accepted unsafe viewID %q", version)
		}
	}
}

func TestSDKManifestAndOfficialAliasConflicts(t *testing.T) {
	m := sdkFixture(t)
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	sdkInstallFixture(t, m, "26.0.0.35-Beta-ohos.2", SDKMetadata{APIVersion: 26, PlatformVersion: "26.0.0", Version: "26.0.0.35", ReleaseType: "Beta"})
	if _, err := m.SDKCreate(context.Background(), "26.0.0.35-Beta-ohos.2"); err == nil || !strings.Contains(err.Error(), "different manifest") {
		t.Fatalf("overwrote colliding view ID: %v", err)
	}
	metaPath := filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage, "oh-uni-package.json")
	sdkWrite(t, metaPath, append(sdkRead(t, metaPath), '\n'))
	if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil {
		t.Fatal("ignored original metadata digest change")
	}
	if _, err := m.SDKPath(view.ID); err == nil {
		t.Fatal("path accepted modified source metadata")
	}
	// Remove only owns the links, so changed/missing sources do not prevent it.
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	sdkInstallFixture(t, m, "release-package", SDKMetadata{APIVersion: 23, Version: "23.0.0.1", ReleaseType: "Release"})
	sdkInstallFixture(t, m, "stable-package", SDKMetadata{APIVersion: 23, Version: "23.0.0.1", ReleaseType: "Stable"})
	if _, err := m.SDKCreate(context.Background(), "release-package"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKCreate(context.Background(), "stable-package"); err == nil {
		t.Fatal("Stable/Release alias collision must fail closed")
	}
}

func TestSDKRejectReplacedBoundaries(t *testing.T) {
	for _, level := range []string{"root", "packages", "state", "sdk", "package", "version", "metadata", "view", "view-root", "inner", "component"} {
		t.Run(level, func(t *testing.T) {
			m := sdkFixture(t)
			view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			sdkWrite(t, sentinel, []byte("do not change"))
			paths := map[string]string{
				"root": m.Root, "packages": filepath.Join(m.Root, "packages"), "state": filepath.Join(m.Root, "state"), "sdk": filepath.Join(m.Root, "sdk"),
				"package": filepath.Join(m.Root, "packages", "ohos-sdk-native"), "version": filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage),
				"metadata": filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage, "oh-uni-package.json"),
				"view":     filepath.Dir(view.Root), "view-root": view.Root, "inner": filepath.Join(view.Root, "26.0.0"), "component": filepath.Join(view.Root, "26.0.0", "native"),
			}
			path := paths[level]
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			target := outside
			if level == "metadata" {
				target = sentinel
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil {
				t.Fatal("create followed replaced boundary")
			}
			if _, err := m.SDKPath(view.ID); err == nil {
				t.Fatal("path followed replaced boundary")
			}
			if level != "metadata" && level != "package" && level != "version" {
				if err := m.SDKRemove(context.Background(), view.ID); err == nil {
					t.Fatal("remove followed replaced view boundary")
				}
			}
			if string(sdkRead(t, sentinel)) != "do not change" {
				t.Fatal("external target changed")
			}
		})
	}
}

// Inject deterministic cancellation/failures without executing real tools or
// using permissions (chmod is not an ownership control on HOME/hmdfs).
type sdkStepContext struct {
	context.Context
	calls    int
	cancelAt int
	onCall   func(int)
}

func (c *sdkStepContext) Err() error {
	c.calls++
	if c.onCall != nil {
		c.onCall(c.calls)
	}
	if c.cancelAt != 0 && c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestSDKCreationRollbackAndPartialRecovery(t *testing.T) {
	m := sdkFixture(t)
	ctx := &sdkStepContext{Context: context.Background(), cancelAt: 6} // one link created
	if _, err := m.SDKCreate(ctx, sdkBetaPackage); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected injected cancellation: %v", err)
	}
	missing(t, filepath.Join(m.Root, "sdk", "26.0.0.35.Beta"))
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(view.Root, "26.0.0", "previewer")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKList(); err == nil {
		t.Fatal("list silently accepted incomplete view")
	}
	if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err != nil {
		t.Fatal("did not recover matching partial creation", err)
	}
	ctx = &sdkStepContext{Context: context.Background(), cancelAt: 4} // one link removed
	if err := m.SDKRemove(ctx, view.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected partial removal cancellation: %v", err)
	}
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal("did not recover partial removal", err)
	}
}

func TestSDKRollbackRetainsNewUnmanagedContent(t *testing.T) {
	m := sdkFixture(t)
	userFile := filepath.Join(m.Root, "sdk", "26.0.0.35.Beta", "user-data")
	ctx := &sdkStepContext{Context: context.Background(), onCall: func(n int) {
		if n == 5 {
			sdkWrite(t, userFile, []byte("retain me"))
		}
	}}
	if _, err := m.SDKCreate(ctx, sdkBetaPackage); err == nil || !strings.Contains(err.Error(), "rollback retained") {
		t.Fatalf("expected safe retained artifact: %v", err)
	}
	if string(sdkRead(t, userFile)) != "retain me" {
		t.Fatal("rollback removed unrelated content")
	}
	if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil {
		t.Fatal("unmanaged retained directory must require review")
	}
}

func TestSDKRemovalDoesNotFollowSourceLinks(t *testing.T) {
	m := sdkFixture(t)
	outside := t.TempDir()
	path := filepath.Join(outside, "external-data")
	sdkWrite(t, path, []byte("retained"))
	source := filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage)
	if err := os.Symlink(outside, filepath.Join(source, "external")); err != nil {
		t.Fatal(err)
	}
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the source directory after creation. Removing the view still
	// unlinks only its exact relative links, not the new source or its contents.
	if err := os.Rename(source, source+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	if string(sdkRead(t, path)) != "retained" {
		t.Fatal("view remove traversed source")
	}
	if info, err := os.Lstat(source); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("source link was removed", err)
	}
}

func TestSDKReferenceGuardChecksFinalPlan(t *testing.T) {
	m := sdkFixture(t)
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	sdkInstallFixture(t, m, "23.0.0.1", SDKMetadata{APIVersion: 23, Version: "23.0.0.1", ReleaseType: "Release"})
	before, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	after := cloneState(before)
	pkg := after.Packages["ohos-sdk-native"]
	delete(pkg.Versions, "23.0.0.1")
	pkg.Active = sdkBetaPackage
	after.Packages["ohos-sdk-native"] = pkg
	if err := m.SDKRemovalGuard(before, after); err != nil {
		t.Fatal("unreferenced version must remain removable", err)
	}
	for _, name := range []string{"native", "ets", "previewer"} {
		after := cloneState(before)
		delete(after.Packages, "ohos-sdk-"+name) // final autoremove/cascade result
		if err := m.SDKRemovalGuard(before, after); err == nil || !strings.Contains(err.Error(), view.ID) || !strings.Contains(err.Error(), "ohos-sdk-"+name+"@"+sdkBetaPackage) {
			t.Fatalf("guard missed final removal of %s: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(view.Root, "26.0.0", "previewer")); err != nil {
		t.Fatal(err)
	}
	after = cloneState(before)
	delete(after.Packages, "ohos-sdk-native")
	if err := m.SDKRemovalGuard(before, after); err == nil {
		t.Fatal("partial view lost reference protection")
	}
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.SDKRemovalGuard(before, after); err != nil {
		t.Fatal("removed view still protects package", err)
	}
	// Unrecognized SDK artifacts fail closed rather than guessing references.
	if err := os.Mkdir(filepath.Join(m.Root, "sdk", "user-data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.SDKRemovalGuard(before, after); err == nil {
		t.Fatal("unmanaged SDK content must fail closed")
	}
}

func TestSDKMissingInstalledDirectories(t *testing.T) {
	m := sdkFixture(t)
	for _, name := range []string{"native", "previewer"} {
		if err := os.RemoveAll(filepath.Join(m.Root, "packages", "ohos-sdk-"+name, sdkBetaPackage)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err == nil || !strings.Contains(err.Error(), "ohos-sdk-native@"+sdkBetaPackage) || !strings.Contains(err.Error(), "ohos-sdk-previewer@"+sdkBetaPackage) {
		t.Fatalf("missing directory list incomplete: %v", err)
	}
}

func TestSDKReceiptAndManifestTampering(t *testing.T) {
	m := sdkFixture(t)
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	pkg := state.Packages["ohos-sdk-native"]
	r := pkg.Versions[sdkBetaPackage]
	r.Artifact.SHA256 = strings.Repeat("1", 64) // immutable content identity, not install bookkeeping
	pkg.Versions[sdkBetaPackage] = r
	state.Packages["ohos-sdk-native"] = pkg
	if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKPath(view.ID); err == nil {
		t.Fatal("ignored changed receipt hash")
	}
	if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil {
		t.Fatal("overwrote different receipt manifest")
	}
	manifestPath := filepath.Join(m.Root, "sdk", view.ID, "view.json")
	manifest := view.Manifest
	manifest.Components[0].Source = "../../outside"
	if err := writeJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKList(); err == nil {
		t.Fatal("accepted path traversal in manifest")
	}
	if err := m.SDKRemove(context.Background(), view.ID); err == nil {
		t.Fatal("removed tampered manifest view")
	}
}

func TestSDKRollbackDoesNotFollowReplacedParent(t *testing.T) {
	m := sdkFixture(t)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "native")
	sdkWrite(t, sentinel, []byte("external sentinel"))
	root := filepath.Join(m.Root, "sdk", "26.0.0.35.Beta", "root")
	ctx := &sdkStepContext{Context: context.Background(), onCall: func(n int) {
		if n == 6 { // replace the parent after creating the first link
			if err := os.Rename(root, root+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, root); err != nil {
				t.Fatal(err)
			}
		}
	}}
	if _, err := m.SDKCreate(ctx, sdkBetaPackage); err == nil {
		t.Fatal("accepted replaced parent")
	}
	if string(sdkRead(t, sentinel)) != "external sentinel" {
		t.Fatal("rollback followed replaced parent")
	}
}

func TestSDKUsesManagerLockAndRejectsCaseAliases(t *testing.T) {
	m := sdkFixture(t)
	if err := m.withLock(func() error {
		if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil || !strings.Contains(err.Error(), "cannot lock") {
			t.Fatalf("create did not share manager lock: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(view.Root), filepath.Join(m.Root, "sdk", "26.0.0.35.beta")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKCreate(context.Background(), sdkBetaPackage); err == nil || !strings.Contains(err.Error(), "case-folding") {
		t.Fatalf("accepted case alias: %v", err)
	}
	if err := m.SDKRemove(context.Background(), "26.0.0.35.beta"); err == nil {
		t.Fatal("accepted noncanonical view ID")
	}
}
