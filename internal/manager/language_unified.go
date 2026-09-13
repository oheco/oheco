package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/registry"
)

// LanguageOptions applies to one backend operation. Args contains only the
// caller's options, not additional package targets or an argv separator. Its
// entries are passed unchanged and in order; oo adds its own safety settings.
type LanguageOptions struct {
	Args   []string
	Yes    bool
	DryRun bool
}

// MissingToolchainError lets callers offer the native toolchain without parsing
// human-readable subprocess errors.
type MissingToolchainError struct {
	Package string
	Reason  string
}

func (e *MissingToolchainError) Error() string {
	return fmt.Sprintf("%s toolchain is unavailable: %s", e.Package, e.Reason)
}

// CheckLanguage validates options and runnable tools without downloading an
// index, acquiring a lock, changing the environment, or printing a plan.
func (m *Manager) CheckLanguage(ctx context.Context, backend string, args []string) error {
	if err := validateLanguageOptions(backend, args); err != nil {
		return err
	}
	_, err := m.languageTool(ctx, backend)
	return err
}

// CheckLanguageInstall preflights a language install before a mixed
// native/language batch starts committing installations. Removal uses
// CheckLanguage; both are offline and only validate options and the runnable
// tool, because oo no longer infers or checks an npm installation scope.
func (m *Manager) CheckLanguageInstall(ctx context.Context, backend string, args []string) error {
	return m.CheckLanguage(ctx, backend, args)
}

// InstallLanguage installs ecosystem roots (catalog roots must be pinned by the
// caller; bare names allow the backend default for uncatalogued roots). The caller owns the manager
// lock. npm/pip alone resolve dependencies and own installation state; this
// method writes no receipts and does not claim to produce a complete plan.
func (m *Manager) InstallLanguage(ctx context.Context, idx catalog.Index, backend string, specs []string, options LanguageOptions) error {
	if err := validateLanguageTargets(backend, specs, true); err != nil {
		return err
	}
	if err := validateLanguageOptions(backend, options.Args); err != nil {
		return err
	}
	tool, err := m.languageTool(ctx, backend)
	if err != nil {
		return err
	}
	m.describeLanguage(tool)
	fmt.Fprintf(m.Out, "%s install roots: %s\n", backend, strings.Join(specs, ", "))
	fmt.Fprintln(m.Out, "The backend will resolve and install ecosystem dependencies; this is NOT a complete dependency plan. No oo installation records are written.")
	if options.DryRun {
		fmt.Fprintln(m.Out, "Dry run: no registry, downloads, installation, or dependency resolution was started.")
		m.printLanguageCommand(tool, "install", specs, options.Args)
		return nil
	}
	if err := m.confirmContext(ctx, "Allow "+backend+" to resolve dependencies and install these roots?", options.Yes); err != nil {
		return err
	}
	return m.runLanguage(ctx, idx, tool, "install", specs, options.Args)
}

// RemoveLanguage never loads an index or starts a registry. The caller owns the
// lock and maps names using local information. Cross-root reverse dependency
// safety is NOT guaranteed. npm owns its tree; pip leaves dependencies installed.
func (m *Manager) RemoveLanguage(ctx context.Context, backend string, names []string, options LanguageOptions) error {
	if err := validateLanguageTargets(backend, names, false); err != nil {
		return err
	}
	if err := validateLanguageOptions(backend, options.Args); err != nil {
		return err
	}
	if err := validateLanguageRemovalOptions(backend, options.Args); err != nil {
		return err
	}
	tool, err := m.languageTool(ctx, backend)
	if err != nil {
		return err
	}
	m.describeLanguage(tool)
	fmt.Fprintf(m.Out, "%s remove roots: %s\n", backend, strings.Join(names, ", "))
	warning := "Cross-root reverse dependency safety is not guaranteed; npm owns removal of its dependency tree."
	if backend == "pip" {
		warning = "Cross-root reverse dependency safety is not guaranteed; pip dependencies are retained (only the named packages are removed)."
	}
	fmt.Fprintln(m.Out, warning)
	if options.DryRun {
		fmt.Fprintln(m.Out, "Dry run: no registry or uninstall was started; the backend environment is unchanged.")
		m.printLanguageCommand(tool, "uninstall", names, options.Args)
		return nil
	}
	if err := m.confirmContext(ctx, warning+" Continue with "+backend+" uninstall?", options.Yes); err != nil {
		return err
	}
	return m.runLanguage(ctx, catalog.Index{}, tool, "uninstall", names, options.Args)
}

var exactNpmVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func validateLanguageTargets(backend string, targets []string, install bool) error {
	if backend != "npm" && backend != "pip" {
		return fmt.Errorf("unsupported language backend %q", backend)
	}
	if len(targets) == 0 {
		return fmt.Errorf("%s requires at least one explicit package target", backend)
	}
	for _, target := range targets {
		name, version := target, ""
		if install && catalog.ValidEcosystemName(backend, target) {
			// An explicitly selected, uncatalogued backend package can ask npm/pip
			// for its default version. Catalog roots arrive pinned by the caller.
			continue
		}
		if install {
			if backend == "npm" {
				if at := strings.LastIndex(target, "@"); at > 0 {
					name, version = target[:at], target[at+1:]
				}
				if !exactNpmVersion.MatchString(version) {
					return fmt.Errorf("npm root %q must use an exact name@version", target)
				}
			} else {
				name, version, _ = strings.Cut(target, "==")
				if !catalog.ValidComponent(version) {
					return fmt.Errorf("pip root %q must use an exact name==version", target)
				}
			}
		}
		if !catalog.ValidEcosystemName(backend, name) {
			return fmt.Errorf("%s requires an ecosystem package name, not a URL, path, or versioned uninstall target: %q", backend, target)
		}
	}
	return nil
}

// Value-taking options, NOT an allowlist of flags. Unknown ordinary flags reach
// the backend unchanged. Bare arguments are forbidden because they could add
// roots outside the user's plan; unknown values can use --option=value.
var npmLanguageValues = map[string]bool{
	"--prefix": true, "-C": true, "--location": true, "--omit": true, "--include": true,
	"--depth": true, "--loglevel": true, "--logs-dir": true, "--logs-max": true,
	"--cache": true, "--tag": true, "--install-strategy": true,
	"--before": true, "--cpu": true, "--os": true, "--libc": true,
	"--fetch-retries": true, "--fetch-retry-factor": true, "--fetch-retry-mintimeout": true,
	"--fetch-retry-maxtimeout": true, "--fetch-timeout": true, "--maxsockets": true,
	"--save-prefix": true, "--heading": true,
}
var pipLanguageValues = map[string]bool{
	"--target": true, "-t": true, "--prefix": true, "--root": true,
	"--timeout": true, "--retries": true, "--resume-retries": true,
	"--cache-dir": true, "--log": true, "--report": true, "--progress-bar": true,
	"--root-user-action": true, "--upgrade-strategy": true, "--platform": true,
	"--python-version": true, "--implementation": true, "--abi": true,
	"--exists-action": true, "--keyring-provider": true, "--use-feature": true,
	"--use-deprecated": true, "--format": true, "--exclude": true, "--path": true,
}

func blockedLanguageOption(backend, key string) bool {
	if key == "--" || key == "-" {
		return true
	}
	if !strings.HasPrefix(key, "--") {
		// pip accepts attached short-option values and short-option clusters.
		if backend == "pip" && strings.HasPrefix(key, "-") && strings.ContainsAny(strings.TrimPrefix(key, "-"), "irefcC") {
			return true
		}
		return backend == "npm" && (strings.Contains(strings.TrimPrefix(key, "-"), "w") || key == "-r" || key == "-c" || (strings.HasPrefix(key, "-C") && key != "-C"))
	}
	name := strings.ToLower(strings.TrimPrefix(key, "--"))
	name = strings.TrimPrefix(name, "no-")
	blocked := []string{"registry", "userconfig", "globalconfig", "ignore-scripts", "script-shell", "node-options", "onload-script", "workspace", "workspaces", "include-workspace-root", "proxy", "https-proxy", "noproxy", "audit", "fund", "update-notifier", "omit-lockfile-registry-resolved"}
	if backend == "pip" {
		blocked = []string{"index-url", "extra-index-url", "index", "find-links", "requirement", "constraint", "build-constraint", "editable", "only-binary", "binary", "config-settings", "global-option", "install-option", "build-option", "use-pep517", "build-isolation", "python", "isolated", "proxy", "trusted-host", "cert", "client-cert"}
	}
	for _, setting := range blocked {
		if name == setting {
			return true
		}
	}
	// Do not mistake complete ordinary options for abbreviations of a blocked
	// option (notably --global versus --globalconfig, or --include versus
	// --include-workspace-root).
	known := npmLanguageValues["--"+name] || name == "global" || name == "local"
	if backend == "pip" {
		known = pipLanguageValues["--"+name]
	}
	if !known {
		for _, setting := range blocked {
			if name != "" && strings.HasPrefix(setting, name) {
				return true
			}
		}
		// Unrecognized scope abbreviations are still rejected so that the
		// forwarded argv is exactly what the user wrote; oo no longer infers a
		// scope, but it must not silently complete a misspelled destination.
		if backend == "npm" && name != "" {
			for _, setting := range []string{"global", "location", "prefix"} {
				if strings.HasPrefix(setting, name) {
					return true
				}
			}
		}
	}
	return backend == "npm" && (strings.Contains(name, "registry") || strings.Contains(name, "config"))
}

func safeLanguageProtectionArg(backend, arg string) bool {
	if backend == "pip" {
		return arg == "--only-binary=:all:"
	}
	switch arg {
	case "--ignore-scripts", "--ignore-scripts=true", "--no-audit", "--audit=false", "--no-fund", "--fund=false", "--no-update-notifier", "--update-notifier=false", "--omit-lockfile-registry-resolved", "--omit-lockfile-registry-resolved=true":
		return true
	}
	return false
}

func validateLanguageOptions(backend string, args []string) error {
	if backend != "npm" && backend != "pip" {
		return fmt.Errorf("unsupported language backend %q", backend)
	}
	values := npmLanguageValues
	if backend == "pip" {
		values = pipLanguageValues
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("invalid NUL in %s argument", backend)
		}
		if !strings.HasPrefix(a, "-") {
			return fmt.Errorf("unexpected backend value %q; package targets belong before --; use --option=value for unknown value-taking options", a)
		}
		key, _, assigned := strings.Cut(a, "=")
		effective := a
		if backend == "npm" && !assigned && i+1 < len(args) && (args[i+1] == "true" || args[i+1] == "false") {
			switch key {
			case "--global", "-g", "--no-global", "--ignore-scripts", "--audit", "--fund", "--update-notifier", "--omit-lockfile-registry-resolved":
				i++
				effective += "=" + args[i]
			}
		}
		if safeLanguageProtectionArg(backend, effective) {
			continue
		}
		if blockedLanguageOption(backend, key) {
			return fmt.Errorf("%s option %q can bypass the controlled registry, source/script restrictions, or explicit target plan; it is not allowed (select Python with OHECO_PYTHON)", backend, a)
		}
		if values[key] && !assigned {
			i++
			if i == len(args) || strings.HasPrefix(args[i], "-") || strings.ContainsRune(args[i], 0) {
				return fmt.Errorf("%s requires a separate non-option value (or use %s=value)", key, key)
			}
		}
	}
	return nil
}

func validateLanguageRemovalOptions(backend string, args []string) error {
	if backend == "npm" {
		for _, arg := range args {
			key, value, has := strings.Cut(strings.ToLower(arg), "=")
			if strings.HasPrefix(key, "--no-off") || (strings.HasPrefix(key, "--off") && has && value != "true") {
				return fmt.Errorf("npm uninstall is offline; %q would bypass that protection", arg)
			}
		}
	}
	return nil
}

type languageToolchain struct {
	backend string
	program string
	env     []string
	note    string
}

func (m *Manager) languageTool(ctx context.Context, backend string) (languageToolchain, error) {
	t := languageToolchain{backend: backend, env: languageEnv(backend)}
	missing := func(pkg string, err error) (languageToolchain, error) {
		if ctx.Err() != nil {
			return t, ctx.Err()
		}
		return t, &MissingToolchainError{Package: pkg, Reason: err.Error()}
	}
	if backend == "npm" {
		node, err := m.findLanguageExecutable("node")
		if err != nil {
			return missing("nodejs", fmt.Errorf("node: %w", err))
		}
		t.env = prependLanguagePath(t.env, filepath.Dir(node))
		if _, err := languageProbe(ctx, node, t.env, "--version"); err != nil {
			return missing("nodejs", fmt.Errorf("node is not runnable: %w", err))
		}
		t.program, err = m.findLanguageExecutable("npm")
		if err != nil {
			return missing("nodejs", fmt.Errorf("npm: %w", err))
		}
		// Keep npm's version fast path: configuration flags would load files,
		// and using /dev/null for both config levels causes a double-load error.
		if _, err := languageProbe(ctx, t.program, t.env, "--version"); err != nil {
			return missing("nodejs", fmt.Errorf("npm is not runnable with node: %w", err))
		}
		return t, nil
	}
	if backend != "pip" {
		return t, fmt.Errorf("unsupported language backend %q", backend)
	}
	python := os.Getenv("OHECO_PYTHON")
	explicit := python != ""
	var err error
	if explicit {
		t.program, err = exec.LookPath(python)
		t.note = "explicit OHECO_PYTHON"
	} else {
		t.program, err = m.findLanguageExecutable("python3")
		t.note = "base Python environment (not an activated virtualenv)"
	}
	if err != nil {
		return missing("python3", fmt.Errorf("Python interpreter: %w", err))
	}
	info, err := inspectLanguagePython(ctx, t.program, t.env)
	if err != nil {
		return missing("python3", err)
	}
	if !explicit && info.Prefix != info.BasePrefix {
		activated := t.program
		if info.BaseExecutable == "" || info.BaseExecutable == info.Executable {
			return missing("python3", fmt.Errorf("%s is a virtualenv with no usable base interpreter; choose OHECO_PYTHON explicitly", activated))
		}
		t.program, err = exec.LookPath(info.BaseExecutable)
		if err != nil {
			return missing("python3", fmt.Errorf("base interpreter for %s: %w", activated, err))
		}
		info, err = inspectLanguagePython(ctx, t.program, t.env)
		if err != nil || info.Prefix != info.BasePrefix {
			return missing("python3", fmt.Errorf("cannot verify a non-virtualenv base interpreter for %s (set OHECO_PYTHON explicitly): %v", activated, err))
		}
		t.note = "activated virtualenv bypassed; using its base interpreter (set OHECO_PYTHON to explicitly select a venv)"
	}
	if explicit && info.Prefix != info.BasePrefix {
		t.note += "; explicitly selected virtualenv"
	}
	if _, err := languageProbe(ctx, t.program, t.env, "-B", "-m", "pip", "--version"); err != nil {
		return missing("python3", fmt.Errorf("%s -m pip is unavailable: %w", t.program, err))
	}
	return t, nil
}

func (m *Manager) findLanguageExecutable(name string) (string, error) {
	active := filepath.Join(m.Root, "bin", name)
	if _, err := os.Stat(active); err == nil {
		return exec.LookPath(active)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return exec.LookPath(name)
}

func prependLanguagePath(env []string, dir string) []string {
	result := append([]string{}, env...)
	for i, entry := range result {
		if strings.HasPrefix(entry, "PATH=") {
			result[i] = "PATH=" + dir + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
			return result
		}
	}
	return append(result, "PATH="+dir)
}

type languagePythonInfo struct {
	Executable     string `json:"executable"`
	BaseExecutable string `json:"base_executable"`
	Prefix         string `json:"prefix"`
	BasePrefix     string `json:"base_prefix"`
}

func inspectLanguagePython(ctx context.Context, python string, env []string) (languagePythonInfo, error) {
	var info languagePythonInfo
	output, err := languageProbe(ctx, python, env, "-B", "-c", `import json,sys; print(json.dumps({"executable":sys.executable,"base_executable":getattr(sys,"_base_executable",sys.executable),"prefix":sys.prefix,"base_prefix":getattr(sys,"real_prefix",getattr(sys,"base_prefix",sys.prefix))}))`)
	if err != nil {
		return info, fmt.Errorf("Python interpreter %s is not runnable: %w", python, err)
	}
	if err := json.Unmarshal(output, &info); err != nil || info.Prefix == "" || info.BasePrefix == "" || info.Executable == "" {
		return info, fmt.Errorf("cannot inspect Python environment for %s", python)
	}
	return info, nil
}

func languageProbe(ctx context.Context, program string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = env
	configureLanguageCancellation(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func configureLanguageCancellation(cmd *exec.Cmd) {
	// A new session avoids SIGTTIN on inherited terminal descriptors and keeps
	// resolver children from outliving the temporary registry on cancellation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
}

func (m *Manager) describeLanguage(tool languageToolchain) {
	if tool.backend == "npm" {
		fmt.Fprintln(m.Out, "npm owns this installation and dependency tree; lifecycle scripts are disabled (prebuilt packages only).")
	} else {
		fmt.Fprintf(m.Out, "pip interpreter: %s (%s); pip owns the environment; wheels only.\n", tool.program, tool.note)
	}
}

func languageNeedsRegistry(command string) bool {
	return command == "install"
}

// scopes lists the npm scopes oo serves from its own catalog, so adapted
// scoped packages always come from oo even when the user configured an
// override for that scope; other names in those scopes are forwarded on.
func catalogScopes(idx catalog.Index) []string {
	seen := map[string]bool{}
	var scopes []string
	for _, p := range idx.Packages {
		if p.Manager() != "npm" || !strings.HasPrefix(p.PackageName, "@") {
			continue
		}
		scope, _, ok := strings.Cut(p.PackageName, "/")
		if !ok || seen[scope] {
			continue
		}
		seen[scope] = true
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

// languageCommand never changes the installation scope: every user argument is
// forwarded unchanged and oo only adds the settings it needs to serve its own
// metadata and to keep npm from rewriting resolved hosts. npm therefore keeps
// its own default (local) unless the user passes --global after --.
func languageCommand(tool languageToolchain, command string, targets, args []string, registryURL string, scopes []string) []string {
	var argv []string
	if tool.backend == "pip" {
		argv = []string{"-B", "-m", "pip"}
	}
	argv = append(argv, command)
	argv = append(argv, targets...)
	argv = append(argv, args...)
	switch tool.backend {
	case "npm":
		// replace-registry-host=never is required: npm's default rewrites
		// tarball hosts to the configured registry (oo), which only serves
		// metadata and would answer 404.
		argv = append(argv, "--ignore-scripts")
		if command == "install" {
			argv = append(argv, "--omit-lockfile-registry-resolved", "--replace-registry-host=never")
			if registryURL != "" {
				argv = append(argv, "--registry="+registryURL+"/npm/")
				for _, scope := range scopes {
					argv = append(argv, "--"+scope+":registry="+registryURL+"/npm/")
				}
			}
		} else {
			// Uninstall can otherwise fetch metadata while rebuilding local trees.
			argv = append(argv, "--offline")
		}
	case "pip":
		if command == "install" {
			argv = append(argv, "--index-url="+registryURL+"/simple/", "--only-binary=:all:")
		} else if command == "uninstall" {
			// Unified oo already confirmed the removal once.
			argv = append(argv, "--yes")
		}
	}
	return argv
}

func (m *Manager) printLanguageCommand(tool languageToolchain, command string, targets, args []string) {
	url := ""
	if languageNeedsRegistry(command) {
		url = "<temporary-verified-registry>"
	}
	argv := append([]string{tool.program}, languageCommand(tool, command, targets, args, url, nil)...)
	fmt.Fprintf(m.Out, "Backend argv: %q\n", argv)
}

func (m *Manager) runLanguage(ctx context.Context, idx catalog.Index, tool languageToolchain, command string, targets, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	url := ""
	var scopes []string
	if languageNeedsRegistry(command) {
		cfg := registry.Config{Index: idx, Platform: m.Platform, Client: m.Client}
		switch tool.backend {
		case "npm":
			upstream, err := m.npmUpstream(ctx, tool)
			if err != nil {
				return err
			}
			cfg.NpmRegistry, cfg.NpmScoped = upstream.Registry, upstream.Scoped
			m.describeAnonymousUpstream("npm", upstream.Auth)
		case "pip":
			upstream, err := m.pipUpstream(ctx, tool)
			if err != nil {
				return err
			}
			cfg.PipIndexes = upstream.Indexes
			m.describeAnonymousUpstream("pip", upstream.Auth)
		}
		s, err := registry.Start(ctx, cfg)
		if err != nil {
			return err
		}
		defer s.Close()
		url = s.URL
		scopes = catalogScopes(idx)
	}
	cmd := exec.CommandContext(ctx, tool.program, languageCommand(tool, command, targets, args, url, scopes)...)
	cmd.Env = tool.env
	cmd.Stdin = m.In
	cmd.Stdout, cmd.Stderr = m.Out, m.Out
	configureLanguageCancellation(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", tool.backend, err)
	}
	return nil
}
