package main

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/oheco/oheco/internal/manager"
)

func exportArgs(args []string) (spec, project, destination string, options manager.ExportOptions, err error) {
	positionals := []string{}
	seen := map[string]bool{}
	literal := false
	fail := func(reason string) (string, string, string, manager.ExportOptions, error) {
		return "", "", "", manager.ExportOptions{}, fmt.Errorf("%s; usage: oo export <package[@version]> [project] [-o|--output directory] [--install [--device value] [--team value] [--product value] [-y|--yes] [--dry-run]]", reason)
	}
	for _, arg := range args {
		if strings.IndexFunc(arg, unicode.IsControl) >= 0 {
			return fail("arguments must not contain control characters")
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literal && arg == "--" {
			literal = true
			continue
		}
		if literal || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "-o" {
			arg = "--output"
		} else if arg == "-y" {
			arg = "--yes"
		}
		flag, value, hasValue := strings.Cut(arg, "=")
		if seen[flag] {
			return fail("duplicate option " + flag)
		}
		seen[flag] = true
		switch flag {
		case "--output", "--device", "--team", "--product":
			if !hasValue {
				i++
				if i == len(args) || strings.HasPrefix(args[i], "-") {
					return fail("missing value for " + flag)
				}
				value = args[i]
			}
			if value == "" {
				return fail("empty value for " + flag)
			}
			switch flag {
			case "--output":
				destination = value
			case "--device":
				options.Device = value
			case "--team":
				options.TeamID = value
			case "--product":
				options.Product = value
			}
		case "--install", "--yes", "--dry-run":
			if hasValue {
				return fail("unexpected value for " + flag)
			}
			switch flag {
			case "--install":
				options.Install = true
			case "--yes":
				options.Yes = true
			case "--dry-run":
				options.DryRun = true
			}
		default:
			return fail("unsupported option " + flag)
		}
	}
	if len(positionals) < 1 || len(positionals) > 2 || positionals[0] == "" {
		return fail("export requires a package and at most one project")
	}
	spec = positionals[0]
	if len(positionals) == 2 {
		project = positionals[1]
		if project == "" {
			return fail("project must not be empty")
		}
	}
	if !options.Install && (options.Device != "" || options.TeamID != "" || options.Product != "" || options.Yes || options.DryRun) {
		return fail("--device, --team, --product, --yes and --dry-run require --install")
	}
	return spec, project, destination, options, nil
}
