package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/manager"
)

const sdkCLIPackageVersion = "26.0.0.35-Beta"
const sdkCLIViewID = "26.0.0.35.Beta"

func sdkCLIInstalledFixture(t *testing.T, root, indexURL string) *manager.Manager {
	t.Helper()
	m, err := manager.New(root, indexURL, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state := manager.State{SchemaVersion: 1, Packages: map[string]manager.InstalledPackage{}}
	for _, name := range []string{"native", "ets", "js", "toolchains", "previewer"} {
		pkg := "ohos-sdk-" + name
		dir := filepath.Join(root, "packages", pkg, sdkCLIPackageVersion)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		// The released component metadata uses string apiVersion, not a JSON number.
		data, err := json.Marshal(map[string]any{"apiVersion": "26", "platformVersion": "26.0.0", "releaseType": "Beta", "version": "26.0.0.35", "path": name, "displayName": "original fixture SDK"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "oh-uni-package.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "NOTICE.txt"), []byte("real installed metadata-only component fixture"), 0644); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		r := manager.Receipt{Platform: m.Platform, InstalledAt: "2026-09-01T00:00:00Z", Artifact: catalog.Artifact{URL: "https://example.invalid/sdk.zip", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data)), Format: "zip", Binaries: map[string]string{}}}
		state.Packages[pkg] = manager.InstalledPackage{Active: sdkCLIPackageVersion, Versions: map[string]manager.Receipt{sdkCLIPackageVersion: r}}
	}
	if err := os.MkdirAll(filepath.Join(root, "state"), 0755); err != nil {
		t.Fatal(err)
	}
	sdkCLIWriteState(t, root, state)
	return m
}

func sdkCLIWriteState(t *testing.T, root string, state manager.State) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state", "installed.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func sdkCLIRead(t *testing.T, filename string) []byte {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sdkCLIAssertNoUpdater(t *testing.T, root, pidFile string, requests *atomic.Int32) {
	t.Helper()
	// TryUpdateLock creates this file synchronously before any background child
	// starts. This avoids an unreliable sleep/poll to prove a negative request.
	for _, path := range []string{filepath.Join(root, "state", "index-update.lock"), pidFile} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("SDK command started the automatic updater: %s: %v", path, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("SDK command accessed the index: %d requests", requests.Load())
	}
	entries, err := os.ReadDir(filepath.Join(root, "index"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("SDK command modified the index: %v %v", entries, err)
	}
}

func TestSDKCLIRealOfflineLifecycleNoAutomaticUpdate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "CLI root with spaces")
	pidFile := filepath.Join(t.TempDir(), "unexpected-updater.pid")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "SDK commands must remain offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	url := server.URL + "/index.json"
	m := sdkCLIInstalledFixture(t, root, url)
	statePath := filepath.Join(root, "state", "installed.json")
	stateBefore := sdkCLIRead(t, statePath)
	metadataBefore := map[string][]byte{}
	for name := range mustSDKCLIState(t, m).Packages {
		path := filepath.Join(root, "packages", name, sdkCLIPackageVersion, "oh-uni-package.json")
		metadataBefore[path] = sdkCLIRead(t, path)
	}
	viewRoot := filepath.Join(root, "sdk", sdkCLIViewID, "root")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sdk", "list"}, ""},
		{[]string{"sdk", "create", sdkCLIPackageVersion}, "SDK " + sdkCLIViewID + ": " + viewRoot + "\n"},
		{[]string{"sdk", "create", sdkCLIPackageVersion}, "SDK " + sdkCLIViewID + ": " + viewRoot + "\n"},
		{[]string{"sdk", "path", sdkCLIViewID}, viewRoot + "\n"},
		{[]string{"sdk", "list"}, sdkCLIViewID + "\t" + viewRoot + "\n"},
		{[]string{"sdk", "remove", sdkCLIViewID}, "Removed SDK view " + sdkCLIViewID + "; component packages retained\n"},
		{[]string{"sdk", "remove", sdkCLIViewID}, "Removed SDK view " + sdkCLIViewID + "; component packages retained\n"},
		{[]string{"sdk", "list"}, ""},
	} {
		out, err := cliCommand(t, root, url, pidFile, tc.args...).CombinedOutput()
		if err != nil || string(out) != tc.want {
			t.Fatalf("%v: output=%q want=%q error=%v", tc.args, out, tc.want, err)
		}
		sdkCLIAssertNoUpdater(t, root, pidFile, &requests)
	}
	if !reflect.DeepEqual(stateBefore, sdkCLIRead(t, statePath)) {
		t.Fatal("SDK CLI changed installed state")
	}
	for path, before := range metadataBefore {
		if !reflect.DeepEqual(before, sdkCLIRead(t, path)) {
			t.Fatalf("SDK CLI changed original metadata: %s", path)
		}
	}
	if _, err := os.Lstat(filepath.Dir(viewRoot)); !os.IsNotExist(err) {
		t.Fatalf("view not removed: %v", err)
	}
	for _, args := range [][]string{{"sdk", "path", sdkCLIViewID}, {"sdk", "list", "extra"}, {"sdk", "create", "../outside"}} {
		if out, err := cliCommand(t, root, url, pidFile, args...).CombinedOutput(); err == nil {
			t.Fatalf("invalid SDK CLI succeeded: %v: %s", args, out)
		}
		sdkCLIAssertNoUpdater(t, root, pidFile, &requests)
	}
}

func mustSDKCLIState(t *testing.T, m *manager.Manager) manager.State {
	t.Helper()
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSDKCLIReportsAllMissingInstalledComponentsOffline(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing CLI root")
	pidFile := filepath.Join(t.TempDir(), "unexpected-updater.pid")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected network access", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	url := server.URL + "/index.json"
	m := sdkCLIInstalledFixture(t, root, url)
	state := mustSDKCLIState(t, m)
	delete(state.Packages, "ohos-sdk-js")
	delete(state.Packages, "ohos-sdk-previewer")
	sdkCLIWriteState(t, root, state)
	out, err := cliCommand(t, root, url, pidFile, "sdk", "create", sdkCLIPackageVersion).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "ohos-sdk-js@"+sdkCLIPackageVersion) || !strings.Contains(string(out), "ohos-sdk-previewer@"+sdkCLIPackageVersion) {
		t.Fatalf("missing component diagnostic: %s %v", out, err)
	}
	sdkCLIAssertNoUpdater(t, root, pidFile, &requests)
	if _, err := os.Lstat(filepath.Join(root, "sdk", sdkCLIViewID)); !os.IsNotExist(err) {
		t.Fatalf("failed create published a view: %v", err)
	}
}

func TestSDKCLIBlocksNativeRemovalUntilViewRemoved(t *testing.T) {
	root := filepath.Join(t.TempDir(), "native removal CLI root")
	url := "http://127.0.0.1:1/index.json" // disabled automatic updates, no network use
	m := sdkCLIInstalledFixture(t, root, url)
	run := func(args ...string) ([]byte, error) {
		cmd := cliCommand(t, root, url, "", args...)
		cmd.Env = append(cmd.Env, "OHECO_NO_AUTO_UPDATE=1")
		return cmd.CombinedOutput()
	}
	if out, err := run("sdk", "create", sdkCLIPackageVersion); err != nil {
		t.Fatalf("create: %s %v", out, err)
	}
	stateBefore := sdkCLIRead(t, filepath.Join(root, "state", "installed.json"))
	for _, args := range [][]string{
		{"remove", "ohos-sdk-native@" + sdkCLIPackageVersion, "-y"},
		{"remove", "ohos-sdk-native@" + sdkCLIPackageVersion, "--dry-run"},
		{"remove", "ohos-sdk-native", "--all", "-y"},
	} {
		out, err := run(args...)
		if err == nil || !strings.Contains(string(out), "referenced by SDK views") || !strings.Contains(string(out), sdkCLIViewID) {
			t.Fatalf("native removal bypassed CLI protection: %v: %s %v", args, out, err)
		}
		if !reflect.DeepEqual(stateBefore, sdkCLIRead(t, filepath.Join(root, "state", "installed.json"))) {
			t.Fatal("blocked CLI removal changed state")
		}
	}
	if out, err := run("sdk", "remove", sdkCLIViewID); err != nil {
		t.Fatalf("remove view: %s %v", out, err)
	}
	if out, err := run("remove", "ohos-sdk-native@"+sdkCLIPackageVersion, "-y"); err != nil {
		t.Fatalf("unreferenced removal failed: %s %v", out, err)
	}
	state := mustSDKCLIState(t, m)
	if _, exists := state.Packages["ohos-sdk-native"]; exists || len(state.Packages) != 4 {
		t.Fatalf("wrong post-removal state: %+v", state)
	}
}
