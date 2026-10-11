package manager

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnlinkedCommandDependencyRequiresExplicitRestore(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	if err := f.m.Install(ctx, "runtime@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Unlink(ctx, []string{"runtime"}); err != nil {
		t.Fatal(err)
	}
	before, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "commands, which are unlinked") {
		t.Fatalf("accepted unlinked runtime: %v", err)
	}
	after, err := f.m.LoadState()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed dependency plan changed state")
	}
	if err := f.m.Switch("runtime", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Unlink(ctx, []string{"runtime"}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Switch("app", "1.0.0"); err == nil {
		t.Fatal("switched consumer with unlinked command dependency")
	}
	if err := f.m.Unlink(ctx, []string{"app"}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Switch("runtime", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Switch("app", "1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkReleasesOwnershipRetainsFilesAndRestoresOffline(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.m.Install(ctx, "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(ctx, "demo@2.0.0", true); err != nil {
		t.Fatal(err)
	}
	before, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	links := stateLinks(before)
	if err := f.m.Unlink(ctx, []string{"demo", "missing"}); err == nil {
		t.Fatal("accepted missing package")
	}
	unchanged, err := f.m.LoadState()
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		t.Fatal("partial unlink")
	}
	if err := f.m.Unlink(ctx, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	after, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	want := before.Packages["demo"]
	want.CommandsDisabled = true
	if after.SchemaVersion != 3 || !reflect.DeepEqual(after.Packages["demo"], want) {
		t.Fatalf("receipts changed: %+v", after)
	}
	for name := range links {
		missing(t, filepath.Join(f.m.Root, "bin", name))
	}
	for version, receipt := range want.Versions {
		if err := verifyInstalled(filepath.Join(f.m.Root, "packages/demo", version), receipt.Artifact); err != nil {
			t.Fatal(err)
		}
	}
	// Withdraw the old catalog entry, then install a package using its names.
	replacement := f.idx.Packages[0]
	replacement.Name = "replacement"
	f.idx.Packages = f.idx.Packages[:1]
	f.idx.Packages[0] = replacement
	if err := f.m.Update(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(ctx, "replacement@1.0.0", false); err != nil {
		t.Fatal("disabled owner still reserves commands:", err)
	}
	if err := f.m.Switch("demo", "2.0.0"); err == nil {
		t.Fatal("restored conflicting command ownership")
	}
	if err := f.m.Unlink(ctx, []string{"replacement"}); err != nil {
		t.Fatal(err)
	}
	f.server.Close()
	if err := f.m.Switch("demo", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	restored, err := f.m.LoadState()
	if err != nil || restored.Packages["demo"].CommandsDisabled {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	for name := range stateLinks(restored) {
		if _, err := os.Lstat(filepath.Join(f.m.Root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.m.Unlink(ctx, []string{"oheco"}); err == nil {
		t.Fatal("unlinked manager")
	}
}
