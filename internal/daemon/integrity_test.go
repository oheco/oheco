package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStatusJSONDuringStartup(t *testing.T) {
	p := testPaths(t)
	lock, err := tryLock(p.daemonLock())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var out bytes.Buffer
	if err = showStatus(p, "status", []string{"--json"}, &out); !errors.Is(err, ErrStatus) {
		t.Fatalf("startup status: %v", err)
	}
	var view statusView
	if err = json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatalf("startup did not produce JSON: %s %v", out.Bytes(), err)
	}
	if view.Running || view.Services == nil || view.Error == "" {
		t.Fatalf("invalid startup view: %+v", view)
	}
}
func TestQuietStoppedStatusIgnoresDamagedDefinitions(t *testing.T) {
	p := testPaths(t)
	if err := os.WriteFile(filepath.Join(p.state, "enabled.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := showStatus(p, "status", []string{"--quiet"}, &out); !errors.Is(err, ErrStatus) || out.Len() != 0 {
		t.Fatalf("quiet stopped status: %v %q", err, out.String())
	}
}
func TestAutoloadConcurrentInstall(t *testing.T) {
	p := newAutoloadTestPaths(t)
	original := []byte("# retained config without a final newline")
	if err := os.WriteFile(p.zshrc, original, 0640); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Autoload(p.root, "install", io.Discard); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data := readAutoloadTestFile(t, p.zshrc)
	if bytes.Count(data, []byte(autoloadStartMarker)) != 1 {
		t.Fatalf("concurrent installs duplicated blocks: %s", data)
	}
	if err := Autoload(p.root, "remove", io.Discard); err != nil {
		t.Fatal(err)
	}
	if data = readAutoloadTestFile(t, p.zshrc); !bytes.Equal(data, original) {
		t.Fatalf("concurrent writes damaged original: %q", data)
	}
}
func TestAutoloadFailedJournalPreservesConfiguration(t *testing.T) {
	p := newAutoloadTestPaths(t)
	original := []byte("export KEEP=unchanged\n")
	if err := os.WriteFile(p.zshrc, original, 0640); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(p.zdot, "victim")
	if err := os.WriteFile(victim, []byte("do not modify"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, p.zshrc+".oheco-daemon.next"); err != nil {
		t.Fatal(err)
	}
	if err := Autoload(p.root, "install", io.Discard); err == nil {
		t.Fatal("accepted a replaced edit journal")
	}
	if data := readAutoloadTestFile(t, p.zshrc); !bytes.Equal(data, original) {
		t.Fatalf("failed journal truncated zsh config: %q", data)
	}
	if data := readAutoloadTestFile(t, victim); string(data) != "do not modify" {
		t.Fatalf("followed journal symlink: %q", data)
	}
}
func TestAutoloadRejectsDanglingStartupSymlink(t *testing.T) {
	p := newAutoloadTestPaths(t)
	if err := os.Symlink(filepath.Join(p.zdot, "missing"), p.zshrc); err != nil {
		t.Fatal(err)
	}
	if err := Autoload(p.root, "install", io.Discard); err == nil {
		t.Fatal("silently wrote through dangling startup symlink")
	}
	info, err := os.Lstat(p.zshrc)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("changed dangling symlink: %v %v", info, err)
	}
}
