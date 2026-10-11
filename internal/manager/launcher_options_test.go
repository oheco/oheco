package manager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

func TestInactiveLauncherOptionsPromoteStateAndActivateOffline(t *testing.T) {
	f := setup(t)
	f.idx.SchemaVersion = 6
	p := &f.idx.Packages[0]
	p.SchemaVersion = 6
	a := p.Versions[0].Artifacts[f.m.Platform]
	a.LauncherOptions = map[string]catalog.LauncherOptions{}
	for name := range a.Binaries {
		a.Launchers = append(a.Launchers, name)
		a.LauncherOptions[name] = catalog.LauncherOptions{Arguments: []string{"--package-root=${PACKAGE_ROOT}"}}
	}
	p.Versions[0].Artifacts[f.m.Platform] = a
	ctx := context.Background()
	if err := f.m.Update(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(ctx, "demo@1.0.0", true); err != nil {
		t.Fatal(err)
	}
	state, err := f.m.LoadState()
	if err != nil || state.SchemaVersion != 3 || state.Packages["demo"].Active != "" {
		t.Fatalf("inactive state: %+v %v", state, err)
	}
	for name := range a.Binaries {
		missing(t, filepath.Join(f.m.Root, "bin", name))
		if _, err := os.Lstat(filepath.Join(f.m.Root, "bin", name+"@1.0.0")); err != nil {
			t.Fatal(err)
		}
	}
	f.server.Close()
	if err := f.m.Switch("demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherPrivateRuntimeArgumentsAndRelocation(t *testing.T) {
	f := setup(t)
	f.idx.SchemaVersion = 6
	p := &f.idx.Packages[0]
	p.SchemaVersion = 6
	data := archive(t,
		entry{name: "node/bin/node", content: "#!/bin/sh\nprintf 'private-node\\n'\n"},
		entry{name: "sdk/resource", content: "sdk"},
		entry{name: "tools/run", content: "#!/bin/sh\nprintf '%s\\n' \"${0##*/}\" \"$NODE_HOME\" \"$DEVECO_SDK_HOME\"\nnode\nprintf '%s\\n' \"$@\"\n"},
	)
	f.files["/runtime.tar.gz"] = data
	a := artifact(data, f.server.URL+"/runtime.tar.gz", map[string]string{"tool": "tools/run"})
	a.Launchers = []string{"tool"}
	a.LauncherOptions = map[string]catalog.LauncherOptions{"tool": {
		Environment: map[string]string{"NODE_HOME": "node/bin", "DEVECO_SDK_HOME": "sdk"},
		Path:        []string{"node/bin"}, Interpreter: "sh",
		Arguments: []string{"--sysroot=${PACKAGE_ROOT}/sdk", "$(touch SHOULD_NOT_EXIST);`echo bad`"},
	}}
	p.Versions = p.Versions[:1]
	p.Latest[f.m.Platform] = p.Versions[0].Version
	p.Versions[0].Artifacts[f.m.Platform] = a
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(context.Background(), "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	state, err := f.m.LoadState()
	if err != nil || state.SchemaVersion != 3 {
		t.Fatalf("state: %+v %v", state, err)
	}
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "node"), []byte("#!/bin/sh\necho WRONG-NODE\n"), 0755); err != nil {
		t.Fatal(err)
	}
	run := func(root string) {
		t.Helper()
		pkg := filepath.Join(root, "packages/demo/1.0.0")
		command := exec.Command(filepath.Join(root, "bin/tool@1.0.0"), "a b", "'quoted'", "")
		command.Dir = t.TempDir()
		command.Env = append(os.Environ(), "PATH="+foreign+":/usr/bin:/bin", "NODE_HOME=wrong", "DEVECO_SDK_HOME=wrong")
		out, err := command.CombinedOutput()
		want := strings.Join([]string{"run", pkg + "/node/bin", pkg + "/sdk", "private-node", "--sysroot=" + pkg + "/sdk", "$(touch SHOULD_NOT_EXIST);`echo bad`", "a b", "'quoted'", ""}, "\n") + "\n"
		if err != nil || string(out) != want {
			t.Fatalf("%q %v; want %q", out, err, want)
		}
		if _, err := os.Stat(filepath.Join(command.Dir, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
			t.Fatal("argument executed shell code")
		}
	}
	run(f.m.Root)
	moved := filepath.Join(t.TempDir(), "moved root ' $dollar")
	if err := os.Rename(f.m.Root, moved); err != nil {
		t.Fatal(err)
	}
	f.m.Root = moved
	run(moved)
	if _, err := os.Stat(filepath.Join(moved, "bin/node")); !os.IsNotExist(err) {
		t.Fatal("private runtime was exported")
	}
	file := filepath.Join(moved, "packages/demo/1.0.0/.oo-launchers/tool")
	if err := os.WriteFile(file, launcherScript("tools/run"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Switch("demo", "1.0.0"); err == nil {
		t.Fatal("accepted removed private runtime configuration")
	}
}
