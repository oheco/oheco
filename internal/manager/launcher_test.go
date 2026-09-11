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

func launcherFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	f.idx.SchemaVersion = catalog.SchemaVersion
	p := &f.idx.Packages[0]
	p.SchemaVersion = catalog.SchemaVersion
	for i, v := range p.Versions {
		rel := "lib/space ' $dollar/ld.lld"
		data := archive(t, entry{name: rel, content: "#!/bin/sh\nprintf '%s\\n' \"${0##*/}\" '" + v.Version + "' \"$@\"\n"})
		url := "/launcher-" + v.Version + ".tar.gz"
		f.files[url] = data
		a := artifact(data, f.server.URL+url, map[string]string{"ld.lld": rel})
		a.Launchers = []string{"ld.lld"}
		p.Versions[i].Artifacts[f.m.Platform] = a
	}
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func runLauncher(t *testing.T, root, name, version string) {
	t.Helper()
	args := []string{"a b", "'quoted'", "$(echo bad);`echo bad`", ""}
	out, err := exec.Command(filepath.Join(root, "bin", name), args...).CombinedOutput()
	want := "ld.lld\n" + version + "\n" + strings.Join(args, "\n") + "\n"
	if err != nil || string(out) != want {
		t.Fatalf("launcher %s: %q %v; want %q", name, out, err, want)
	}
}

func TestLaunchersPreserveNameArgumentsAndRelocation(t *testing.T) {
	f := launcherFixture(t)
	ctx := context.Background()
	if err := f.m.Install(ctx, "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(ctx, "demo@2.0.0", true); err != nil {
		t.Fatal(err)
	}
	linkIs(t, f.m.Root, "ld.lld@1.0.0", "../packages/demo/1.0.0/.oo-launchers/ld.lld")
	runLauncher(t, f.m.Root, "ld.lld", "1.0.0")
	runLauncher(t, f.m.Root, "ld.lld@2.0.0", "2.0.0")
	requests := f.requests
	if err := f.m.Install(ctx, "demo@2.0.0", true); err != nil {
		t.Fatal(err)
	}
	if f.requests != requests {
		t.Fatal("idempotent launcher install downloaded again")
	}
	newRoot := filepath.Join(t.TempDir(), "moved root ' $dollar")
	if err := os.Rename(f.m.Root, newRoot); err != nil {
		t.Fatal(err)
	}
	f.m.Root = newRoot
	f.server.Close()
	if err := f.m.Switch("demo", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	runLauncher(t, newRoot, "ld.lld", "2.0.0")
	runLauncher(t, newRoot, "ld.lld@1.0.0", "1.0.0")
	if err := f.m.Remove("demo", false); err != nil {
		t.Fatal(err)
	}
	missing(t, filepath.Join(newRoot, "bin/ld.lld"))
	missing(t, filepath.Join(newRoot, "packages/demo/2.0.0/.oo-launchers"))
	runLauncher(t, newRoot, "ld.lld@1.0.0", "1.0.0")
	if err := f.m.Remove("demo", true); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherRecoveryAndTampering(t *testing.T) {
	for _, committed := range []bool{false, true} {
		f := launcherFixture(t)
		for _, v := range []string{"1.0.0", "2.0.0"} {
			if err := f.m.Install(context.Background(), "demo@"+v, v == "2.0.0"); err != nil {
				t.Fatal(err)
			}
		}
		before, err := f.m.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		after := cloneState(before)
		p := after.Packages["demo"]
		p.Active = "2.0.0"
		after.Packages["demo"] = p
		if err := writeJSON(filepath.Join(f.m.Root, "state/transaction.json"), transaction{Before: before, After: after, Committed: committed}); err != nil {
			t.Fatal(err)
		}
		if err := f.m.applyLinks(stateLinks(before), stateLinks(after)); err != nil {
			t.Fatal(err)
		}
		if err := f.m.Recover(); err != nil {
			t.Fatal(err)
		}
		want := "1.0.0"
		if committed {
			want = "2.0.0"
		}
		runLauncher(t, f.m.Root, "ld.lld", want)
		file := filepath.Join(f.m.Root, "packages/demo/1.0.0/.oo-launchers/ld.lld")
		if err := os.WriteFile(file, []byte("#!/bin/sh\necho tampered\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := f.m.Switch("demo", "1.0.0"); err == nil {
			t.Fatal("accepted modified launcher")
		}
		if err := f.m.Install(context.Background(), "demo@1.0.0", false); err == nil {
			t.Fatal("reinstalled modified launcher")
		}
	}
}
