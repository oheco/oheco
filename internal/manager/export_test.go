package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

type cancelAtOutput struct {
	context.Context
	filename string
}

func (c cancelAtOutput) Err() error {
	if _, err := os.Stat(c.filename); err == nil {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestProjectCopyCancellationRemovesOnlyNewFiles(t *testing.T) {
	stage, destination := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(stage, "entry"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "entry/Main.ets"), []byte("project source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "my-notes"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := cancelAtOutput{context.Background(), filepath.Join(destination, "entry/Main.ets")}
	if err := publishProject(ctx, stage, destination); !errors.Is(err, context.Canceled) {
		t.Fatal("expected interrupted copy:", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 1 || entries[0].Name() != "my-notes" {
		t.Fatal("partial copy or removed user data:", entries, err)
	}
	content, _ := os.ReadFile(filepath.Join(destination, "my-notes"))
	if string(content) != "keep" {
		t.Fatal("modified user notes")
	}
}

func exportFixture(t *testing.T, data []byte, format string) *fixture {
	t.Helper()
	f := setup(t)
	f.idx.Packages = nil
	f.add(t, "app", "1.0", map[string]string{"app": "bin/app"})
	f.idx.SchemaVersion = 4
	f.files["/project"] = data
	a := artifact(data, f.server.URL+"/project", map[string]string{})
	a.Format = format
	p := &f.idx.Packages[0]
	p.SchemaVersion = 4
	p.Versions[0].Artifacts = nil
	p.Versions[0].Projects = map[string]catalog.Project{"editor": {URL: a.URL, SHA256: a.SHA256, Size: a.Size, Format: a.Format, StripComponents: 1, Description: "DevEco shell"}}
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExportProjectDefaultDirectoryCacheAndNoInstallation(t *testing.T) {
	data := archive(t, entry{name: "app/build-profile.json5", content: "{}", mode: 0644}, entry{name: "app/bin/tool", content: "signed bytes"}, entry{name: "app/tool-link", kind: tar.TypeSymlink, link: "bin/tool"})
	f := exportFixture(t, data, "tar.gz")
	ctx := context.Background()
	destination := filepath.Join(t.TempDir(), "project with spaces")
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "my-notes"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(destination); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := f.m.Export(ctx, "app", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "tool-link"))
	if err != nil || string(got) != "signed bytes" {
		t.Fatalf("symlink/bytes: %q %v", got, err)
	}
	info, _ := os.Stat(filepath.Join(destination, "bin/tool"))
	if info.Mode().Perm()&0111 == 0 {
		t.Fatal("lost executable permission")
	}
	state, err := f.m.LoadState()
	if err != nil || len(state.Packages) != 0 {
		t.Fatal("export installed a package:", state, err)
	}
	bins, _ := os.ReadDir(filepath.Join(f.m.Root, "bin"))
	if len(bins) != 0 {
		t.Fatal("created command links")
	}
	if err := f.m.Install(ctx, "app", false); err == nil || !strings.Contains(err.Error(), "oo export") {
		t.Fatal("project-only install:", err)
	}
	if err := f.m.Export(ctx, "app", "", destination); err == nil {
		t.Fatal("overwrote destination")
	}
	got, _ = os.ReadFile(filepath.Join(destination, "my-notes"))
	if string(got) != "keep" {
		t.Fatal("modified unrelated file")
	}
	f.server.Close()
	if err := f.m.Export(ctx, "app@1.0", "editor", filepath.Join(t.TempDir(), "offline", "project")); err != nil {
		t.Fatal("cached offline export:", err)
	}
}

func TestExportRejectsMalformedProjectsWithoutWritingDestination(t *testing.T) {
	for name, entries := range map[string][]entry{
		"traversal":      {{name: "app/../../escape", content: "bad"}},
		"case collision": {{name: "app/Main.cs", content: "A"}, {name: "app/main.cs", content: "B"}},
		"escaping link":  {{name: "app/link", kind: tar.TypeSymlink, link: "../../escape"}},
		"duplicate":      {{name: "app/a", content: "A"}, {name: "app/a", content: "B"}},
		"empty":          {{name: "app/", kind: tar.TypeDir}},
	} {
		t.Run(name, func(t *testing.T) {
			f := exportFixture(t, archive(t, entries...), "tar.gz")
			destination := filepath.Join(t.TempDir(), "output")
			if err := f.m.Export(context.Background(), "app", "", destination); err == nil {
				t.Fatal("accepted malformed project")
			}
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatal("created destination before archive validation")
			}
		})
	}
}

func TestExportZipDigestConflictAndQueries(t *testing.T) {
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	w, _ := z.Create("app/build-profile.json5")
	_, _ = w.Write([]byte("{}"))
	w, _ = z.Create("app/entry/Main.ets")
	_, _ = w.Write([]byte("source"))
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	f := exportFixture(t, data.Bytes(), "zip")
	ctx := context.Background()
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.Search(ctx, "DevEco"); err != nil || !strings.Contains(out.String(), "projects") {
		t.Fatalf("project search: %s %v", out.String(), err)
	}
	out.Reset()
	if err := f.m.Info("app"); err != nil || !strings.Contains(out.String(), "oo export app@1.0 editor") {
		t.Fatalf("project info: %s %v", out.String(), err)
	}
	destination := t.TempDir()
	if err := os.Mkdir(filepath.Join(destination, "entry"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Export(ctx, "app", "", destination); err == nil {
		t.Fatal("merged existing directory")
	}
	if _, err := os.Stat(filepath.Join(destination, "build-profile.json5")); !os.IsNotExist(err) {
		t.Fatal("partial export after conflict")
	}
	if err := f.m.Export(ctx, "app", "", filepath.Join(t.TempDir(), "valid")); err != nil {
		t.Fatal(err)
	}
	project := f.idx.Packages[0].Versions[0].Projects["editor"]
	cache := filepath.Join(f.m.Root, "cache/downloads", project.SHA256+".zip")
	if err := os.WriteFile(cache, []byte("corrupted"), 0644); err != nil {
		t.Fatal(err)
	}
	f.files["/project"] = []byte("corrupted")
	destination = filepath.Join(t.TempDir(), "bad")
	if err := f.m.Export(ctx, "app", "", destination); err == nil {
		t.Fatal("accepted corrupted cache/download")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("created destination for bad download")
	}
}
