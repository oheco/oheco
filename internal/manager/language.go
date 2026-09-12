package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/registry"
)

// Language delegates environment ownership to pip/npm. It intentionally does
// not create native oo receipts: their contents and removal belong to pip/npm.
func (m *Manager) Language(ctx context.Context, manager string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: oo %s <install|uninstall|list|show|check|freeze|ci> [arguments]", manager)
	}
	if err := validateLanguageArgs(manager, args); err != nil {
		return err
	}
	return m.withLock(func() error {
		idx, err := m.LoadIndex()
		if err != nil {
			return err
		}
		return m.languageLocked(ctx, idx, manager, args)
	})
}

func validateLanguageArgs(manager string, args []string) error {
	allowed := map[string]bool{"install": true, "uninstall": true, "list": true}
	if manager == "npm" {
		allowed["ci"] = true
		allowed["ls"] = true
		allowed["view"] = true
		allowed["outdated"] = true
		allowed["root"] = true
		allowed["prefix"] = true
	}
	if manager == "pip" {
		allowed["show"] = true
		allowed["check"] = true
		allowed["freeze"] = true
	}
	if !allowed[args[0]] {
		return fmt.Errorf("unsupported %s command %q", manager, args[0])
	}
	// Restrict settings that can redirect downloads, invoke arbitrary code or
	// hide URL dependencies in another file. Add supported options deliberately.
	values := map[string]bool{"--prefix": true, "--omit": true, "--include": true}
	flags := map[string]bool{"--global": true, "-g": true, "--save-dev": true, "-D": true, "--save-exact": true, "-E": true, "--no-save": true, "--json": true, "--depth=0": true, "--long": true, "--all": true, "--yes": true, "-y": true}
	if manager == "pip" {
		values = map[string]bool{}
		flags = map[string]bool{"--upgrade": true, "-U": true, "--force-reinstall": true, "--no-deps": true, "--user": true, "--yes": true, "-y": true, "--verbose": true, "-v": true, "--pre": true, "--format=json": true, "--format=freeze": true}
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		key, _, has := strings.Cut(a, "=")
		if values[key] {
			if !has {
				i++
				if i == len(args) {
					return fmt.Errorf("missing value for %s", key)
				}
			}
			continue
		}
		if flags[a] {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("option %s is not supported by oo %s", a, manager)
		}
		if manager == "npm" {
			name := a
			if n := strings.LastIndex(a, "@"); n > 0 {
				name = a[:n]
				if err := registry.ValidateNpmDependencies(map[string]any{"dependencies": map[string]any{name: a[n+1:]}}); err != nil {
					return err
				}
			}
			if !catalog.ValidEcosystemName("npm", name) {
				return fmt.Errorf("use an npm registry package name, not a path or URL: %s", a)
			}
		} else if strings.ContainsAny(a, "/:\\@\r\n") || strings.HasPrefix(a, ".") {
			return fmt.Errorf("pip requires a named package requirement, not a path or URL: %s", a)
		}
	}
	return nil
}

func languageEnv(manager string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "NPM_CONFIG_") || strings.HasPrefix(upper, "PIP_") || upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "NO_PROXY" {
			continue
		}
		env = append(env, entry)
	}
	// oo's HTTP client retains the caller's proxy. Its children only talk to
	// loopback; empty client configuration prevents extra indexes leaking in.
	env = append(env, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
	if manager == "pip" {
		env = append(env, "PIP_CONFIG_FILE="+os.DevNull, "PIP_DISABLE_PIP_VERSION_CHECK=1")
	}
	return env
}

func (m *Manager) languageLocked(ctx context.Context, idx catalog.Index, manager string, args []string) error {
	s, err := registry.Start(ctx, registry.Config{Index: idx, Platform: m.Platform, Cache: filepath.Join(m.Root, "cache", "language"), Client: m.Client})
	if err != nil {
		return err
	}
	defer s.Close()
	var cmd *exec.Cmd
	if manager == "npm" {
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		global := false
		for i, a := range args {
			if a == "--global" || a == "-g" {
				global = true
			}
			if a == "--prefix" && i+1 < len(args) {
				dir = args[i+1]
			}
			if strings.HasPrefix(a, "--prefix=") {
				dir = strings.TrimPrefix(a, "--prefix=")
			}
		}
		if !global {
			if err := checkNpmProject(dir); err != nil {
				return err
			}
		}
		config, err := os.CreateTemp(filepath.Join(m.Root, "tmp"), "npmrc-*")
		if err != nil {
			return err
		}
		config.Close()
		defer os.Remove(config.Name())
		argv := append([]string{}, args...)
		argv = append(argv, "--registry="+s.URL+"/npm/", "--userconfig="+config.Name(), "--globalconfig="+os.DevNull, "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier", "--omit-lockfile-registry-resolved", "--fetch-retries=0")
		cmd = exec.CommandContext(ctx, "npm", argv...)
		cmd.Env = languageEnv(manager)
		fmt.Fprintln(m.Out, "npm manages this installation; dependency lifecycle scripts are disabled (use prebuilt packages).")
	} else {
		python := os.Getenv("OHECO_PYTHON")
		if python == "" {
			python = "python3"
		}
		argv := append([]string{"-m", "pip"}, args...)
		if args[0] == "install" {
			argv = append(argv, "--index-url="+s.URL+"/simple/", "--only-binary=:all:")
		}
		cmd = exec.CommandContext(ctx, python, argv...)
		cmd.Env = languageEnv(manager)
		fmt.Fprintln(m.Out, "pip manages this installation in the selected Python environment (wheels only).")
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = m.Out
	cmd.Stderr = m.Out
	// Cancellation kills the entire process group so resolver children cannot
	// outlive the registry. pip/npm retain their usual interrupted-install semantics.
	// A new session also avoids SIGTTIN when npm touches inherited terminal
	// input: a bare Setpgid would create a background group in oo's session.
	// The inherited descriptors still carry prompts/input; oo forwards signals.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", manager, err)
	}
	return nil
}

func checkNpmProject(dir string) error {
	// A project .npmrc can override scoped registries even with --registry.
	// Reject such settings instead of silently installing an unadapted package.
	config, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(config), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(strings.ToLower(key))
		if strings.Contains(key, "registry") || strings.Contains(key, "proxy") || strings.Contains(key, "config") {
			return fmt.Errorf("project .npmrc setting %q can bypass oo; remove it for this installation", key)
		}
	}
	for _, name := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			return err
		}
		if err := registry.ValidateNpmDependencies(doc); err != nil {
			return err
		}
		if name == "package.json" {
			if err := registry.ValidateNpmDependencies(map[string]any{"dependencies": doc["devDependencies"]}); err != nil {
				return err
			}
			if doc["workspaces"] != nil {
				return fmt.Errorf("oo npm does not yet support workspace installation; use a standalone project")
			}
		}
		if err := checkLockURLs(doc); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func checkLockURLs(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "resolved" {
				if address, ok := item.(string); ok && !strings.HasPrefix(address, "https://registry.npmjs.org/") {
					return fmt.Errorf("lockfile contains a custom URL; regenerate it with oo npm install")
				}
			}
			if err := checkLockURLs(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := checkLockURLs(item); err != nil {
				return err
			}
		}
	}
	return nil
}
