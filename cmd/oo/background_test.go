package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/manager"
)

func TestMain(m *testing.M) {
	if os.Getenv("OHECO_TEST_CLI") == "1" {
		if len(os.Args) == 2 && os.Args[1] == "_update" {
			if filename := os.Getenv("OHECO_TEST_BACKGROUND_PID"); filename != "" {
				_ = os.WriteFile(filename, []byte(strconv.Itoa(os.Getpid())), 0600)
			}
		}
		main()
		return
	}
	os.Exit(m.Run())
}

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

func testIndex() []byte {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	a := catalog.Artifact{URL: "https://example.com/demo.tar.gz", SHA256: strings.Repeat("0", 64), Size: 1024, Format: "tar.gz", Binaries: map[string]string{"demo": "bin/demo"}}
	idx := catalog.Index{SchemaVersion: 2, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []catalog.Package{{SchemaVersion: 1, Name: "demo", Description: "background data", Upstream: "https://example.com", Repository: "https://github.com/oheco/demo", Maintainers: []catalog.Maintainer{{GitHub: "alice"}}, License: "MIT", Latest: map[string]string{platform: "1"}, Versions: []catalog.Version{{Version: "1", Artifacts: map[string]catalog.Artifact{platform: a}}}}}}
	data, _ := json.Marshal(idx)
	return data
}

func cliCommand(t *testing.T, root, url, pidFile string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(os.Environ(), "OHECO_TEST_CLI=1", "OHECO_ROOT="+root, "OHECO_INDEX_URL="+url,
		"OHECO_TEST_BACKGROUND_PID="+pidFile, "OHECO_NO_AUTO_UPDATE=0", "GORACE=atexit_sleep_ms=0")
	return cmd
}

func TestDetachedUpdaterSingletonAndParentExit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root with spaces")
	pidFile := filepath.Join(t.TempDir(), "updater.pid")
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-gate:
			w.Header().Set("ETag", "fixture")
			w.Write(testIndex())
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer release()
	url := server.URL + "/index.json"
	// The foreground must finish while the server is deliberately blocked.
	out, err := cliCommand(t, root, url, pidFile, "--version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "oo "+version+" ") {
		t.Fatalf("foreground failed or waited on worker: %s %v", out, err)
	}
	eventually(t, "detached request after parent exit", func() bool { return requests.Load() == 1 })
	pidBefore, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	// All these new invocations must see the inherited lock and avoid spawning
	// a second updater, including when they run concurrently.
	commands := [][]string{{}, {"help"}, {"--help"}, {"-h"}, {"version"}, {"--version"}, {"list"}}
	var group sync.WaitGroup
	for _, args := range commands {
		cmd := cliCommand(t, root, url, pidFile, args...)
		group.Add(1)
		go func() {
			defer group.Done()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("concurrent command: %s %v", out, err)
			}
		}()
	}
	group.Wait()
	pidAfter, _ := os.ReadFile(pidFile)
	if requests.Load() != 1 || string(pidBefore) != string(pidAfter) {
		t.Fatalf("more than one updater: requests=%d pid=%q -> %q", requests.Load(), pidBefore, pidAfter)
	}
	release()
	m, err := manager.New(root, url, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "updated local index", func() bool { idx, err := m.LoadIndex(); return err == nil && len(idx.Packages) == 1 })
	eventually(t, "released worker lock", func() bool {
		lock, err := m.TryUpdateLock()
		if err != nil {
			return false
		}
		lock.Close()
		return true
	})
	// Search reads the new local snapshot; its new worker uses the cooldown.
	out, err = cliCommand(t, root, url, pidFile, "search", "demo").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "background data") || strings.Contains(string(out), "Update now?") {
		t.Fatalf("search result: %s %v", out, err)
	}
	eventually(t, "next worker", func() bool { pid, _ := os.ReadFile(pidFile); return string(pid) != string(pidBefore) })
	eventually(t, "cooldown worker exit", func() bool {
		lock, err := m.TryUpdateLock()
		if err != nil {
			return false
		}
		lock.Close()
		return true
	})
	if requests.Load() != 1 {
		t.Fatalf("cooldown made another request: %d", requests.Load())
	}
}

func TestKilledUpdaterReleasesLock(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "worker.pid")
	var requests atomic.Int32
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-gate:
			w.Write(testIndex())
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer release()
	url := server.URL + "/index.json"
	if out, err := cliCommand(t, root, url, pidFile, "help").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	eventually(t, "worker request", func() bool { return requests.Load() == 1 })
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	process.Release()
	m, err := manager.New(root, url, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "kernel releasing killed worker lock", func() bool {
		lock, err := m.TryUpdateLock()
		if err != nil {
			return false
		}
		lock.Close()
		return true
	})
	// Discard the failed attempt's cooldown so this test can immediately retry.
	if err := os.Remove(filepath.Join(root, "index", "http.json")); err != nil {
		t.Fatal(err)
	}
	if out, err := cliCommand(t, root, url, pidFile, "list").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	eventually(t, "replacement worker", func() bool { return requests.Load() == 2 })
	release()
	eventually(t, "replacement worker exit", func() bool {
		lock, err := m.TryUpdateLock()
		if err != nil {
			return false
		}
		lock.Close()
		return true
	})
}

func TestAutoUpdateOptOutAndExplicitUpdate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(testIndex()) }))
	defer server.Close()
	for _, args := range [][]string{{"--version"}, {"update"}} {
		cmd := cliCommand(t, root, server.URL, "", args...)
		cmd.Env = append(cmd.Env, "OHECO_NO_AUTO_UPDATE=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
		if args[0] == "--version" {
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("opt-out created background state")
			}
		} else if !strings.Contains(string(out), "Updated index") {
			t.Fatalf("explicit update failed: %s", out)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("unexpected requests: %d", requests.Load())
	}
}
