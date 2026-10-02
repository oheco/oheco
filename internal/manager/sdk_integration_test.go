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

func sdkRemovalFixture(t *testing.T, mode string) (*Manager, SDKView, []string, RemoveOptions) {
	t.Helper()
	m := sdkFixture(t)
	if mode == "all" {
		sdkInstallFixture(t, m, "23.0.0.1", SDKMetadata{APIVersion: 23, Version: "23.0.0.1", ReleaseType: "Release"})
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion = 2
	addPackage := func(name string, dependencies []InstalledDependency) {
		dir := filepath.Join(m.Root, "packages", name, "1.0.0")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		sdkWrite(t, filepath.Join(dir, "payload"), []byte("controlled package payload"))
		r := state.Packages["ohos-sdk-native"].Versions[sdkBetaPackage]
		r.Automatic = false
		r.Dependencies = dependencies
		state.Packages[name] = InstalledPackage{Active: "1.0.0", Versions: map[string]Receipt{"1.0.0": r}}
	}
	specs := []string{"ohos-sdk-native@" + sdkBetaPackage}
	options := RemoveOptions{Yes: true}
	switch mode {
	case "direct":
	case "batch":
		addPackage("sdk-unrelated", nil)
		specs = []string{"sdk-unrelated", "ohos-sdk-native@" + sdkBetaPackage}
	case "all":
		specs = []string{"ohos-sdk-native"}
		options.All = true
	case "cascade":
		addPackage("sdk-anchor", nil)
		pkg := state.Packages["ohos-sdk-native"]
		r := pkg.Versions[sdkBetaPackage]
		r.Dependencies = []InstalledDependency{{Dependency: catalog.Dependency{Name: "sdk-anchor", Constraint: "*"}, Version: "1.0.0"}}
		pkg.Versions[sdkBetaPackage] = r
		state.Packages["ohos-sdk-native"] = pkg
		specs = []string{"sdk-anchor"} // the referenced native version is indirect
		options.Cascade = true
	case "autoremove":
		pkg := state.Packages["ohos-sdk-native"]
		r := pkg.Versions[sdkBetaPackage]
		r.Automatic = true
		pkg.Versions[sdkBetaPackage] = r
		state.Packages["ohos-sdk-native"] = pkg
		addPackage("sdk-consumer", []InstalledDependency{{Dependency: catalog.Dependency{Name: "ohos-sdk-native", Constraint: "*"}, Version: sdkBetaPackage}})
		specs = []string{"sdk-consumer"} // SDK native is only an orphan candidate
		options.AutoRemove = true
	default:
		t.Fatalf("unknown fixture mode %q", mode)
	}
	if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), state); err != nil {
		t.Fatal(err)
	}
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	return m, view, specs, options
}

func sdkAssertBlockedPlanUnchanged(t *testing.T, m *Manager, view SDKView, stateBytes, manifestBytes []byte, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "referenced by SDK views") || !strings.Contains(err.Error(), "ohos-sdk-native@"+sdkBetaPackage) || !strings.Contains(err.Error(), view.ID) {
		t.Fatalf("final removal plan escaped SDK protection: %v", err)
	}
	if !reflect.DeepEqual(stateBytes, sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))) {
		t.Fatal("blocked removal changed installed state")
	}
	if !reflect.DeepEqual(manifestBytes, sdkRead(t, filepath.Join(m.Root, "sdk", view.ID, "view.json"))) {
		t.Fatal("blocked removal changed view manifest")
	}
	if string(sdkRead(t, filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage, "es2abc"))) != "signed binary fixture; never patch" {
		t.Fatal("blocked removal changed pinned source")
	}
	state, loadErr := m.LoadState()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	for name, installed := range state.Packages {
		for version := range installed.Versions {
			if err := sdkRealDir(filepath.Join(m.Root, "packages", name, version)); err != nil {
				t.Fatalf("blocked batch removed source %s@%s: %v", name, version, err)
			}
		}
	}
	missing(t, filepath.Join(m.Root, "state", "transaction.json"))
}

func TestSDKRemovalIntegrationFinalPlans(t *testing.T) {
	for _, mode := range []string{"direct", "batch", "all", "cascade", "autoremove"} {
		for _, dryRun := range []bool{false, true} {
			name := mode + "/actual"
			if dryRun {
				name = mode + "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				m, view, specs, options := sdkRemovalFixture(t, mode)
				var out bytes.Buffer
				m.Out = &out
				options.DryRun = dryRun
				stateBytes := sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))
				manifestBytes := sdkRead(t, filepath.Join(m.Root, "sdk", view.ID, "view.json"))
				err := m.RemoveMany(context.Background(), specs, options)
				sdkAssertBlockedPlanUnchanged(t, m, view, stateBytes, manifestBytes, err)
				if strings.Contains(out.String(), "Continue with this removal?") {
					t.Fatal("SDK plan protection must run before destructive confirmation")
				}
				if err := m.SDKRemove(context.Background(), view.ID); err != nil {
					t.Fatal(err)
				}
				if dryRun {
					if err := m.RemoveMany(context.Background(), specs, options); err != nil {
						t.Fatal("unreferenced dry-run was rejected", err)
					}
					if !reflect.DeepEqual(stateBytes, sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))) {
						t.Fatal("accepted dry-run changed state")
					}
					options.DryRun = false
				}
				if err := m.RemoveMany(context.Background(), specs, options); err != nil {
					t.Fatal("removal must succeed after view removal", err)
				}
				state, err := m.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := state.Packages["ohos-sdk-native"].Versions[sdkBetaPackage]; exists {
					t.Fatal("unreferenced exact package version was not removed")
				}
				missing(t, filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage))
				for _, name := range []string{"ets", "js", "toolchains", "previewer"} {
					if _, exists := state.Packages["ohos-sdk-"+name].Versions[sdkBetaPackage]; !exists {
						t.Fatalf("removal touched unrelated SDK component %s", name)
					}
				}
			})
		}
	}
}

func TestSDKRemovalIntegrationInteractiveCleanupFinalGuard(t *testing.T) {
	m, view, specs, _ := sdkRemovalFixture(t, "autoremove")
	m.In = strings.NewReader("y\n") // consent adds the referenced orphan to the final plan
	stateBytes := sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))
	manifestBytes := sdkRead(t, filepath.Join(m.Root, "sdk", view.ID, "view.json"))
	err := m.RemoveMany(context.Background(), specs, RemoveOptions{})
	sdkAssertBlockedPlanUnchanged(t, m, view, stateBytes, manifestBytes, err)
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	m.In = strings.NewReader("y\ny\n") // cleanup consent, then removal confirmation
	if err := m.RemoveMany(context.Background(), specs, RemoveOptions{}); err != nil {
		t.Fatal("interactive removal still blocked after view removal", err)
	}
}

func TestSDKRemovalIntegrationRetainsPinWithoutAutoremove(t *testing.T) {
	m, view, specs, _ := sdkRemovalFixture(t, "autoremove")
	stateBytes := sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))
	if err := m.RemoveMany(context.Background(), specs, RemoveOptions{Yes: true, DryRun: true}); err != nil {
		t.Fatal("dry-run without autoremove must not include the pinned orphan", err)
	}
	if !reflect.DeepEqual(stateBytes, sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))) {
		t.Fatal("dry-run changed state")
	}
	if err := m.RemoveMany(context.Background(), specs, RemoveOptions{Yes: true}); err != nil {
		t.Fatal("--yes without autoremove must retain the pinned orphan", err)
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Packages["sdk-consumer"]; exists {
		t.Fatal("unreferenced consumer was not removed")
	}
	if _, exists := state.Packages["ohos-sdk-native"].Versions[sdkBetaPackage]; !exists {
		t.Fatal("--yes implied autoremove of the pinned SDK")
	}
	if root, err := m.SDKPath(view.ID); err != nil || root != view.Root {
		t.Fatalf("retained SDK is not usable: %q %v", root, err)
	}
}

func TestSDKRemovalIntegrationManagerWrapperAndActiveSwitch(t *testing.T) {
	m := sdkFixture(t)
	sdkInstallFixture(t, m, "23.0.0.1", SDKMetadata{APIVersion: 23, Version: "23.0.0.1", ReleaseType: "Release"})
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Switch("ohos-sdk-native", "23.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("ohos-sdk-native", false); err != nil {
		t.Fatal("unpinned active version must remain removable", err)
	}
	if root, err := m.SDKPath(view.ID); err != nil || root != view.Root {
		t.Fatalf("switch/removal changed pinned root: %q %v", root, err)
	}
	// Removing an active version deliberately leaves Active empty. Select the
	// remaining pinned version explicitly before testing the unversioned wrapper.
	if err := m.Switch("ohos-sdk-native", sdkBetaPackage); err != nil {
		t.Fatal(err)
	}
	stateBytes := sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))
	manifestBytes := sdkRead(t, filepath.Join(m.Root, "sdk", view.ID, "view.json"))
	for _, tc := range []struct {
		spec string
		all  bool
	}{
		{"ohos-sdk-native@" + sdkBetaPackage, false},
		{"ohos-sdk-native", false},
		{"ohos-sdk-native", true},
	} {
		err := m.Remove(tc.spec, tc.all)
		sdkAssertBlockedPlanUnchanged(t, m, view, stateBytes, manifestBytes, err)
	}
	if err := m.SDKRemove(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("ohos-sdk-native", true); err != nil {
		t.Fatal("wrapper still blocks after view removal", err)
	}
}

func TestSDKRemovalIntegrationAutomaticReceiptSwitchKeepsViewUsable(t *testing.T) {
	m, view, _, _ := sdkRemovalFixture(t, "autoremove")
	manifestPath := filepath.Join(m.Root, "sdk", view.ID, "view.json")
	manifestBytes := sdkRead(t, manifestPath)
	metadataPath := filepath.Join(m.Root, "packages", "ohos-sdk-native", sdkBetaPackage, "oh-uni-package.json")
	metadataBytes := sdkRead(t, metadataPath)
	// A selected version may have been installed automatically as a native
	// dependency. Explicit switch promotes ownership, not SDK artifact contents.
	if err := m.Switch("ohos-sdk-native", sdkBetaPackage); err != nil {
		t.Fatal(err)
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Packages["ohos-sdk-native"].Versions[sdkBetaPackage].Automatic {
		t.Fatal("fixture did not exercise explicit ownership promotion")
	}
	if root, err := m.SDKPath(view.ID); err != nil || root != view.Root {
		t.Fatalf("ownership-only switch invalidated fixed SDK view: root=%q err=%v", root, err)
	}
	if again, err := m.SDKCreate(context.Background(), sdkBetaPackage); err != nil || !reflect.DeepEqual(again, view) {
		t.Fatalf("ownership-only switch broke idempotent SDK create: %+v %v", again, err)
	}
	// Timestamp and active selection are bookkeeping as well. Neither may
	// change a view's immutable receipt identity or its exact package pin.
	pkg := state.Packages["ohos-sdk-native"]
	r := pkg.Versions[sdkBetaPackage]
	r.InstalledAt = "2026-09-02T00:00:00Z"
	pkg.Versions[sdkBetaPackage] = r
	pkg.Active = ""
	state.Packages["ohos-sdk-native"] = pkg
	if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), state); err != nil {
		t.Fatal(err)
	}
	if root, err := m.SDKPath(view.ID); err != nil || root != view.Root {
		t.Fatalf("timestamp/active bookkeeping invalidated SDK view: %q %v", root, err)
	}
	if again, err := m.SDKCreate(context.Background(), sdkBetaPackage); err != nil || !reflect.DeepEqual(again, view) {
		t.Fatalf("timestamp/active bookkeeping broke create idempotence: %+v %v", again, err)
	}
	if !reflect.DeepEqual(manifestBytes, sdkRead(t, manifestPath)) || !reflect.DeepEqual(metadataBytes, sdkRead(t, metadataPath)) {
		t.Fatal("switch rewrote the view or original metadata")
	}
}

func TestSDKRemovalIntegrationCommitGuardBackstop(t *testing.T) {
	m := sdkFixture(t)
	view, err := m.SDKCreate(context.Background(), sdkBetaPackage)
	if err != nil {
		t.Fatal(err)
	}
	stateBytes := sdkRead(t, filepath.Join(m.Root, "state", "installed.json"))
	manifestBytes := sdkRead(t, filepath.Join(m.Root, "sdk", view.ID, "view.json"))
	err = m.withLock(func() error {
		before, err := m.LoadState()
		if err != nil {
			return err
		}
		after := stateWithout(before, map[removeKey]bool{{Name: "ohos-sdk-native", Version: sdkBetaPackage}: true})
		// Bypass RemoveMany intentionally: native commit must independently
		// enforce the same final-state guard without acquiring another lock.
		return m.commit(before, after, "", "")
	})
	sdkAssertBlockedPlanUnchanged(t, m, view, stateBytes, manifestBytes, err)
}
