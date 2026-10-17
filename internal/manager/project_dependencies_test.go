package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const projectDependencyFixture = `// Blender root configuration, including ordinary OHPM development dependencies.
{
  name: 'blender',
  version: '4.5.3',
  modelVersion: '5.0.0',
  dependencies: {
    '@ohos/runtime': 'file:./libs/runtime.har',
    '@ohos/hvigor': 'file:./libs/ordinary-dependency.har',
  },
  devDependencies: {
    '@ohos/hvigor': '7.26.8',
    '@ohos/hvigor-ohos-plugin': '7.26.8',
    '@ohos/hypium': '1.0.21',
    '@ohos/hamock': '1.0.0',
    '@ohos/hvigor-ohos-plugin-extra': '1.2.3',
  },
  overrides: {'@ohos/hvigor-ohos-plugin': 'keep-this-field'},
  metadata: {sequence: 9007199254740993, nested: [true, null, 'unchanged']},
}
`

func projectAssertDependencyPatch(t *testing.T, original, patched []byte) {
	t.Helper()
	var before, after map[string]json.RawMessage
	if err := decodeProjectJSON(original, &before); err != nil {
		t.Fatal(err)
	}
	if err := decodeProjectJSON(patched, &after); err != nil {
		t.Fatal(err)
	}
	var beforeDev, afterDev map[string]json.RawMessage
	if err := decodeProjectJSON(before["devDependencies"], &beforeDev); err != nil {
		t.Fatal(err)
	}
	if err := decodeProjectJSON(after["devDependencies"], &afterDev); err != nil {
		t.Fatal(err)
	}
	delete(beforeDev, "@ohos/hvigor")
	delete(beforeDev, "@ohos/hvigor-ohos-plugin")
	if !reflect.DeepEqual(beforeDev, afterDev) {
		t.Fatalf("changed other development dependencies: got %s", after["devDependencies"])
	}
	delete(before, "devDependencies")
	delete(after, "devDependencies")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("changed other root fields: before %s, after %s", before, after)
	}
}

func TestProjectDependenciesRestoreAfterCommand(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := t.TempDir()
			original := []byte(projectDependencyFixture)
			manifest := projectTestFile(t, root, "oh-package.json5", original)
			module := []byte(`// Module dependencies must never be patched.
{dependencies:{'@ohos/runtime':'file:../libs/runtime.har'},devDependencies:{'@ohos/hvigor':'module-owned','@ohos/hvigor-ohos-plugin':'module-owned','@ohos/hypium':'1.0.21'}}`)
			modulePath := projectTestFile(t, root, "entry/oh-package.json5", module)
			profileOriginal, profilePatched := []byte("// profile\n{}\n"), []byte(`{"temporary":true}`)
			profile := projectTestFile(t, root, "build-profile.json5", profilePatched)
			if err := saveProjectRecovery(root, "build-profile.json5", profileOriginal, profilePatched); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commandFailure := errors.New("OHPM fixture failure")
			i := &projectInstaller{m: &Manager{Out: io.Discard}, directory: root, tools: projectTools{clt: filepath.Join(root, "selected CLT"), node: "not-the-CLT-node", env: []string{"FIXTURE=kept"}}}
			calls := 0
			i.run = func(commandCtx context.Context, program string, args []string, dir string, env []string, interactive bool) ([]byte, error) {
				calls++
				wantArgs := []string{filepath.Join(i.tools.clt, "ohpm/bin/pm-cli.js"), "install", "--all"}
				if program != filepath.Join(i.tools.clt, "node/bin/node") || !reflect.DeepEqual(args, wantArgs) || dir != root || !reflect.DeepEqual(env, i.tools.env) || interactive {
					t.Fatalf("unexpected public OHPM command: %q %q dir=%q env=%q interactive=%v", program, args, dir, env, interactive)
				}
				patched, err := os.ReadFile(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(patched, original) {
					t.Fatal("OHPM saw the unpatched configuration")
				}
				projectAssertDependencyPatch(t, original, patched)
				gotModule, err := os.ReadFile(modulePath)
				if err != nil || !bytes.Equal(gotModule, module) {
					t.Fatal("module configuration changed", err)
				}
				journal, err := projectDependencyRecoveryPath(root)
				if err != nil {
					t.Fatal(err)
				}
				var backup projectProfileBackup
				if err = readJSON(journal, &backup); err != nil {
					t.Fatal("journal was not committed before OHPM", err)
				}
				if backup.SchemaVersion != 1 || !bytes.Equal(backup.Original, original) || backup.OriginalSHA256 != projectDigest(string(original)) || backup.PatchedSHA256 != projectDigest(string(patched)) {
					t.Fatal("journal does not describe the exact original and patched bytes")
				}
				switch outcome {
				case "failure":
					return []byte("fixture stderr"), commandFailure
				case "cancellation":
					cancel()
					<-commandCtx.Done()
					return nil, commandCtx.Err()
				}
				return nil, nil
			}
			err := i.installDependencies(ctx)
			switch outcome {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "failure":
				if !errors.Is(err, commandFailure) {
					t.Fatal("lost command failure", err)
				}
			case "cancellation":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation", err)
				}
			}
			if calls != 1 {
				t.Fatal("unexpected command count", calls)
			}
			got, err := os.ReadFile(manifest)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("did not restore exact root bytes", err)
			}
			journal, _ := projectDependencyRecoveryPath(root)
			if _, err = os.Stat(journal); !os.IsNotExist(err) {
				t.Fatal("kept completed dependency journal", err)
			}
			gotProfile, err := os.ReadFile(profile)
			if err != nil || !bytes.Equal(gotProfile, profilePatched) {
				t.Fatal("dependency cleanup restored the active build profile", err)
			}
			if err = restoreProjectProfile(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectDependenciesUnchangedRoot(t *testing.T) {
	for name, original := range map[string][]byte{
		"missing":                  nil,
		"no development section":   []byte("// unchanged\n{dependencies:{'@ohos/hvigor':'keep'}}\n"),
		"ordinary devDependencies": []byte("// unchanged\n{devDependencies:{'@ohos/hypium':'1.0.21','@ohos/hvigor-extra':'keep'}}\n"),
		"null development section": []byte("{devDependencies:null}"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", "") // Unchanged/missing roots need no journal.
			root := t.TempDir()
			manifest := filepath.Join(root, "oh-package.json5")
			if original != nil {
				projectTestFile(t, root, "oh-package.json5", original)
			}
			i := &projectInstaller{m: &Manager{Out: io.Discard}, directory: root, tools: projectTools{clt: "selected-clt"}}
			calls := 0
			i.run = func(_ context.Context, program string, args []string, _ string, _ []string, _ bool) ([]byte, error) {
				calls++
				if program != "selected-clt/node/bin/node" || !reflect.DeepEqual(args, []string{"selected-clt/ohpm/bin/pm-cli.js", "install", "--all"}) {
					t.Fatal(program, args)
				}
				return nil, nil
			}
			if err := i.installDependencies(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(manifest)
			if original == nil {
				if !os.IsNotExist(err) {
					t.Fatal("created missing root manifest", err)
				}
			} else if err != nil || !bytes.Equal(got, original) {
				t.Fatal("rewrote unchanged manifest", err)
			}
			if calls != 1 {
				t.Fatal("OHPM was not run exactly once", calls)
			}
		})
	}
}

func TestProjectDependenciesSingleToolKey(t *testing.T) {
	for _, key := range []string{"@ohos/hvigor", "@ohos/hvigor-ohos-plugin"} {
		t.Run(key, func(t *testing.T) {
			original := []byte(`{devDependencies:{'` + key + `':'7.26.8','@ohos/hypium':'1.0.21'},dependencies:{runtime:'file:./runtime.har'}}`)
			patched, err := projectOHPMDependencies(original)
			if err != nil || patched == nil {
				t.Fatal("did not remove the single CLT tool dependency", err)
			}
			projectAssertDependencyPatch(t, original, patched)
		})
	}
}

func TestProjectDependenciesPreservesExternalEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	manifest := projectTestFile(t, root, "oh-package.json5", []byte(projectDependencyFixture))
	external := []byte("// user changed this during OHPM\n{devDependencies:{'@ohos/hypium':'new-version'}}\n")
	commandFailure := errors.New("OHPM failed after external edit")
	i := &projectInstaller{m: &Manager{Out: io.Discard}, directory: root}
	i.run = func(_ context.Context, _ string, _ []string, _ string, _ []string, _ bool) ([]byte, error) {
		if err := os.WriteFile(manifest, external, 0600); err != nil {
			t.Fatal(err)
		}
		return nil, commandFailure
	}
	err := i.installDependencies(context.Background())
	if !errors.Is(err, commandFailure) || !strings.Contains(err.Error(), "changed outside this installation") {
		t.Fatal("lost command failure or recovery conflict", err)
	}
	got, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(got, external) {
		t.Fatal("overwrote user changes", err)
	}
	journal, _ := projectDependencyRecoveryPath(root)
	if _, err = os.Stat(journal); err != nil {
		t.Fatal("lost conflict recovery journal", err)
	}
}

func TestProjectDependenciesJournalFailureLeavesRootIntact(t *testing.T) {
	root := t.TempDir()
	original := []byte(projectDependencyFixture)
	manifest := projectTestFile(t, root, "oh-package.json5", original)
	privateFile := projectTestFile(t, t.TempDir(), "not-a-directory", []byte("fixture"))
	t.Setenv("XDG_CONFIG_HOME", privateFile)
	i := &projectInstaller{m: &Manager{Out: io.Discard}, directory: root}
	i.run = func(_ context.Context, _ string, _ []string, _ string, _ []string, _ bool) ([]byte, error) {
		t.Fatal("ran OHPM without committing the recovery journal")
		return nil, nil
	}
	if err := i.installDependencies(context.Background()); err == nil {
		t.Fatal("ignored journal failure")
	}
	got, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("changed root before saving its journal", err)
	}
}
