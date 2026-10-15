package daemon

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type autoloadTestPaths struct {
	home   string
	zdot   string
	root   string
	zshrc  string
	stable string
}

func newAutoloadTestPaths(t *testing.T) autoloadTestPaths {
	t.Helper()
	home, zdot, root := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", zdot)
	paths := autoloadTestPaths{home: home, zdot: zdot, root: root, zshrc: filepath.Join(zdot, ".zshrc"), stable: filepath.Join(root, "bin", "oo")}
	writeAutoloadTestExecutable(t, paths.stable, "#!/usr/bin/zsh\nexit 0\n")
	return paths
}

func writeAutoloadTestExecutable(t *testing.T, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func readAutoloadTestFile(t *testing.T, filename string) []byte {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAutoloadInstallRemovePreservesContents(t *testing.T) {
	for _, original := range []string{"", "export KEEP='unchanged'", "export KEEP='unchanged'\n", "# keep CRLF\r\n", "\n\n"} {
		t.Run(strings.ReplaceAll(original, "\n", "_newline_"), func(t *testing.T) {
			paths := newAutoloadTestPaths(t)
			if err := os.WriteFile(paths.zshrc, []byte(original), 0640); err != nil {
				t.Fatal(err)
			}
			if err := Autoload(paths.root, "", io.Discard); err != nil {
				t.Fatal(err)
			}
			installed := readAutoloadTestFile(t, paths.zshrc)
			if !bytes.HasPrefix(installed, []byte(original)) {
				t.Fatalf("original prefix changed: %q", installed)
			}
			if bytes.Count(installed, []byte(autoloadStartMarker)) != 1 || bytes.Count(installed, []byte(autoloadEndMarker)) != 1 {
				t.Fatalf("unexpected block markers: %q", installed)
			}
			if err := Autoload(paths.root, "install", io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := readAutoloadTestFile(t, paths.zshrc); !bytes.Equal(got, installed) {
				t.Fatalf("install is not idempotent:\n%q\n%q", installed, got)
			}
			if err := Autoload(paths.root, "remove", io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := string(readAutoloadTestFile(t, paths.zshrc)); got != original {
				t.Fatalf("remove did not restore original bytes: got %q, want %q", got, original)
			}
			if err := Autoload(paths.root, "remove", io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAutoloadReplacesOnlyOwnBlock(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	original := "# before\n" + autoloadStartMarker + "\nold startup command\n" + autoloadEndMarker + "\n# after without newline"
	if err := os.WriteFile(paths.zshrc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Autoload(paths.root, "install", io.Discard); err != nil {
		t.Fatal(err)
	}
	want := "# before\n" + autoloadScript(paths.root, false) + "# after without newline"
	if got := string(readAutoloadTestFile(t, paths.zshrc)); got != want {
		t.Fatalf("replacement changed unrelated bytes: got %q, want %q", got, want)
	}
	if err := Autoload(paths.root, "remove", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := string(readAutoloadTestFile(t, paths.zshrc)); got != "# before\n# after without newline" {
		t.Fatalf("unexpected remainder: %q", got)
	}
}

func TestAutoloadPreservesUnterminatedClosingMarker(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	original := "# before\n" + autoloadStartMarker + "\nold\n" + autoloadEndMarker
	if err := os.WriteFile(paths.zshrc, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Autoload(paths.root, "install", io.Discard); err != nil {
		t.Fatal(err)
	}
	got := readAutoloadTestFile(t, paths.zshrc)
	if bytes.HasSuffix(got, []byte("\n")) {
		t.Fatalf("added a trailing newline to the existing block: %q", got)
	}
	if err := Autoload(paths.root, "remove", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := string(readAutoloadTestFile(t, paths.zshrc)); got != "# before\n" {
		t.Fatalf("unexpected remainder: %q", got)
	}
}

func TestAutoloadZdotdirAndHomeFallback(t *testing.T) {
	for _, useHome := range []bool{false, true} {
		t.Run(map[bool]string{false: "ZDOTDIR", true: "HOME"}[useHome], func(t *testing.T) {
			paths := newAutoloadTestPaths(t)
			wantFile := paths.zshrc
			untouched := filepath.Join(paths.home, ".zshrc")
			if useHome {
				t.Setenv("ZDOTDIR", "")
				wantFile, untouched = untouched, wantFile
			}
			if err := os.WriteFile(untouched, []byte("leave alone"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Autoload(paths.root, "install", io.Discard); err != nil {
				t.Fatal(err)
			}
			if got := readAutoloadTestFile(t, wantFile); !bytes.Contains(got, []byte(autoloadStartMarker)) {
				t.Fatalf("startup block missing: %q", got)
			}
			if got := string(readAutoloadTestFile(t, untouched)); got != "leave alone" {
				t.Fatalf("wrong startup file was changed: %q", got)
			}
		})
	}
}

func TestAutoloadPreservesSymlinkAndTargetPermissions(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	target := filepath.Join(t.TempDir(), "linked configuration")
	original := []byte("# linked target without newline")
	if err := os.WriteFile(target, original, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.zshrc); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"install", "remove"} {
		if err := Autoload(paths.root, mode, io.Discard); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(paths.zshrc)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink replaced: info=%v err=%v", info, err)
		}
		link, err := os.Readlink(paths.zshrc)
		if err != nil || link != target {
			t.Fatalf("symlink changed: target=%q err=%v", link, err)
		}
	}
	if got := readAutoloadTestFile(t, target); !bytes.Equal(got, original) {
		t.Fatalf("linked target did not round-trip: %q", got)
	}
	after, err := os.Stat(target)
	if err != nil || before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("target permissions changed: before=%v after=%v err=%v", before, after, err)
	}
}

func TestAutoloadRejectsMalformedBlocksWithoutWriting(t *testing.T) {
	valid := autoloadStartMarker + "\nold\n" + autoloadEndMarker + "\n"
	cases := map[string]string{
		"missing end":          "# keep\n" + autoloadStartMarker + "\nold",
		"missing start":        "# keep\n" + autoloadEndMarker + "\n",
		"reversed":             autoloadEndMarker + "\n" + autoloadStartMarker + "\n",
		"duplicate":            valid + valid,
		"nested":               autoloadStartMarker + "\n" + valid + autoloadEndMarker + "\n",
		"trailing marker text": autoloadStartMarker + " extra\nold\n" + autoloadEndMarker + "\n",
		"indented marker":      "  " + valid,
		"truncated end marker": autoloadStartMarker + "\nold\n# <<< oheco daemon autoload <<\n",
		"bad separator":        autoloadStartMarker + "\n" + autoloadSeparatorMarker + "\nold\n" + autoloadEndMarker + "\n",
		"duplicate separator":  "# keep\n" + autoloadStartMarker + "\n" + autoloadSeparatorMarker + "\n" + autoloadSeparatorMarker + "\n" + autoloadEndMarker + "\n",
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			paths := newAutoloadTestPaths(t)
			if err := os.WriteFile(paths.zshrc, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"install", "remove"} {
				if err := Autoload(paths.root, mode, io.Discard); err == nil {
					t.Fatalf("%s accepted malformed blocks", mode)
				}
				if got := string(readAutoloadTestFile(t, paths.zshrc)); got != original {
					t.Fatalf("%s changed malformed file: got %q, want %q", mode, got, original)
				}
			}
		})
	}
}

func TestAutoloadMissingStableExecutable(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	if err := Autoload(paths.root, "install", io.Discard); err != nil {
		t.Fatal(err)
	}
	installed := readAutoloadTestFile(t, paths.zshrc)
	if err := os.Remove(paths.stable); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"install", "print"} {
		var out bytes.Buffer
		err := Autoload(paths.root, mode, &out)
		if err == nil || !strings.Contains(err.Error(), "stable oo executable") || !strings.Contains(err.Error(), paths.stable) {
			t.Fatalf("%s: expected clear missing executable error, got %v", mode, err)
		}
		if out.Len() != 0 {
			t.Fatalf("%s printed script despite missing executable", mode)
		}
		if got := readAutoloadTestFile(t, paths.zshrc); !bytes.Equal(got, installed) {
			t.Fatalf("%s modified startup file on error", mode)
		}
	}
	if err := Autoload(paths.root, "remove", io.Discard); err != nil {
		t.Fatalf("remove requires a missing executable: %v", err)
	}
	if got := readAutoloadTestFile(t, paths.zshrc); len(got) != 0 {
		t.Fatalf("remove left startup block: %q", got)
	}
}

func TestAutoloadPrintDoesNotEditStartupFile(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	original := []byte("# keep without newline")
	if err := os.WriteFile(paths.zshrc, original, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Autoload(paths.root, "print", &out); err != nil {
		t.Fatal(err)
	}
	if got := readAutoloadTestFile(t, paths.zshrc); !bytes.Equal(got, original) {
		t.Fatalf("print modified startup file: %q", got)
	}
	if got := out.String(); got != autoloadScript(paths.root, false) {
		t.Fatalf("unexpected script: %q", got)
	}
	if strings.Contains(out.String(), "status") || strings.Count(out.String(), " daemon </dev/null") != 1 {
		t.Fatalf("expected one daemon invocation without status: %q", out.String())
	}
	if !strings.Contains(out.String(), " daemon </dev/null >/dev/null 2>&1 &!\n") {
		t.Fatalf("daemon is not detached with redirected streams: %q", out.String())
	}
}

func TestAutoloadZshQuotesArbitraryRoot(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable")
	}
	root := filepath.Join(t.TempDir(), "root space\n# >>> oheco daemon autoload >>>\n'\"$`\\\t\r;()")
	stable := filepath.Join(root, "bin", "oo")
	resultFile := filepath.Join(t.TempDir(), "invocation")
	writeAutoloadTestExecutable(t, stable, "#!"+zsh+"\nprintf '%s\\000' \"$OHECO_ROOT\" \"$0\" \"$@\" > \"$OHECO_AUTOLOAD_TEST_RESULT\"\n")
	t.Setenv("OHECO_ROOT", "caller-root")
	t.Setenv("OHECO_AUTOLOAD_TEST_RESULT", resultFile)
	var out bytes.Buffer
	if err := Autoload(root, "print", &out); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(zsh, "-df", "-c", out.String()+"printf '%s' \"$OHECO_ROOT\"\n")
	got, err := cmd.CombinedOutput()
	if err != nil || string(got) != "caller-root" {
		t.Fatalf("generated block failed or leaked command environment: output=%q err=%v", got, err)
	}
	want := []byte(root + "\x00" + stable + "\x00daemon\x00")
	deadline := time.Now().Add(3 * time.Second)
	var invocation []byte
	for time.Now().Before(deadline) {
		invocation, err = os.ReadFile(resultFile)
		if err == nil && bytes.Equal(invocation, want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bytes.Equal(invocation, want) {
		t.Fatalf("root or executable quoting changed argv/environment: got %q, want %q, err=%v", invocation, want, err)
	}
	// Newlines and marker text within the quoted path must remain harmless
	// when installing or replacing our own generated block.
	if err := Autoload(root, "install", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Autoload(root, "install", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Autoload(root, "remove", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := readAutoloadTestFile(t, paths.zshrc); len(got) != 0 {
		t.Fatalf("quoted-path block did not round-trip: %q", got)
	}
}

type autoloadFailWriter struct{ err error }

func (w autoloadFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAutoloadValidationAndPrintErrors(t *testing.T) {
	paths := newAutoloadTestPaths(t)
	for _, mode := range []string{"invalid", "--remove"} {
		if err := Autoload(paths.root, mode, io.Discard); err == nil {
			t.Fatalf("accepted invalid mode %q", mode)
		}
	}
	for _, root := range []string{"", "relative/root", "/bad\x00root"} {
		if err := Autoload(root, "install", io.Discard); err == nil {
			t.Fatalf("accepted invalid root %q", root)
		}
	}
	wantErr := errors.New("writer failed")
	if err := Autoload(paths.root, "print", autoloadFailWriter{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("lost print error: %v", err)
	}
	if err := Autoload(paths.root, "print", nil); err != nil {
		t.Fatalf("nil writer failed: %v", err)
	}
	if err := Autoload(paths.root, "remove", io.Discard); err != nil {
		t.Fatalf("removing absent startup file failed: %v", err)
	}
	if _, err := os.Stat(paths.zshrc); !os.IsNotExist(err) {
		t.Fatalf("remove created an absent startup file: %v", err)
	}
}
