package manager

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectProfileRecoveryPreservesExternalEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []string{"before patch", "after patch", "external edit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			original := []byte("// original\n{app:{products:[]}}\n")
			patched := []byte(`{"temporary":true}`)
			external := []byte("// user changed this\n{}")
			current := original
			if mode == "after patch" {
				current = patched
			}
			if mode == "external edit" {
				current = external
			}
			projectTestFile(t, root, "build-profile.json5", current)
			journal, err := projectProfileRecoveryPath(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(journal), 0700); err != nil {
				t.Fatal(err)
			}
			if err = writeJSON(journal, projectProfileBackup{SchemaVersion: 1, Original: original, OriginalSHA256: projectDigest(string(original)), PatchedSHA256: projectDigest(string(patched))}); err != nil {
				t.Fatal(err)
			}
			err = restoreProjectProfile(root)
			if mode == "external edit" {
				if err == nil {
					t.Fatal("overwrote external edit")
				}
				got, _ := os.ReadFile(filepath.Join(root, "build-profile.json5"))
				if !bytes.Equal(got, external) {
					t.Fatal("lost external edit")
				}
				if _, err = os.Stat(journal); err != nil {
					t.Fatal("lost recovery journal", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(root, "build-profile.json5"))
			if !bytes.Equal(got, original) {
				t.Fatal("did not recover exact original bytes")
			}
			if _, err = os.Stat(journal); !os.IsNotExist(err) {
				t.Fatal("kept completed journal", err)
			}
		})
	}
}
func TestProjectDependencyRecoveryPreservesExternalEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []string{"before patch", "after patch", "external edit", "external deletion"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			original := []byte(projectDependencyFixture)
			patched, err := projectOHPMDependencies(original)
			if err != nil {
				t.Fatal(err)
			}
			external := []byte("// user changed root dependencies\n{}\n")
			current := original
			if mode == "after patch" {
				current = patched
			} else if mode == "external edit" {
				current = external
			}
			manifest := filepath.Join(root, "oh-package.json5")
			if mode != "external deletion" {
				projectTestFile(t, root, "oh-package.json5", current)
			}
			if err = saveProjectRecovery(root, "oh-package.json5", original, patched); err != nil {
				t.Fatal(err)
			}
			journal, _ := projectDependencyRecoveryPath(root)
			err = restoreProjectProfile(root) // Existing export/resume entry point.
			if strings.HasPrefix(mode, "external") {
				if err == nil {
					t.Fatal("ignored externally changed manifest")
				}
				got, readErr := os.ReadFile(manifest)
				if mode == "external deletion" {
					if !os.IsNotExist(readErr) {
						t.Fatal("recreated user-deleted manifest", readErr)
					}
				} else if readErr != nil || !bytes.Equal(got, external) {
					t.Fatal("lost user edits", readErr)
				}
				if _, err = os.Stat(journal); err != nil {
					t.Fatal("lost recovery journal", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(manifest)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("did not restore exact dependency bytes", err)
			}
			if _, err = os.Stat(journal); !os.IsNotExist(err) {
				t.Fatal("kept completed journal", err)
			}
		})
	}
}

func TestProjectProfileRecoveryIndependentFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, conflict := range []string{"build-profile.json5", "oh-package.json5"} {
		t.Run(conflict, func(t *testing.T) {
			root := t.TempDir()
			original, patched, external := []byte("// original\n{}\n"), []byte(`{"temporary":true}`), []byte("// user edit\n{keep:true}\n")
			for _, filename := range []string{"build-profile.json5", "oh-package.json5"} {
				current := patched
				if filename == conflict {
					current = external
				}
				projectTestFile(t, root, filename, current)
				if err := saveProjectRecovery(root, filename, original, patched); err != nil {
					t.Fatal(err)
				}
			}
			if err := restoreProjectProfile(root); err == nil || !strings.Contains(err.Error(), conflict+" changed outside") {
				t.Fatal("did not report the external edit", err)
			}
			for _, filename := range []string{"build-profile.json5", "oh-package.json5"} {
				got, err := os.ReadFile(filepath.Join(root, filename))
				want := original
				if filename == conflict {
					want = external
				}
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("did not recover independent file safely", filename, err)
				}
				journal, _ := projectRecoveryPath(root, filename)
				_, err = os.Stat(journal)
				if filename == conflict && err != nil {
					t.Fatal("lost conflict journal", err)
				}
				if filename != conflict && !os.IsNotExist(err) {
					t.Fatal("kept successfully recovered journal", err)
				}
			}
		})
	}
}

func TestProjectDependencyRecoveryPathsAndValidation(t *testing.T) {
	private := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", private)
	root := t.TempDir()
	profile, err := projectProfileRecoveryPath(root)
	if err != nil || profile != filepath.Join(private, "oheco/export-recovery", projectDigest(root)+".json") {
		t.Fatal("changed legacy profile recovery path", profile, err)
	}
	dependency, err := projectDependencyRecoveryPath(root)
	if err != nil || dependency != filepath.Join(private, "oheco/export-recovery", projectDigest(root+"\x00oh-package.json5")+".json") || dependency == profile {
		t.Fatal("dependency journal is not isolated in private configuration", dependency, err)
	}
	for _, filename := range []string{"../outside", filepath.Join(root, "oh-package.json5"), "entry/oh-package.json5", "arbitrary.json5"} {
		if _, err = projectRecoveryPath(root, filename); err == nil {
			t.Fatal("accepted arbitrary recovery filename", filename)
		}
		if err = restoreProjectConfiguration(root, filename); err == nil {
			t.Fatal("accepted arbitrary restore filename", filename)
		}
	}
	original, patched := []byte("// original\n{}"), []byte(`{"temporary":true}`)
	manifest := projectTestFile(t, root, "oh-package.json5", patched)
	for _, corrupt := range []string{"version", "original digest", "patched digest"} {
		t.Run(corrupt, func(t *testing.T) {
			backup := projectProfileBackup{SchemaVersion: 1, Original: original, OriginalSHA256: projectDigest(string(original)), PatchedSHA256: projectDigest(string(patched))}
			switch corrupt {
			case "version":
				backup.SchemaVersion = 2
			case "original digest":
				backup.OriginalSHA256 = strings.Repeat("0", 64)
			case "patched digest":
				backup.PatchedSHA256 = strings.Repeat("0", 64)
			}
			if err = os.MkdirAll(filepath.Dir(dependency), 0700); err != nil {
				t.Fatal(err)
			}
			if err = writeJSON(dependency, backup); err != nil {
				t.Fatal(err)
			}
			if err = restoreProjectProfile(root); err == nil {
				t.Fatal("accepted invalid journal or mismatched patched digest")
			}
			got, readErr := os.ReadFile(manifest)
			if readErr != nil || !bytes.Equal(got, patched) {
				t.Fatal("modified file from invalid journal", readErr)
			}
			if _, err = os.Stat(dependency); err != nil {
				t.Fatal("discarded invalid journal", err)
			}
		})
	}
}

func TestProjectDependencyRecoveryKilledInstall(t *testing.T) {
	const childRoot = "OHECO_TEST_KILLED_DEPENDENCY_ROOT"
	if root := os.Getenv(childRoot); root != "" {
		i := &projectInstaller{m: &Manager{Out: io.Discard}, directory: root}
		i.run = func(ctx context.Context, _ string, _ []string, _ string, _ []string, _ bool) ([]byte, error) {
			fmt.Fprintln(os.Stdout, "dependency-patched")
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if err := i.installDependencies(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	original := []byte(projectDependencyFixture)
	manifest := projectTestFile(t, root, "oh-package.json5", original)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProjectDependencyRecoveryKilledInstall$", "-test.count=1")
	cmd.Env = append(os.Environ(), childRoot+"="+root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "dependency-patched\n" {
		t.Fatalf("child did not reach patched OHPM invocation: %q %v", ready, err)
	}
	patched, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	projectAssertDependencyPatch(t, original, patched)
	journal, _ := projectDependencyRecoveryPath(root)
	if _, err = os.Stat(journal); err != nil {
		t.Fatal("missing journal before kill", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil {
		t.Fatal("child unexpectedly exited successfully", stderr.String())
	}
	stillPatched, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(stillPatched, patched) {
		t.Fatal("SIGKILL did not leave the temporary dependency configuration", err)
	}
	if err = restoreProjectProfile(root); err != nil {
		t.Fatal("export/resume entry point did not recover killed install", err)
	}
	got, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("did not restore exact bytes after SIGKILL", err)
	}
	if _, err = os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("kept completed SIGKILL recovery journal", err)
	}
}

func TestProjectProfileAcceptsDERCertificate(t *testing.T) {
	certificate := projectTestCertificate(t)
	block, _ := pem.Decode(certificate)
	if err := checkProjectProfile(projectTestProfile(t, certificate), "org.oheco.exportfixture", string(bytes.Repeat([]byte("a"), 64)), nil, block.Bytes, time.Now()); err != nil {
		t.Fatal(err)
	}
}
func TestProjectCLIReadableJSAndExecutableLauncher(t *testing.T) {
	root := t.TempDir()
	script := projectTestFile(t, root, "cli.js", []byte("console.log('fixture')"))
	t.Setenv("OHECO_DEVECOCLI", script)
	i := &projectInstaller{tools: projectTools{node: "selected-node"}}
	cli, err := i.findCLI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i.tools.cli = cli
	program, args := i.cliCommand([]string{"auth", "status"})
	if program != "selected-node" || len(args) != 3 || args[0] != script {
		t.Fatal(program, args)
	}
	i.tools.cli = filepath.Join(root, "public-launcher")
	program, args = i.cliCommand([]string{"auth", "login"})
	if program != i.tools.cli || len(args) != 2 {
		t.Fatal(program, args)
	}
}
