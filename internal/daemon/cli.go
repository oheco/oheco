package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// ErrStatus requests exit status 1 after status output (or for --quiet), without
// an additional diagnostic from main.
var ErrStatus = errors.New("daemon status is not active")

const Help = `oo daemon — user service manager

Usage:
  oo daemon                         Ensure one detached daemon is running
  oo daemon --foreground            Run the daemon in the foreground
  oo daemon enable <service>...      Enable startup with the daemon
  oo daemon disable <service>...     Disable startup (does not stop a service)
  oo daemon start <service>...        Start services now
  oo daemon stop <service>...         Stop services and pending automatic restarts
  oo daemon restart <service>...      Stop and start services using current config
  oo daemon status [service...] [--quiet|--json]
  oo daemon list [--json]            List enabled and runtime state
  oo daemon shutdown                Stop all services and exit the daemon
  oo daemon autoload [--remove|--print]

Definitions: $OHECO_ROOT/services/<name>.json (default root: ~/.oheco).
Logs append to services/logs/<name>.stdout.log and <name>.stderr.log.
Enable/disable only change the next daemon startup; start/stop do not change it.
Run services in foreground mode; intentional daemonization is unsupported.
Autoload writes an asynchronous, disowned command into ${ZDOTDIR:-$HOME}/.zshrc.
Daemon commands never access the package index or launch automatic updates.
`

func Handles(args []string) bool {
	return len(args) > 0 && (args[0] == "daemon" || args[0] == "_daemon" || args[0] == "_service")
}
func Execute(ctx context.Context, args []string, out io.Writer, version string) error {
	p, err := newPaths(os.Getenv("OHECO_ROOT"))
	if err != nil {
		return err
	}
	if args[0] == "_daemon" {
		if len(args) != 1 {
			return fmt.Errorf("invalid internal daemon arguments")
		}
		return internalDaemon(ctx, p, version)
	}
	if args[0] == "_service" {
		if len(args) != 2 {
			return fmt.Errorf("invalid internal service arguments")
		}
		return runService(ctx, p, args[1])
	}
	args = args[1:]
	if len(args) == 0 {
		return startDaemon(ctx, p, false, version)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(out, Help)
		return nil
	}
	if len(args) == 1 && args[0] == "--foreground" {
		return startDaemon(ctx, p, true, version)
	}
	action, names := args[0], args[1:]
	switch action {
	case "autoload":
		mode := "install"
		if len(names) == 1 && names[0] == "--remove" {
			mode = "remove"
		} else if len(names) == 1 && names[0] == "--print" {
			mode = "print"
		} else if len(names) != 0 {
			return fmt.Errorf("usage: oo daemon autoload [--remove|--print]")
		}
		return Autoload(p.root, mode, out)
	case "enable", "disable":
		if len(names) == 0 {
			return fmt.Errorf("%s requires service names", action)
		}
		if err = p.setEnabled(ctx, names, action == "enable"); err != nil {
			return err
		}
		for _, name := range names {
			fmt.Fprintln(out, action+"d", name)
		}
		return nil
	case "start", "stop", "restart":
		if len(names) == 0 {
			return fmt.Errorf("%s requires service names", action)
		}
		_, err = call(p, action, names)
		return err
	case "status", "list":
		return showStatus(p, action, names, out)
	case "shutdown":
		if len(names) != 0 {
			return fmt.Errorf("shutdown takes no service names")
		}
		running, err := lockHeld(p)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		answer, err := call(p, "shutdown", nil)
		if err != nil {
			return err
		}
		limit := duration(answer.ShutdownWait)
		if limit == 0 {
			limit = 15 * time.Second
		}
		lock, err := waitLock(ctx, p.daemonLock(), limit)
		if err != nil {
			return err
		}
		return lock.Close()
	default:
		return fmt.Errorf("invalid daemon command %q; run oo daemon --help", action)
	}
}
func lockHeld(p paths) (bool, error) {
	lock, err := tryLock(p.daemonLock())
	if errors.Is(err, errBusy) {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lock.Close()
	return false, nil
}

type statusView struct {
	Running  bool     `json:"running"`
	PID      int      `json:"pid,omitempty"`
	Version  string   `json:"version,omitempty"`
	Services []Status `json:"services"`
	Error    string   `json:"error,omitempty"`
}

func showStatus(p paths, action string, args []string, out io.Writer) error {
	quiet, jsonOutput := false, false
	var names []string
	for _, arg := range args {
		switch arg {
		case "--quiet":
			quiet = true
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown status option %s", arg)
			}
			if err := validName(arg); err != nil {
				return err
			}
			names = append(names, arg)
		}
	}
	if quiet && jsonOutput {
		return fmt.Errorf("--quiet and --json are mutually exclusive")
	}
	if action == "list" && (quiet || len(names) != 0) {
		return fmt.Errorf("usage: oo daemon list [--json]")
	}
	querySpecific := len(names) > 0
	view := statusView{Services: []Status{}}
	running, err := lockHeld(p)
	if err != nil {
		if quiet {
			return ErrStatus
		}
		return err
	}
	if quiet && !running {
		return ErrStatus
	}
	if running {
		requestAction := "status"
		if quiet && len(names) == 0 {
			requestAction = "ping"
		}
		answer, err := call(p, requestAction, names)
		if err != nil {
			if quiet {
				return ErrStatus
			}
			if jsonOutput {
				view.Error = err.Error()
				if err := json.NewEncoder(out).Encode(view); err != nil {
					return err
				}
				return ErrStatus
			}
			return err
		}
		view.Running = true
		view.PID = answer.PID
		view.Version = answer.Version
		if answer.Services != nil {
			view.Services = answer.Services
		}
	} else {
		enabled, err := p.enabled()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			names, err = p.names()
			if err != nil {
				return err
			}
			set := map[string]bool{}
			for _, name := range names {
				set[name] = true
			}
			for name := range enabled {
				set[name] = true
			}
			names = names[:0]
			for name := range set {
				names = append(names, name)
			}
			sort.Strings(names)
		}
		for _, name := range names {
			cfg, err := p.config(name)
			status := Status{Name: name, Enabled: enabled[name], State: "inactive"}
			_ = cfg
			if err != nil {
				status.State = "failed"
				status.Error = err.Error()
			}
			view.Services = append(view.Services, status)
		}
	}
	active := view.Running
	if action == "status" && querySpecific {
		for _, s := range view.Services {
			if s.State != "running" {
				active = false
			}
		}
	}
	if !quiet {
		if jsonOutput {
			if err := json.NewEncoder(out).Encode(view); err != nil {
				return err
			}
		} else {
			if view.Running {
				fmt.Fprintf(out, "daemon running (pid %d, oo %s)\n", view.PID, view.Version)
			} else {
				fmt.Fprintln(out, "daemon stopped")
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tENABLED\tSTATE\tPID\tSTARTS\tDETAIL")
			for _, s := range view.Services {
				enabled := "no"
				if s.Enabled {
					enabled = "yes"
				}
				detail := s.Error
				if s.ExitCode != nil && detail == "" {
					detail = fmt.Sprintf("exit %d", *s.ExitCode)
				}
				detail = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(detail)
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", s.Name, enabled, s.State, s.PID, s.Starts, detail)
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
	}
	if action == "status" && !active {
		return ErrStatus
	}
	return nil
}
