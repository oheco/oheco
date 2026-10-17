package main

import (
	"fmt"
	"testing"

	"github.com/oheco/oheco/internal/manager"
)

func TestExportArguments(t *testing.T) {
	for _, tc := range []struct {
		name               string
		args               []string
		spec, project, out string
		options            manager.ExportOptions
	}{
		{name: "package", args: []string{"godot"}, spec: "godot"},
		{name: "project and short output", args: []string{"godot@1", "editor", "-o", "a b"}, spec: "godot@1", project: "editor", out: "a b"},
		{name: "output equals before positionals", args: []string{"--output=out", "godot", "editor"}, spec: "godot", project: "editor", out: "out"},
		{name: "output after package", args: []string{"godot", "--output", "out"}, spec: "godot", out: "out"},
		{name: "output between positionals", args: []string{"godot", "-o", "out", "editor"}, spec: "godot", project: "editor", out: "out"},
		{name: "install defaults", args: []string{"godot", "--install"}, spec: "godot", options: manager.ExportOptions{Install: true}},
		{
			name: "all install options separated",
			args: []string{"godot@1", "editor", "--install", "--device", "serial-1", "--team", "TEAM1", "--product", "phone", "-y", "--dry-run", "-o", "a b"},
			spec: "godot@1", project: "editor", out: "a b",
			options: manager.ExportOptions{Install: true, Device: "serial-1", TeamID: "TEAM1", Product: "phone", Yes: true, DryRun: true},
		},
		{
			name: "install last and options between positionals",
			args: []string{"--device", "serial-1", "godot", "--team", "TEAM1", "editor", "--product", "phone", "--yes", "--dry-run", "--install"},
			spec: "godot", project: "editor",
			options: manager.ExportOptions{Install: true, Device: "serial-1", TeamID: "TEAM1", Product: "phone", Yes: true, DryRun: true},
		},
		{
			name: "equals values preserve extra equals",
			args: []string{"--output=out=dir", "--device=serial=1", "--team=TEAM=1", "--product=phone=debug", "--install", "godot"},
			spec: "godot", out: "out=dir",
			options: manager.ExportOptions{Install: true, Device: "serial=1", TeamID: "TEAM=1", Product: "phone=debug"},
		},
		{
			name: "equals output can start with a dash",
			args: []string{"godot", "--output=-out"}, spec: "godot", out: "-out",
		},
		{
			name: "omitted product stays empty",
			args: []string{"--install", "--device=serial-1", "--team=TEAM1", "godot"}, spec: "godot",
			options: manager.ExportOptions{Install: true, Device: "serial-1", TeamID: "TEAM1"},
		},
		{
			name: "yes alone with install",
			args: []string{"godot", "--yes", "--install"}, spec: "godot", options: manager.ExportOptions{Install: true, Yes: true},
		},
		{
			name: "dry run alone with install",
			args: []string{"--dry-run", "godot", "--install"}, spec: "godot", options: manager.ExportOptions{Install: true, DryRun: true},
		},
		{name: "trailing separator", args: []string{"godot", "--"}, spec: "godot"},
		{name: "literal option as project", args: []string{"godot", "--", "--install"}, spec: "godot", project: "--install"},
		{name: "literal option-like positionals", args: []string{"--", "--install", "--device=serial-1"}, spec: "--install", project: "--device=serial-1"},
		{name: "second separator is literal", args: []string{"godot", "--", "--"}, spec: "godot", project: "--"},
		{
			name: "install before literal positionals",
			args: []string{"--install", "--", "-godot", "--yes"}, spec: "-godot", project: "--yes", options: manager.ExportOptions{Install: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, project, out, options, err := exportArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if spec != tc.spec || project != tc.project || out != tc.out || options != tc.options {
				t.Fatalf("exportArgs(%q) = (%q, %q, %q, %+v), want (%q, %q, %q, %+v)", tc.args, spec, project, out, options, tc.spec, tc.project, tc.out, tc.options)
			}
		})
	}
}

func TestExportArgumentsRejectInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no package", nil},
		{"options without package", []string{"--install", "--yes"}},
		{"empty package", []string{""}},
		{"empty project", []string{"godot", ""}},
		{"too many positionals", []string{"godot", "editor", "extra"}},
		{"too many literal positionals", []string{"godot", "--", "--device", "serial-1"}},
		{"unknown option", []string{"godot", "--force"}},
		{"short output equals unsupported", []string{"godot", "-o=out"}},
		{"install boolean value unsupported", []string{"godot", "--install=true"}},
		{"yes boolean value unsupported", []string{"godot", "--install", "--yes=false"}},
		{"dry run boolean value unsupported", []string{"godot", "--install", "--dry-run="}},
		{"output missing", []string{"godot", "--output"}},
		{"short output missing", []string{"godot", "-o"}},
		{"output empty", []string{"godot", "--output", ""}},
		{"output equals empty", []string{"godot", "--output="}},
		{"output followed by flag", []string{"godot", "--output", "--install"}},
		{"output followed by separator", []string{"godot", "--output", "--"}},
		{"device missing", []string{"godot", "--install", "--device"}},
		{"device empty", []string{"godot", "--install", "--device", ""}},
		{"device equals empty", []string{"godot", "--install", "--device="}},
		{"device followed by flag", []string{"godot", "--install", "--device", "--yes"}},
		{"device followed by separator", []string{"godot", "--install", "--device", "--", "serial-1"}},
		{"team missing", []string{"godot", "--install", "--team"}},
		{"team empty", []string{"godot", "--install", "--team", ""}},
		{"team equals empty", []string{"godot", "--install", "--team="}},
		{"team followed by flag", []string{"godot", "--install", "--team", "--product=phone"}},
		{"product missing", []string{"godot", "--install", "--product"}},
		{"product empty", []string{"godot", "--install", "--product", ""}},
		{"product equals empty", []string{"godot", "--install", "--product="}},
		{"product followed by flag", []string{"godot", "--install", "--product", "-y"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, _, err := exportArgs(tc.args); err == nil {
				t.Fatalf("accepted %q", tc.args)
			}
		})
	}
}

func TestExportArgumentsRejectDuplicateOptions(t *testing.T) {
	for _, flags := range [][]string{
		{"--install"},
		{"-o", "a", "-o", "b"},
		{"-o", "a", "--output=b"},
		{"--output=a", "-o", "b"},
		{"--output", "a", "--output=b"},
		{"--device", "serial-1", "--device=serial-2"},
		{"--device=serial-1", "--device=serial-1"},
		{"--team=TEAM1", "--team", "TEAM2"},
		{"--product", "phone", "--product=phone"},
		{"--yes", "--yes"},
		{"-y", "-y"},
		{"-y", "--yes"},
		{"--yes", "-y"},
		{"--dry-run", "--dry-run"},
	} {
		args := append([]string{"godot", "--install"}, flags...)
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			if _, _, _, _, err := exportArgs(args); err == nil {
				t.Fatalf("accepted duplicate options %q", args)
			}
		})
	}
}

func TestExportArgumentsRequireInstall(t *testing.T) {
	for _, flags := range [][]string{
		{"--device", "serial-1"}, {"--device=serial-1"},
		{"--team", "TEAM1"}, {"--team=TEAM1"},
		{"--product", "phone"}, {"--product=phone"},
		{"-y"}, {"--yes"}, {"--dry-run"},
	} {
		args := append([]string{"godot"}, flags...)
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			if _, _, _, _, err := exportArgs(args); err == nil {
				t.Fatalf("accepted install-only options without --install: %q", args)
			}
		})
	}
}

func TestExportArgumentsRejectControlCharacters(t *testing.T) {
	for _, control := range []string{"\n", "\r", "\t", "\x00", "\x1b", "\x7f", "\u0085"} {
		value := "before" + control + "after"
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"package", []string{value}},
			{"project", []string{"godot", value}},
			{"output", []string{"godot", "--output", value}},
			{"output equals", []string{"godot", "--output=" + value}},
			{"device", []string{"godot", "--install", "--device", value}},
			{"device equals", []string{"godot", "--install", "--device=" + value}},
			{"team", []string{"godot", "--install", "--team", value}},
			{"team equals", []string{"godot", "--install", "--team=" + value}},
			{"product", []string{"godot", "--install", "--product", value}},
			{"product equals", []string{"godot", "--install", "--product=" + value}},
			{"literal package", []string{"--", value}},
			{"literal project", []string{"godot", "--", value}},
		} {
			t.Run(fmt.Sprintf("%s/%q", tc.name, control), func(t *testing.T) {
				if _, _, _, _, err := exportArgs(tc.args); err == nil {
					t.Fatalf("accepted control character in %q", tc.args)
				}
			})
		}
	}
}
