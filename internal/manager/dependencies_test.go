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

	"github.com/oheco/oheco/internal/catalog"
)

func addDependency(t *testing.T, f *fixture, name, version string, deps ...catalog.Dependency) {
	t.Helper()
	f.idx.SchemaVersion = catalog.SchemaVersion
	for i := range f.idx.Packages {
		p := &f.idx.Packages[i]
		if p.Name != name {
			continue
		}
		p.SchemaVersion = 5
		for j := range p.Versions {
			if p.Versions[j].Version == version {
				p.Versions[j].Dependencies = deps
				return
			}
		}
	}
	t.Fatalf("fixture version not found: %s@%s", name, version)
}

func dependencyFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	f.add(t, "runtime", "1.0.0", map[string]string{"runtime": "bin/runtime"})
	f.add(t, "runtime", "2.0.0", map[string]string{"runtime": "bin/runtime"})
	f.add(t, "app", "1.0.0", map[string]string{"app": "bin/app"})
	f.add(t, "other", "1.0.0", map[string]string{"other": "bin/other"})
	dep := catalog.Dependency{Name: "runtime", Constraint: ">=1.0.0 <2.0.0"}
	addDependency(t, f, "app", "1.0.0", dep)
	addDependency(t, f, "other", "1.0.0", dep)
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNativeDependenciesRequireConsentBeforeDownload(t *testing.T) {
	for _, answer := range []string{"n\n", ""} {
		t.Run(answer, func(t *testing.T) {
			f := dependencyFixture(t)
			f.m.In = strings.NewReader(answer)
			requests := f.requests
			if err := f.m.Install(context.Background(), "app", false); err == nil {
				t.Fatal("installed without consent")
			}
			if f.requests != requests {
				t.Fatal("downloaded before consent")
			}
			s, err := f.m.LoadState()
			if err != nil || len(s.Packages) != 0 {
				t.Fatalf("state changed: %v %v", s, err)
			}
		})
	}
}

func TestNativeBatchSharedDependencyLifecycle(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.InstallMany(ctx, []string{"app", "other"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != 2 || s.Packages["runtime"].Active != "1.0.0" {
		t.Fatalf("wrong selection: %#v", s)
	}
	if !s.Packages["runtime"].Versions["1.0.0"].Automatic || s.Packages["app"].Versions["1.0.0"].Automatic {
		t.Fatal("wrong installation ownership")
	}
	if d := s.Packages["app"].Versions["1.0.0"].Dependencies; len(d) != 1 || d[0].Version != "1.0.0" {
		t.Fatalf("missing binding: %#v", d)
	}
	if err := f.m.RemoveMany(ctx, []string{"runtime"}, RemoveOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "required by") {
		t.Fatalf("removed shared dependency: %v", err)
	}
	out.Reset()
	if err := f.m.RemoveMany(ctx, []string{"app"}, RemoveOptions{Yes: true, AutoRemove: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "still required by other@1.0.0") {
		t.Fatalf("missing shared dependency explanation: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(f.m.Root, "packages", "runtime", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	f.server.Close()
	if err := os.Remove(filepath.Join(f.m.Root, "index", "index.json")); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RemoveMany(ctx, []string{"other"}, RemoveOptions{Yes: true, AutoRemove: true}); err != nil {
		t.Fatal(err)
	}
	s, err = f.m.LoadState()
	if err != nil || len(s.Packages) != 0 {
		t.Fatalf("orphan not removed offline: %#v %v", s, err)
	}
}

func TestNativeExplicitPromotionAndYesDoesNotAutoremove(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RemoveMany(ctx, []string{"app"}, RemoveOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	s, _ := f.m.LoadState()
	if _, ok := s.Packages["runtime"]; !ok {
		t.Fatal("--yes implied autoremove")
	}
	if err := f.m.InstallMany(ctx, []string{"app", "runtime@1.0.0"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RemoveMany(ctx, []string{"app"}, RemoveOptions{Yes: true, AutoRemove: true}); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := s.Packages["runtime"].Versions["1.0.0"]; !ok || r.Automatic {
		t.Fatal("manual dependency ownership not preserved")
	}
}

func TestNativeBatchConflictIsSideEffectFree(t *testing.T) {
	f := dependencyFixture(t)
	addDependency(t, f, "other", "1.0.0", catalog.Dependency{Name: "runtime", Constraint: ">=2"})
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := f.requests
	if err := f.m.InstallMany(context.Background(), []string{"app", "other"}, InstallOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected constraint conflict: %v", err)
	}
	if f.requests != before {
		t.Fatal("downloaded before checking all roots")
	}
	s, err := f.m.LoadState()
	if err != nil || len(s.Packages) != 0 {
		t.Fatalf("partial install: %v %v", s, err)
	}
}

func TestNativeBatchDownloadFailureDoesNotCommitFirstPackage(t *testing.T) {
	f := dependencyFixture(t)
	f.files["/app-1.0.0.tar.gz"] = []byte("corrupt")
	if err := f.m.InstallMany(context.Background(), []string{"app"}, InstallOptions{Yes: true}); err == nil {
		t.Fatal("accepted corrupt root")
	}
	s, err := f.m.LoadState()
	if err != nil || len(s.Packages) != 0 {
		t.Fatalf("dependency committed before root verified: %v %v", s, err)
	}
	missing(t, filepath.Join(f.m.Root, "packages", "runtime", "1.0.0"))
}

func TestNativeResolverBacktracksAndDetectsSelectedCycles(t *testing.T) {
	f := dependencyFixture(t)
	addDependency(t, f, "runtime", "2.0.0", catalog.Dependency{Name: "app", Constraint: "=1.0.0"})
	addDependency(t, f, "app", "1.0.0", catalog.Dependency{Name: "runtime", Constraint: "*"})
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(context.Background(), []string{"app"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	s, _ := f.m.LoadState()
	if s.Packages["runtime"].Active != "1.0.0" {
		t.Fatal("did not backtrack away from cycle")
	}
	g := dependencyFixture(t)
	addDependency(t, g, "runtime", "1.0.0", catalog.Dependency{Name: "app", Constraint: "*"})
	if err := g.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.m.InstallMany(context.Background(), []string{"app"}, InstallOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected selected cycle: %v", err)
	}
}

func TestNativeSwitchProtectsActiveDependentsAndInactiveBindings(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"runtime@2.0.0"}, InstallOptions{Yes: true}); err == nil {
		t.Fatal("silently broke active app")
	}
	// Stage without switching remains subject to active consumers; here no new
	// dependencies are needed, so the incompatible version can coexist inactive.
	if err := f.m.InstallMany(ctx, []string{"runtime@2.0.0"}, InstallOptions{NoSwitch: true, Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Switch("runtime", "2.0.0"); err == nil {
		t.Fatal("switch broke active dependency")
	}
	linkIs(t, f.m.Root, "runtime", "runtime@1.0.0")
	if err := f.m.RemoveMany(ctx, []string{"runtime@1.0.0"}, RemoveOptions{Yes: true, Cascade: true}); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Packages["app"]; ok {
		t.Fatal("cascade left dependent")
	}
}

func (m *Manager) installLockedTest(ctx context.Context, name, version string) error {
	idx, err := m.LoadIndex()
	if err != nil {
		return err
	}
	p, err := idx.Find(name)
	if err != nil {
		return err
	}
	_, a, err := p.Resolve(version, m.Platform)
	if err != nil {
		return err
	}
	return m.withLock(func() error { return m.installLocked(ctx, name, version, a, "", true) })
}

func TestNativeDryRunBatchAndStateMigration(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	if err := f.m.installLockedTest(ctx, "demo", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	old, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if old.SchemaVersion != 1 {
		t.Fatal("fixture is not legacy")
	}
	requests := f.requests
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	now, _ := f.m.LoadState()
	if !reflect.DeepEqual(old, now) || requests != f.requests {
		t.Fatal("dry run mutated installation")
	}
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.m.Root, "state", "installed.v1.backup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var backup State
	if err := json.Unmarshal(data, &backup); err != nil || !reflect.DeepEqual(backup, old) {
		t.Fatal("legacy backup does not match")
	}
}

func TestNativeRemoveEntireBatchBeforeSharedCleanup(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	if err := f.m.InstallMany(ctx, []string{"app", "other"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RemoveMany(ctx, []string{"app", "other"}, RemoveOptions{Yes: true, AutoRemove: true}); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.LoadState()
	if err != nil || len(s.Packages) != 0 {
		t.Fatalf("batch cleanup not global: %v %v", s, err)
	}
}

func TestNativeNoSwitchWithOwnDependenciesCoexistsWithRequiredOldVersion(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	f.add(t, "library", "1.0.0", map[string]string{"library": "bin/library"})
	addDependency(t, f, "runtime", "2.0.0", catalog.Dependency{Name: "library", Constraint: ">=1"})
	if err := f.m.Update(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"runtime@2.0.0"}, InstallOptions{Yes: true, NoSwitch: true}); err != nil {
		t.Fatal(err)
	}
	s, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if s.Packages["runtime"].Active != "1.0.0" || s.Packages["library"].Active != "1.0.0" {
		t.Fatal("no-switch changed the required active root")
	}
	if d := s.Packages["runtime"].Versions["2.0.0"].Dependencies; len(d) != 1 || d[0].Name != "library" || d[0].Version != "1.0.0" {
		t.Fatalf("missing passive dependency binding: %#v", d)
	}
}

func TestNativeNoSwitchCannotIndirectlyActivateAnotherVersion(t *testing.T) {
	f := dependencyFixture(t)
	ctx := context.Background()
	f.add(t, "runtime", "3.0.0", map[string]string{"runtime": "bin/runtime"})
	f.add(t, "library", "1.0.0", map[string]string{"library": "bin/library"})
	addDependency(t, f, "runtime", "2.0.0", catalog.Dependency{Name: "library", Constraint: "=1"})
	addDependency(t, f, "library", "1.0.0", catalog.Dependency{Name: "runtime", Constraint: ">=3"})
	if err := f.m.Update(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"runtime@1.0.0"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"runtime@2.0.0"}, InstallOptions{Yes: true, NoSwitch: true}); err == nil {
		t.Fatal("no-switch indirectly activated another version")
	}
	linkIs(t, f.m.Root, "runtime", "runtime@1.0.0")
	s, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Packages["runtime"].Versions) != 1 {
		t.Fatal("failed plan partially installed versions")
	}
}

func TestNativeUnchangedDependencyMustExistBeforeInstallAndSwitch(t *testing.T) {
	for _, wholeDirectory := range []bool{false, true} {
		t.Run(map[bool]string{false: "command", true: "directory"}[wholeDirectory], func(t *testing.T) {
			f := dependencyFixture(t)
			ctx := context.Background()
			if err := f.m.InstallMany(ctx, []string{"app"}, InstallOptions{Yes: true}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.m.Root, "packages", "runtime", "1.0.0")
			if !wholeDirectory {
				path = filepath.Join(path, "bin", "runtime")
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			requests := f.requests
			if err := f.m.InstallMany(ctx, []string{"other"}, InstallOptions{Yes: true}); err == nil {
				t.Fatal("installed against missing reused dependency")
			}
			if f.requests != requests {
				t.Fatal("downloaded before validating reused dependency")
			}
			if err := f.m.Switch("app", "1.0.0"); err == nil {
				t.Fatal("switched against missing reused dependency")
			}
			s, err := f.m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.Packages["other"]; ok {
				t.Fatal("committed failed installation")
			}
		})
	}
}

func TestNativeClientStateCapabilityProtectsMigrationAndDowngrade(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, version := range []string{"0.6.0", "0.7.0"} {
		f.add(t, "oheco", version, map[string]string{"oo": "bin/oo"})
		schemaCommand := "exit 1"
		if version == "0.7.0" {
			schemaCommand = "echo 2"
		}
		script := "#!/bin/sh\ncase \"$1\" in --version) echo 'oo " + version + " (test/test)' ;; --state-schema) " + schemaCommand + " ;; *) exit 1 ;; esac\n"
		data := archive(t, entry{name: "bin/oo", content: script, mode: 0755})
		path := "/oheco-" + version + ".tar.gz"
		f.files[path] = data
		for i := range f.idx.Packages {
			if f.idx.Packages[i].Name == "oheco" {
				for j := range f.idx.Packages[i].Versions {
					v := &f.idx.Packages[i].Versions[j]
					if v.Version == version {
						v.Artifacts[f.m.Platform] = artifact(data, f.server.URL+path, map[string]string{"oo": "bin/oo"})
					}
				}
			}
		}
	}
	if err := f.m.Update(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := f.idx.Find("oheco")
	_, a, _ := p.Resolve("0.6.0", f.m.Platform)
	if err := f.m.withLock(func() error { return f.m.installLocked(ctx, "oheco", "0.6.0", a, "", false) }); err != nil {
		t.Fatal(err)
	}
	if err := f.m.InstallMany(ctx, []string{"demo"}, InstallOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "cannot manage local state schema 2") {
		t.Fatalf("left old client active after migration: %v", err)
	}
	s, _ := f.m.LoadState()
	if s.SchemaVersion != 1 || len(s.Packages) != 1 {
		t.Fatal("failed migration changed state")
	}
	if err := f.m.InstallMany(ctx, []string{"oheco@0.7.0", "demo"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Switch("oheco", "0.6.0"); err == nil {
		t.Fatal("allowed incompatible client downgrade")
	}
	linkIs(t, f.m.Root, "oo", "oo@0.7.0")
}

type cancelConfirmationReader struct {
	cancel context.CancelFunc
	done   bool
}

func (r *cancelConfirmationReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.cancel()
	return copy(p, "y\n"), nil
}

func TestNativeRemovalCancelledDuringConfirmationDoesNotCommit(t *testing.T) {
	f := dependencyFixture(t)
	if err := f.m.InstallMany(context.Background(), []string{"app", "other"}, InstallOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.m.In = &cancelConfirmationReader{cancel: cancel}
	if err := f.m.RemoveMany(ctx, []string{"app"}, RemoveOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.m.Root, "packages", "app", "1.0.0")); err != nil {
		t.Fatal("cancelled removal deleted package")
	}
	if err := f.m.RemoveMany(ctx, []string{"app"}, RemoveOptions{Yes: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-cancelled operation continued: %v", err)
	}
}

func TestRollbackOnlyRemovesDirectoriesMovedByThisTransaction(t *testing.T) {
	f := setup(t)
	if err := f.m.InstallMany(context.Background(), []string{"demo@1.0.0"}, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	installed, err := f.m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	actualDir := filepath.Join(f.m.Root, "packages", "demo", "1.0.0")
	ownedMove, err := transactionDirectory(filepath.Join("demo", "1.0.0"), actualDir)
	if err != nil {
		t.Fatal(err)
	}
	// This entry was intended for a second rename, but its destination is a
	// user directory. Its stage's distinct identity was recorded pre-rename.
	stage, err := os.MkdirTemp(filepath.Join(f.m.Root, "tmp"), "install-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	unmoved, err := transactionDirectory(filepath.Join("demo", "2.0.0"), stage)
	if err != nil {
		t.Fatal(err)
	}
	p := installed.Packages["demo"]
	p.Versions["2.0.0"] = p.Versions["1.0.0"]
	installed.Packages["demo"] = p
	unowned := filepath.Join(f.m.Root, "packages", "demo", "2.0.0")
	if err := os.Mkdir(unowned, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(unowned, "valuable-user-data.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	tx := transaction{Before: emptyState(), After: installed, Moves: []transactionMove{ownedMove, unmoved}}
	if err := writeJSON(filepath.Join(f.m.Root, "state", "transaction.json"), tx); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Recover(); err != nil {
		t.Fatal(err)
	}
	missing(t, actualDir)
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("rollback deleted unowned data: %s %v", data, err)
	}
	missing(t, filepath.Join(f.m.Root, "state", "transaction.json"))
}

func TestLegacyRollbackDoesNotGuessDirectoryOwnership(t *testing.T) {
	f := setup(t)
	if err := f.m.InstallMany(context.Background(), []string{"demo@1.0.0"}, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	installed, _ := f.m.LoadState()
	if err := writeJSON(filepath.Join(f.m.Root, "state", "transaction.json"), transaction{Before: emptyState(), After: installed}); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.m.Root, "packages", "demo", "1.0.0")); err != nil {
		t.Fatal("legacy rollback deleted directory without ownership proof")
	}
	s, err := f.m.LoadState()
	if err != nil || len(s.Packages) != 0 {
		t.Fatal("legacy journal did not restore state")
	}
}

func TestNativeUnknownTargetDoesNotInstallEarlierRoot(t *testing.T) {
	f := dependencyFixture(t)
	requests := f.requests
	if err := f.m.InstallMany(context.Background(), []string{"demo", "not-in-index"}, InstallOptions{Yes: true}); err == nil {
		t.Fatal("accepted unknown target")
	}
	if f.requests != requests {
		t.Fatal("installed earlier target")
	}
}
