package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/oheco/oheco/internal/manager"
)

var version = "0.1.0"

const help = `oo — the oheco package manager

Usage:
  oo update                          Refresh the local package index
  oo search [query]                   Search the local index
  oo info <package>                   Show available versions and package details
  oo install <package[@version]> [--no-switch]
  oo switch <package> <version>       Activate an installed version (offline)
  oo remove <package[@version]> [--all]
  oo list                            List installed versions (* = active)
  oo recover                         Recover an interrupted operation
  oo --version

install activates the selected version unless --no-switch is given.
remove without a version removes the active version; --all removes every version.
update refreshes metadata only. To update oo itself: oo install oheco

Environment:
  OHECO_ROOT        Installation root (default: ~/.oheco)
  OHECO_INDEX_URL   Index URL (default: official GitHub Pages index)
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "oo:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(help)
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Printf("oo %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return nil
	}
	m, err := manager.New(os.Getenv("OHECO_ROOT"), os.Getenv("OHECO_INDEX_URL"), os.Stdout)
	if err != nil {
		return err
	}
	command, args := args[0], args[1:]
	switch command {
	case "update":
		if len(args) != 0 {
			break
		}
		return m.Update(ctx)
	case "search":
		if len(args) > 1 {
			break
		}
		return m.Search(strings.Join(args, ""))
	case "info":
		if len(args) != 1 {
			break
		}
		return m.Info(args[0])
	case "list":
		if len(args) != 0 {
			break
		}
		return m.List()
	case "recover":
		if len(args) != 0 {
			break
		}
		return m.Recover()
	case "install", "remove":
		flag, spec := false, ""
		allowed := "--no-switch"
		if command == "remove" {
			allowed = "--all"
		}
		for _, arg := range args {
			if arg == allowed && !flag {
				flag = true
			} else if strings.HasPrefix(arg, "-") || spec != "" {
				return fmt.Errorf("invalid arguments; run oo --help")
			} else {
				spec = arg
			}
		}
		if spec == "" {
			break
		}
		if command == "install" {
			return m.Install(ctx, spec, flag)
		}
		return m.Remove(spec, flag)
	case "switch":
		if len(args) != 2 {
			break
		}
		return m.Switch(args[0], args[1])
	case "_bootstrap":
		if len(args) != 3 || args[2] != version {
			return fmt.Errorf("bootstrap version does not match this oo binary")
		}
		return m.Bootstrap(ctx, args[0], args[1], args[2])
	default:
		return fmt.Errorf("unknown command %q; run oo --help", command)
	}
	return fmt.Errorf("invalid arguments for %s; run oo --help", command)
}
