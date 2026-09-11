package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

type zipEntry struct {
	name, content string
	mode          os.FileMode
}

func zipArchive(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		h.SetMode(e.mode)
		out, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestZIPExtraction(t *testing.T) {
	data := zipArchive(t,
		zipEntry{"sdk/", "", os.ModeDir | 0755},
		zipEntry{"sdk/lib/tool", "#!/bin/sh\nexit 0\n", 0755},
		zipEntry{"sdk/bin/tool", "../lib/tool", os.ModeSymlink | 0777},
		zipEntry{"sdk/share/说明.txt", "SDK data", 0644},
	)
	a := artifact(data, "https://example.com/sdk.zip", map[string]string{"tool": "bin/tool"})
	a.Format, a.StripComponents = "zip", 1
	dir := t.TempDir()
	file := filepath.Join(dir, "sdk.zip")
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extract(context.Background(), file, out, a); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(out, "bin/tool")); err != nil || target != "../lib/tool" {
		t.Fatalf("symlink: %q %v", target, err)
	}
	for rel, mode := range map[string]os.FileMode{"lib/tool": 0755, "share/说明.txt": 0644} {
		info, err := os.Stat(filepath.Join(out, rel))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %o", rel, info.Mode().Perm())
		}
	}
}

func TestZIPRejectsUnsafeOrCorruptEntries(t *testing.T) {
	tests := map[string][]zipEntry{
		"traversal":            {{"sdk/../escape", "x", 0755}},
		"absolute":             {{"/escape", "x", 0755}},
		"backslash":            {{`sdk\escape`, "x", 0755}},
		"duplicate":            {{"sdk/bin/tool", "x", 0755}, {"sdk/bin/tool", "y", 0755}},
		"normalized duplicate": {{"sdk/bin/tool", "x", 0755}, {"sdk/bin/./tool", "y", 0755}},
		"strip discards file":  {{"sdk", "x", 0755}},
		"non executable":       {{"sdk/bin/tool", "x", 0644}},
		"special file":         {{"sdk/bin/tool", "", os.ModeNamedPipe | 0755}},
		"reserved directory":   {{"sdk/.oo-launchers/tool", "x", 0755}},
		"symlink escape":       {{"sdk/bin/tool", "../../escape", os.ModeSymlink | 0777}},
		"symlink absolute":     {{"sdk/bin/tool", "/etc/passwd", os.ModeSymlink | 0777}},
		"symlink dangling":     {{"sdk/bin/tool", "../missing", os.ModeSymlink | 0777}},
		"symlink cycle":        {{"sdk/bin/tool", "other", os.ModeSymlink | 0777}, {"sdk/bin/other", "tool", os.ModeSymlink | 0777}},
		"symlink parent":       {{"sdk/bin", "lib", os.ModeSymlink | 0777}, {"sdk/bin/tool", "x", 0755}, {"sdk/lib/tool", "x", 0755}},
		"symlink chain escape": {{"sdk/link", "real", os.ModeSymlink | 0777}, {"sdk/real", "..", os.ModeSymlink | 0777}, {"sdk/bin/tool", "../link/escape", os.ModeSymlink | 0777}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			data := zipArchive(t, entries...)
			a := artifact(data, "https://example.com/sdk.zip", map[string]string{"tool": "bin/tool"})
			a.Format, a.StripComponents = "zip", 1
			dir := t.TempDir()
			file := filepath.Join(dir, "sdk.zip")
			os.WriteFile(file, data, 0644)
			out := filepath.Join(dir, "out")
			os.Mkdir(out, 0755)
			if err := extract(context.Background(), file, out, a); err == nil {
				t.Fatal("accepted unsafe ZIP")
			}
			missing(t, filepath.Join(dir, "escape"))
		})
	}
	for _, corrupt := range []string{"crc", "truncated", "oversized", "encrypted", "cancelled"} {
		t.Run(corrupt, func(t *testing.T) {
			data := zipArchive(t, zipEntry{"sdk/bin/tool", "unique zip payload", 0755})
			if corrupt == "crc" {
				data[bytes.Index(data, []byte("unique zip payload"))] ^= 1
			}
			if corrupt == "truncated" {
				data = data[:len(data)-12]
			}
			if corrupt == "oversized" {
				// Each declared file is below 8 GiB; their combined size is not.
				var buf bytes.Buffer
				w := zip.NewWriter(&buf)
				for _, name := range []string{"sdk/a", "sdk/b"} {
					h := &zip.FileHeader{Name: name, UncompressedSize64: 5 << 30, Method: zip.Store}
					h.SetMode(0755)
					if _, err := w.CreateRaw(h); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				data = buf.Bytes()
			}
			if corrupt == "encrypted" {
				central := bytes.Index(data, []byte{'P', 'K', 1, 2})
				data[central+8] |= 1
			}
			a := artifact(data, "https://example.com/sdk.zip", map[string]string{"tool": "bin/tool"})
			a.Format, a.StripComponents = "zip", 1
			dir := t.TempDir()
			file := filepath.Join(dir, "sdk.zip")
			os.WriteFile(file, data, 0644)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if corrupt == "cancelled" {
				cancel()
			}
			if err := extract(ctx, file, filepath.Join(dir, "out"), a); err == nil {
				t.Fatal("accepted invalid ZIP")
			}
		})
	}
}

func TestZIPDataPackageLifecycle(t *testing.T) {
	f := setup(t)
	f.idx.SchemaVersion = catalog.SchemaVersion
	p := f.idx.Packages[0]
	p.SchemaVersion = catalog.SchemaVersion
	p.Name = "sdk-data"
	p.Latest = map[string]string{f.m.Platform: "26-Beta"}
	data := zipArchive(t, zipEntry{"previewer/NOTICE.txt", "notice", 0644})
	a := artifact(data, f.server.URL+"/previewer.zip", map[string]string{})
	a.Format, a.StripComponents = "zip", 1
	f.files["/previewer.zip"] = data
	p.Versions = []catalog.Version{{Version: "26-Beta", Artifacts: map[string]catalog.Artifact{f.m.Platform: a}}}
	f.idx.Packages = []catalog.Package{p}
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Install(context.Background(), p.Name, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.m.Root, "cache/downloads", a.SHA256+".zip")); err != nil {
		t.Fatal(err)
	}
	f.server.Close()
	if err := f.m.Switch(p.Name, "26-Beta"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.m.Root, "bin"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("data package created commands: %v %v", entries, err)
	}
	if err := f.m.Remove(p.Name, false); err != nil {
		t.Fatal(err)
	}
	missing(t, filepath.Join(f.m.Root, "packages", p.Name))
}
