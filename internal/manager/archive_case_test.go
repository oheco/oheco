package manager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

func caseArchive(t *testing.T, format string, entries []zipEntry) []byte {
	t.Helper()
	if format == "zip" {
		return zipArchive(t, entries...)
	}
	var tarEntries []entry
	for _, e := range entries {
		v := entry{name: e.name, content: e.content, mode: int64(e.mode.Perm())}
		switch e.mode.Type() {
		case os.ModeDir:
			v.kind = tar.TypeDir
		case os.ModeSymlink:
			v.kind, v.link, v.content = tar.TypeSymlink, e.content, ""
		}
		tarEntries = append(tarEntries, v)
	}
	return archive(t, tarEntries...)
}

func extractCaseArchive(t *testing.T, format string, data []byte) (string, error) {
	t.Helper()
	dir := t.TempDir()
	filename := filepath.Join(dir, "sdk."+format)
	if err := os.WriteFile(filename, data, 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	return out, extract(context.Background(), filename, out, catalog.Artifact{Format: format, StripComponents: 1})
}

func TestArchivePrefersLowercaseFiles(t *testing.T) {
	variants := []zipEntry{
		{"sdk/Include/xt_MARK.h", "uppercase executable", 0755},
		{"sdk/Include/xt_Mark.h", "mixed case executable", 0755},
		{"sdk/Include/xt_mark.h", "original lowercase header", 0644},
	}
	// Both two-entry orders and every permutation of three variants; the parent
	// directory's uppercase spelling is intentional and must stay unchanged.
	orders := [][]int{{0, 2}, {2, 0}, {0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, format := range []string{"tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			for _, order := range orders {
				entries := []zipEntry{{"sdk/README", "standalone uppercase file", 0644}}
				for _, i := range order {
					entries = append(entries, variants[i])
				}
				entries = append(entries, zipEntry{"sdk/Include/", "", os.ModeDir | 0755})
				out, err := extractCaseArchive(t, format, caseArchive(t, format, entries))
				if err != nil {
					t.Fatalf("order %v: %v", order, err)
				}
				files, err := os.ReadDir(filepath.Join(out, "Include"))
				if err != nil || len(files) != 1 || files[0].Name() != "xt_mark.h" {
					t.Fatalf("order %v: expected one lowercase filename, got %v (%v)", order, files, err)
				}
				content, err := os.ReadFile(filepath.Join(out, "Include/xt_mark.h"))
				if err != nil || string(content) != variants[2].content {
					t.Fatalf("order %v: incorrect lowercase content %q (%v)", order, content, err)
				}
				info, err := files[0].Info()
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatalf("order %v: incorrect winner permissions: %v (%v)", order, info, err)
				}
				rootFiles, err := os.ReadDir(out)
				if err != nil || len(rootFiles) != 2 || rootFiles[0].Name() != "Include" || rootFiles[1].Name() != "README" {
					t.Fatalf("order %v: changed unconflicted names: %v (%v)", order, rootFiles, err)
				}
			}
		})
	}
}

func TestArchiveRejectsUnresolvedCaseConflicts(t *testing.T) {
	tests := map[string][]zipEntry{
		"no lowercase winner":         {{"sdk/FOO", "x", 0644}, {"sdk/Foo", "y", 0644}},
		"duplicate winner":            {{"sdk/foo", "x", 0644}, {"sdk/FOO", "y", 0644}, {"sdk/foo", "z", 0644}},
		"duplicate discarded variant": {{"sdk/foo", "x", 0644}, {"sdk/FOO", "y", 0644}, {"sdk/FOO", "z", 0644}},
		"duplicate replaced variant":  {{"sdk/FOO", "x", 0644}, {"sdk/foo", "y", 0644}, {"sdk/FOO", "z", 0644}},
		"directory":                   {{"sdk/Foo/", "", os.ModeDir | 0755}, {"sdk/foo/", "", os.ModeDir | 0755}},
		"implicit directory":          {{"sdk/Foo/a", "x", 0644}, {"sdk/foo/b", "y", 0644}},
		"file then directory":         {{"sdk/FOO", "x", 0644}, {"sdk/foo/a", "y", 0644}},
		"directory then file":         {{"sdk/FOO/a", "x", 0644}, {"sdk/foo", "y", 0644}},
		"file then symlink":           {{"sdk/FOO", "x", 0644}, {"sdk/foo", "FOO", os.ModeSymlink | 0777}},
		"symlink then file":           {{"sdk/FOO", "foo", os.ModeSymlink | 0777}, {"sdk/foo", "x", 0644}},
		"symlink variants":            {{"sdk/target", "x", 0644}, {"sdk/FOO", "target", os.ModeSymlink | 0777}, {"sdk/foo", "target", os.ModeSymlink | 0777}},
		"reserved directory case":     {{"sdk/.OO-LAUNCHERS/tool", "x", 0755}},
	}
	for _, format := range []string{"tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			for name, entries := range tests {
				t.Run(name, func(t *testing.T) {
					if _, err := extractCaseArchive(t, format, caseArchive(t, format, entries)); err == nil {
						t.Fatal("accepted unresolved case conflict")
					}
				})
			}
		})
	}
}

func TestZIPChecksDiscardedCaseVariantCRC(t *testing.T) {
	data := zipArchive(t,
		zipEntry{"sdk/foo", "lowercase content", 0644},
		zipEntry{"sdk/FOO", "unique discarded payload", 0644},
	)
	data[bytes.Index(data, []byte("unique discarded payload"))] ^= 1
	if _, err := extractCaseArchive(t, "zip", data); !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("discarded variant must fail checksum verification: %v", err)
	}
}
