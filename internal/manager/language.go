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

// languageEnv keeps the caller's package-manager configuration and proxy:
// oo no longer owns the download path, so npm/pip talk directly to the sources
// the user configured. Only options that can inject code into the tool itself
// are dropped, and loopback must bypass any proxy for oo's temporary source.
func languageEnv(backend string) []string {
	var env []string
	bypass := "127.0.0.1,localhost"
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if upper == "NODE_OPTIONS" {
			continue
		}
		if upper == "NO_PROXY" {
			if strings.TrimSpace(value) != "" {
				bypass += "," + value
			}
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "NO_PROXY="+bypass, "no_proxy="+bypass)
	if backend == "pip" {
		env = append(env, "PIP_DISABLE_PIP_VERSION_CHECK=1")
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
	// Registry and proxy settings are now honoured and forwarded, so they are
	// no longer rejected. Only options that can execute code inside npm itself
	// remain blocked.
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
		switch key {
		case "node-options", "script-shell", "onload-script":
			return fmt.Errorf("project .npmrc setting %q can execute code inside npm; remove it for this installation", key)
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
	// Any HTTPS source is acceptable now that oo forwards to the user's own
	// registry; only plaintext or malformed origins are rejected.
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "resolved" {
				if address, ok := item.(string); ok {
					// Any HTTPS source is acceptable now that oo forwards to the
					// user's own registry, but plaintext or loopback URLs are a
					// leftover from the old proxy model and cannot work.
					if err := catalog.ValidateURL(address); err != nil || !strings.HasPrefix(address, "https://") {
						return fmt.Errorf("lockfile contains an unusable source URL; regenerate it with oo npm install")
					}
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
