package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func testPaths(t *testing.T) paths {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p, err := newPaths(filepath.Join(t.TempDir(), "root with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.prepare(); err != nil {
		t.Fatal(err)
	}
	return p
}
func definition(t *testing.T, p paths, name string) {
	t.Helper()
	data := `{"schema_version":1,"command":["/usr/bin/true"],"restart":"no"}`
	if err := os.WriteFile(filepath.Join(p.services, name+".json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestEnableConcurrentAndUncommittedJournal(t *testing.T) {
	p := testPaths(t)
	names := []string{"a", "b", "c", "d", "e", "f"}
	for _, name := range names {
		definition(t, p, name)
	}
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := p.setEnabled(context.Background(), []string{name}, true); err != nil {
				t.Error(err)
			}
		}(name)
	}
	wg.Wait()
	enabled, err := p.enabled()
	if err != nil || len(enabled) != len(names) {
		t.Fatalf("lost concurrent enables: %v %v", enabled, err)
	}
	if err = os.WriteFile(filepath.Join(p.state, "enabled.json.next"), []byte(`{"services":`), 0600); err != nil {
		t.Fatal(err)
	}
	enabled, err = p.enabled()
	if err != nil || len(enabled) != len(names) {
		t.Fatalf("uncommitted journal replaced committed state: %v %v", enabled, err)
	}
	if err = p.setEnabled(context.Background(), []string{"a"}, false); err != nil {
		t.Fatal(err)
	}
	enabled, err = p.enabled()
	if err != nil || enabled["a"] || len(enabled) != len(names)-1 {
		t.Fatalf("disable after interrupted write: %v %v", enabled, err)
	}
	// Validation is all-or-nothing; a valid first name cannot be enabled if the
	// later definition is missing or invalid.
	if err = p.setEnabled(context.Background(), []string{"a", "missing"}, true); err == nil {
		t.Fatal("enabled missing definition")
	}
	enabled, _ = p.enabled()
	if enabled["a"] {
		t.Fatal("partially committed enable")
	}
}
func TestDefinitionValidationAndCanonicalFilename(t *testing.T) {
	p := testPaths(t)
	definition(t, p, "valid")
	cfg, err := p.config("valid")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkingDirectory != os.Getenv("HOME") || cfg.StopTimeout != "10s" {
		t.Fatalf("defaults: %+v", cfg)
	}
	cases := []string{
		`{"schema_version":1,"command":[]}`,
		`{"schema_version":1,"command":["true"],"typo":1}`,
		`{"schema_version":1,"command":["true"],"working_directory":"relative"}`,
		`{"schema_version":1,"command":["true"],"restart":"sometimes"}`,
		`{"schema_version":1,"command":["true"],"restart_delay":"0s"}`,
		`{"schema_version":1,"command":["true"],"stop_timeout":"24h"}`,
		`{"schema_version":1,"command":["true"],"environment":{"A=B":"bad"}}`,
		`{"schema_version":1,"command":["true"]} {}`,
	}
	for _, data := range cases {
		if err := os.WriteFile(filepath.Join(p.services, "bad.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := p.config("bad"); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
	if err = os.WriteFile(filepath.Join(p.services, "Upper.JSON"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	names, err := p.names()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names, ","), "upper") {
		t.Fatal("noncanonical filename hidden from list")
	}
	if _, err = p.config("upper"); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("canonical filename error: %v", err)
	}
	for _, name := range []string{"A", "../escape", ".state", "", "a/b", strings.Repeat("x", 65)} {
		if validName(name) == nil {
			t.Errorf("accepted name %q", name)
		}
	}
}
func TestRejectSpecialFilesAndSymlinkDirectories(t *testing.T) {
	p := testPaths(t)
	fifo := filepath.Join(p.logs, "fifo.stdout.log")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if f, err := openRegular(fifo, os.O_RDONLY, 0); err == nil {
		f.Close()
		t.Fatal("accepted FIFO")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(p.state, "daemon.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if lock, err := tryLock(link); err == nil {
		lock.Close()
		t.Fatal("followed lock symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("changed target: %q %v", data, err)
	}
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "services")); err != nil {
		t.Fatal(err)
	}
	unsafe, err := newPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.prepare() == nil {
		t.Fatal("accepted symlink service directory")
	}
}
func TestExecutableUsesConfiguredPathAndWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(cwd, "tools", "test-tool")
	if err := os.WriteFile(tool, []byte("#!/usr/bin/zsh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	found, err := serviceExecutable("test-tool", "tools", cwd)
	if err != nil || found != tool {
		t.Fatalf("relative PATH: %q %v", found, err)
	}
	if _, err := serviceExecutable("test-tool", "", t.TempDir()); err == nil {
		t.Fatal("unexpected executable lookup")
	}
}
func TestEnabledJSONRemainsVersioned(t *testing.T) {
	p := testPaths(t)
	definition(t, p, "demo")
	if err := p.setEnabled(context.Background(), []string{"demo"}, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.state, "enabled.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state enabledState
	if err = json.Unmarshal(data, &state); err != nil || state.SchemaVersion != 1 || len(state.Services) != 1 {
		t.Fatalf("state %s: %v", data, err)
	}
}
