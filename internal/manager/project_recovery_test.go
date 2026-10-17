package manager

import (
	"bytes"
	"context"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInterruptedProjectProfileRecoveryPreservesExternalEdits(t *testing.T) {
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
