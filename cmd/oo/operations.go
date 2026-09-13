package main

import (
	"fmt"
	"strings"

	"github.com/oheco/oheco/internal/manager"
)

func operationArgs(command string, args []string) ([]string, manager.InstallOptions, manager.RemoveOptions, error) {
	var specs, tail []string
	install, remove := manager.InstallOptions{}, manager.RemoveOptions{}
	seen := map[string]bool{}
	for i, arg := range args {
		if arg == "--" {
			tail = append([]string{}, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			specs = append(specs, arg)
			continue
		}
		if arg == "-y" {
			arg = "--yes"
		}
		if seen[arg] {
			return nil, install, remove, fmt.Errorf("duplicate option %s", arg)
		}
		seen[arg] = true
		switch {
		case arg == "--yes":
			install.Yes, remove.Yes = true, true
		case arg == "--dry-run":
			install.DryRun, remove.DryRun = true, true
		case command == "install" && arg == "--no-switch":
			install.NoSwitch = true
		case command == "remove" && arg == "--all":
			remove.All = true
		case command == "remove" && arg == "--autoremove":
			remove.AutoRemove = true
		case command == "remove" && arg == "--cascade":
			remove.Cascade = true
		default:
			return nil, install, remove, fmt.Errorf("unsupported %s option %s; backend arguments must follow --", command, arg)
		}
	}
	if len(specs) == 0 {
		return nil, install, remove, fmt.Errorf("%s requires at least one package before --", command)
	}
	install.Args, remove.Args = tail, tail
	return specs, install, remove, nil
}
