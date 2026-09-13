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

var version = "0.8.1"

const help = `oo — the oheco package manager

Usage:
  oo update                          Refresh the local package index
  oo search [query]                   Search the local package index
  oo info <package>                   Show available versions and package details
  oo install <package[@version]>... [--no-switch] [-y] [--dry-run] [-- backend-args]
  oo export <package[@version]> [project] [-o|--output directory]
  oo switch <package> <version>       Activate an installed version (offline)
  oo remove <package[@version]>... [--all] [--autoremove] [--cascade] [-y] [--dry-run] [-- backend-args]
  oo list                            List installed versions (* = active)
  oo recover                         Recover an interrupted operation
  oo pip <command> [arguments]        Run pip through oo's temporary source
  oo npm <command> [arguments]        Run npm through oo's temporary registry
  oo --version

install activates the selected version unless --no-switch is given.
export writes an editable project into the current directory by default.
The project name is optional when the version provides exactly one project.
Existing files and directories are not overwritten; export does not install.
remove without a version removes the active version; --all removes every version.
update refreshes metadata only. To update oo itself: oo update && oo install oheco
Commands also refresh the index in the background without waiting.
Native dependency plans are checked before installation; --yes accepts a reviewed
plan without prompting. --autoremove includes unused automatic native dependencies;
--cascade also removes native reverse dependents. Neither is implied by --yes.
Language packages use npm global / the selected base Python environment by default.
Arguments after -- are passed as argv to the one selected external manager; scope
options override the default. Options bypassing verified downloads are rejected.
Use npm:<name> / pip:<name> to explicitly select an ecosystem package.
<由 npm/pip 管理> denotes ownership, not an assertion that the package is installed.
Only native packages have oo receipts, versioned links and dependency cleanup.
npm/pip own their dependency resolution; oo does not guarantee external reverse-
dependency protection, and pip dependencies are retained on removal.
Only wheels and prebuilt npm packages are supported; npm scripts are disabled.
Legacy oo npm / oo pip remain available (oo npm keeps its original local default).

Environment:
  OHECO_ROOT        Installation root (default: ~/.oheco)
  OHECO_INDEX_URL   Index URL (default: https://oheco.org/index/v5/index.json)
  OHECO_NO_AUTO_UPDATE=1  Disable background index updates
  OHECO_PYTHON      Python executable for oo pip (default: python3 from PATH)
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:]); err != nil {
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
	if len(args) == 1 && args[0] == "--state-schema" {
		fmt.Println(2)
		return nil
	}
	m, err := manager.New(os.Getenv("OHECO_ROOT"), os.Getenv("OHECO_INDEX_URL"), os.Stdout)
	if err != nil {
		return err
	}
	command, args := args[0], args[1:]
	switch command {
	case "export":
		spec, project, destination, err := exportArgs(args)
		if err != nil {
			return err
		}
		return m.Export(ctx, spec, project, destination)
	case "pip", "npm":
		return m.Language(ctx, command, args)
	case "update":
		if len(args) != 0 {
			break
		}
		return m.Update(ctx)
	case "search":
		if len(args) > 1 {
			break
		}
		return m.Search(ctx, strings.Join(args, ""))
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
		specs, install, remove, err := operationArgs(command, args)
		if err != nil {
			return err
		}
		if command == "install" {
			return m.InstallMany(ctx, specs, install)
		}
		return m.RemoveMany(ctx, specs, remove)
	case "switch":
		if len(args) != 2 {
			break
		}
		return m.SwitchContext(ctx, args[0], args[1])
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
