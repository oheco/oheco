package main

import (
	"fmt"
	"strings"
)

func exportArgs(args []string) (spec, project, destination string, err error) {
	positionals := []string{}
	outputSet, literal := false, false
	fail := func() (string, string, string, error) {
		return "", "", "", fmt.Errorf("usage: oo export <package[@version]> [project] [-o|--output directory]")
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literal && arg == "--" {
			literal = true
			continue
		}
		if !literal && (arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=")) {
			if outputSet {
				return fail()
			}
			outputSet = true
			if strings.HasPrefix(arg, "--output=") {
				destination = strings.TrimPrefix(arg, "--output=")
			} else {
				i++
				if i == len(args) {
					return fail()
				}
				destination = args[i]
			}
			if destination == "" {
				return fail()
			}
		} else if !literal && strings.HasPrefix(arg, "-") {
			return fail()
		} else {
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) < 1 || len(positionals) > 2 || positionals[0] == "" {
		return fail()
	}
	spec = positionals[0]
	if len(positionals) == 2 {
		project = positionals[1]
		if project == "" {
			return fail()
		}
	}
	return spec, project, destination, nil
}
