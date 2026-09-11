package manager

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

func TestSearchTable(t *testing.T) {
	f := setup(t)
	f.add(t, "long-package-name", "3.0.0", map[string]string{"long-tool": "bin/long-tool"})
	p := &f.idx.Packages[0]
	p.Description = "A package\twith\n多行说明"
	p.Maintainers = []catalog.Maintainer{{GitHub: "alice", Name: "Alice Example"}, {GitHub: "bob"}}
	// The platform's designated latest can precede other versions and have a
	// different download size from another platform's artifact.
	p.Latest[f.m.Platform] = "1.0.0"
	a := p.Versions[0].Artifacts[f.m.Platform]
	a.Size = 1536
	p.Versions[0].Artifacts[f.m.Platform] = a
	a.Size = 8192
	p.Versions[0].Artifacts["other-arm64"] = a
	p.Latest["other-arm64"] = "2.0.0"
	p.Versions[1].Artifacts["other-arm64"] = a
	a = f.idx.Packages[1].Versions[0].Artifacts[f.m.Platform]
	a.Size = 2 << 20
	f.idx.Packages[1].Versions[0].Artifacts[f.m.Platform] = a
	if err := writeJSON(filepath.Join(f.m.Root, "index", "index.json"), f.idx); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.Search(""); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header and two rows, got %q", out.String())
	}
	rows := [][]string{
		{"NAME", "LATEST", "SIZE", "MAINTAINERS", "DESCRIPTION"},
		{"demo", "1.0.0", "1.5 KiB", "@alice, @bob", "A package with 多行说明"},
		{"long-package-name", "3.0.0", "2.0 MiB", "@oheco", "test package"},
	}
	for row, cells := range rows {
		for col, cell := range cells {
			position := strings.Index(lines[row], cell)
			wantPosition := strings.Index(lines[0], rows[0][col])
			if position != wantPosition {
				t.Errorf("row %d column %s: %q starts at %d, want %d; output:\n%s", row, rows[0][col], cell, position, wantPosition, out.String())
			}
		}
	}
	// Matching remains case-insensitive and includes command names.
	out.Reset()
	if err := f.m.Search("LONG-TOOL"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "demo") || !strings.Contains(out.String(), "long-package-name") {
		t.Fatalf("unexpected command search results: %q", out.String())
	}
}

func TestSearchUnavailableAndNoMatches(t *testing.T) {
	f := setup(t)
	f.m.Platform = "other-arm64"
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.Search("demo"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "unavailable for other-arm64") {
		t.Fatalf("missing unavailable platform status: %q", out.String())
	}
	sizeColumn := strings.Index(lines[0], "SIZE")
	maintainerColumn := strings.Index(lines[0], "MAINTAINERS")
	if got := strings.TrimSpace(lines[1][sizeColumn:maintainerColumn]); got != "-" {
		t.Fatalf("unavailable package size = %q, want -", got)
	}
	out.Reset()
	if err := f.m.Search("no-such-package"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "No matching packages.\n" {
		t.Fatalf("no matches output = %q", got)
	}
}
