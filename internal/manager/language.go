package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/registry"
)

// Language preserves the legacy oo npm/pip entry points, including npm's local
// default. Unified oo install/remove use InstallLanguage/RemoveLanguage instead.
// Neither entry point writes oo receipts for environments owned by npm/pip.
func (m *Manager) Language(ctx context.Context, backend string, args []string) error {
	if err := validateLanguageArgs(backend, args); err != nil {
		return err
	}
	return m.withLock(func() error {
		var idx catalog.Index
		if languageNeedsRegistry(backend, args[0]) {
			var err error
			idx, err = m.LoadIndex()
			if err != nil {
				return err
			}
		}
		return m.languageLocked(ctx, idx, backend, args)
	})
}

func validateLanguageArgs(backend string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: oo %s <install|uninstall|list|show|check|freeze|ci> [arguments]; see also oo install/remove", backend)
	}
	allowed := map[string]bool{"install": true, "uninstall": true, "list": true}
	if backend == "npm" {
		for _, name := range []string{"ci", "ls", "view", "outdated", "root", "prefix"} {
			allowed[name] = true
		}
	} else if backend == "pip" {
		for _, name := range []string{"show", "check", "freeze"} {
			allowed[name] = true
		}
	} else {
		return fmt.Errorf("unsupported language backend %q", backend)
	}
	if !allowed[args[0]] {
		return fmt.Errorf("unsupported %s command %q", backend, args[0])
	}
	if err := validateLanguageOptions(backend, args[1:], true); err != nil {
		return err
	}
	if args[0] == "uninstall" {
		return validateLanguageRemovalOptions(backend, args[1:])
	}
	return nil
}

func languageEnv(backend string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "NPM_CONFIG_") || strings.HasPrefix(upper, "PIP_") || upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "NO_PROXY" || upper == "NODE_OPTIONS" {
			continue
		}
		env = append(env, entry)
	}
	// oo's HTTP client retains the caller's proxy; children only use loopback.
	env = append(env, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
	if backend == "pip" {
		env = append(env, "PIP_CONFIG_FILE="+os.DevNull, "PIP_DISABLE_PIP_VERSION_CHECK=1")
	}
	return env
}

func (m *Manager) languageLocked(ctx context.Context, idx catalog.Index, backend string, args []string) error {
	if err := validateLanguageArgs(backend, args); err != nil {
		return err
	}
	tool, err := m.languageTool(ctx, backend)
	if err != nil {
		return err
	}
	m.describeLanguage(tool)
	if args[0] == "uninstall" {
		fmt.Fprintln(m.Out, "Cross-root reverse dependency safety is not guaranteed; npm owns its tree and pip dependencies are retained.")
	}
	return m.runLanguage(ctx, idx, tool, args[0], nil, args[1:], false)
}

func checkNpmProject(dir string) error {
	// A project .npmrc can override scoped registries even with --registry.
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
