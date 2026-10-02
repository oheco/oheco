package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
)

// Upstream resolution: oo no longer downloads or verifies third-party bytes.
// It serves catalog metadata itself and forwards everything else to the
// sources the user configured in npmrc / pip.conf. These helpers read the
// *effective* configuration exactly as the child would see it, but without the
// oo-injected overrides.

// defaultPipIndex mirrors pip's own default when nothing is configured.
const defaultPipIndex = "https://pypi.org/simple"

type npmUpstream struct {
	Registry string            // default registry for unscoped names
	Scoped   map[string]string // "@scope" -> registry override
	Auth     []string          // hosts that appear to need credentials
	Sources  []string          // configuration files/env that supplied values (for messages)
}

type pipUpstream struct {
	Indexes []string // primary first, then extras, de-duplicated
	Auth    []string // indexes that appear to need credentials
	Sources []string
}

func (m *Manager) probe(ctx context.Context, program string, env []string, args ...string) ([]byte, error) {
	return languageProbe(ctx, program, env, args...)
}

// npmUpstream resolves the registry npm would use without oo's own --registry
// override, plus any per-scope overrides and credential hints.
func (m *Manager) npmUpstream(ctx context.Context, tool languageToolchain, options LanguageOptions) (npmUpstream, error) {
	var result npmUpstream
	// npm itself decides which npmrc files apply. In global mode a project
	// npmrc must not become the upstream for the temporary registry.
	scope := npmScopeArgs(options)
	out, err := m.probe(ctx, tool.program, tool.env, append([]string{"config", "get", "registry"}, scope...)...)
	if err != nil {
		return result, fmt.Errorf("read npm registry configuration: %w", err)
	}
	registry := strings.TrimSpace(string(out))
	if registry == "" {
		registry = "https://registry.npmjs.org"
	}
	if err := catalog.ValidateURL(registry); err != nil {
		return result, fmt.Errorf("configured npm registry is not a usable HTTPS URL: %s", registry)
	}
	result.Registry = registry

	// The merged configuration exposes scoped registries. npm deliberately hides
	// credential *values* from `config ls` (and refuses `config get` on them), so
	// credential hints are collected from key names in the config files and the
	// environment below.
	out, err = m.probe(ctx, tool.program, tool.env, append([]string{"config", "ls", "--json"}, scope...)...)
	if err != nil {
		return result, fmt.Errorf("read npm configuration: %w", err)
	}
	var config map[string]any
	if err := json.Unmarshal(out, &config); err != nil {
		return result, fmt.Errorf("parse npm configuration: %w", err)
	}
	result.Scoped = map[string]string{}
	for key, value := range config {
		text, ok := value.(string)
		if !ok || text == "" {
			continue
		}
		lower := strings.ToLower(key)
		if strings.HasSuffix(lower, ":registry") && strings.HasPrefix(key, "@") {
			scope := key[:strings.LastIndex(key, ":")]
			if err := catalog.ValidateURL(text); err == nil {
				result.Scoped[scope] = text
			}
		}
	}
	if len(result.Scoped) == 0 {
		result.Scoped = nil
	}
	global := !options.HasBackendArgs || config["global"] == true || config["location"] == "global"
	result.Auth = npmCredentialHosts(tool.env, config, global)
	return result, nil
}

// npmScopeArgs forwards only destination/mode flags to read-only config probes.
// Logging and arbitrary backend flags must not alter the probe's output format.
func npmScopeArgs(options LanguageOptions) []string {
	if !options.HasBackendArgs {
		return []string{"--global"}
	}
	var result []string
	for i := 0; i < len(options.Args); i++ {
		arg := options.Args[i]
		key, value, assigned := strings.Cut(arg, "=")
		// nopt also accepts exact long option names with one dash. Do not
		// mistake -loglevel/-ignore-scripts for clusters containing global -g.
		switch key {
		case "-global", "-no-global", "-local", "-no-local", "-location", "-prefix":
			key = "-" + key
			arg = key
			if assigned {
				arg += "=" + value
			}
		}
		switch key {
		case "--global", "--no-global", "--local", "--no-local", "-g":
			result = append(result, arg)
			if !assigned && i+1 < len(options.Args) && (options.Args[i+1] == "true" || options.Args[i+1] == "false") {
				i++
				result = append(result, options.Args[i])
			}
		case "--location", "--prefix", "-C", "-L":
			result = append(result, arg)
			if !assigned && i+1 < len(options.Args) {
				i++
				result = append(result, options.Args[i])
			}
		default:
			// npm only expands clusters consisting entirely of its one-letter
			// shorthands. Forwarding -v would stop the probe; normalize just -g.
			// C/L (value-taking scope shorthands) cannot be clustered: validation
			// requires separate -C/-L or the complete long scope option instead.
			if strings.HasPrefix(key, "-") && !strings.HasPrefix(key, "--") && strings.Contains(key, "g") && strings.Trim(strings.TrimPrefix(key, "-"), "adqsncfglmpSBDEOP?Hhvy") == "" {
				if assigned && strings.HasSuffix(key, "g") {
					result = append(result, "--global="+value)
				} else {
					result = append(result, "--global")
				}
			}
			if npmLanguageValues[key] && !assigned {
				i++
			}
		}
	}
	return result
}

// npmCredentialHosts reports which hosts appear to need credentials, by reading
// only the KEY NAMES of effective user/global (and, in local mode, project)
// npmrc files and the environment. npm
// hides credential values from `config ls`, and oo never needs them: file
// contents and values are neither copied nor printed.
func npmCredentialHosts(env []string, config map[string]any, global bool) []string {
	found := map[string]bool{}
	record := func(key string) {
		if !isNpmCredentialKey(key) {
			return
		}
		host := credentialHost(key)
		if host == "" {
			host = "(npm _auth)"
		}
		found[host] = true
	}
	var files []string
	home := ""
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if upper == "HOME" {
			home = value
		}
		if upper == "NPM_CONFIG_USERCONFIG" && value != "" {
			files = append(files, value)
		}
		if strings.HasPrefix(upper, "NPM_CONFIG_") {
			record(strings.TrimSpace(key))
		}
	}
	if userconfig, ok := config["userconfig"].(string); ok && userconfig != "" {
		files = []string{userconfig}
	} else if len(files) == 0 && home != "" {
		files = append(files, filepath.Join(home, ".npmrc"))
	}
	if globalconfig, ok := config["globalconfig"].(string); ok && globalconfig != "" {
		files = append(files, globalconfig)
	}
	if dir, err := os.Getwd(); err == nil && !global {
		for depth := 0; depth < 32; depth++ {
			files = append(files, filepath.Join(dir, ".npmrc"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, filename := range files {
		data, err := os.ReadFile(filename)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			key, _, _ := strings.Cut(line, "=")
			record(strings.TrimSpace(key))
		}
	}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "NPM_CONFIG_") {
			record(strings.TrimSpace(key))
		}
	}
	hosts := make([]string, 0, len(found))
	for host := range found {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func isNpmCredentialKey(key string) bool {
	lower := strings.ToLower(key)
	for _, suffix := range []string{":_authtoken", ":_auth", ":_password", ":username", ":certfile", ":keyfile"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return lower == "_auth" || lower == "_authtoken"
}

func credentialHost(key string) string {
	key = strings.TrimPrefix(key, "//")
	if at := strings.Index(key, "/"); at > 0 {
		return key[:at]
	}
	return ""
}

func pipScopeConflict(key string) error {
	return fmt.Errorf("default pip global scope conflicts with %s; unset the destination override or use -- to explicitly accept backend scope/configuration", key)
}

func falsePipUser(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

func checkDefaultPipEnvironment(env []string) error {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if value == "" {
			continue
		}
		switch key {
		case "PIP_USER":
			if falsePipUser(value) {
				continue
			}
		case "PIP_TARGET", "PIP_PREFIX", "PIP_ROOT", "PIP_PYTHON", "PYTHONPATH", "PYTHONHOME":
		default:
			continue
		}
		return pipScopeConflict(key)
	}
	return nil
}

// checkDefaultPipScope fails closed rather than silently redirecting a default
// global operation. It never edits pip.conf or the caller's environment. Explicit
// -- opts out of this check, including a bare -- or ordinary logging flags.
func (m *Manager) checkDefaultPipScope(ctx context.Context, tool languageToolchain, command string) error {
	if err := checkDefaultPipEnvironment(tool.env); err != nil {
		return err
	}
	// Use pip's own loader but not its CLI parser: even `pip config list`
	// honors global.python and can re-exec into a different environment before
	// reporting values. Read the selected interpreter's config without re-exec.
	// If this internal API is unavailable, fail closed; explicit -- opts out.
	out, err := m.probe(ctx, tool.program, tool.env, "-B", "-c", `import json; from pip._internal.configuration import Configuration; _oheco_pip_config=Configuration(isolated=False); _oheco_pip_config.load(); _oheco_pip_values={}; [_oheco_pip_values.update(v if isinstance(v,dict) else {k:v}) for k,v in _oheco_pip_config.items()]; print(json.dumps(_oheco_pip_values))`)
	if err != nil {
		return fmt.Errorf("read pip destination configuration without interpreter redirection (use -- to explicitly accept backend scope): %w", err)
	}
	var values map[string]string
	if err := json.Unmarshal(out, &values); err != nil {
		return fmt.Errorf("parse pip destination configuration: %w", err)
	}
	// The general parser consumes global.python before the command parser can
	// override it, so an empty install.python/uninstall.python cannot make it safe.
	if values["global.python"] != "" {
		return pipScopeConflict("global.python")
	}
	// Match pip's option precedence: global < command section < environment.
	for _, name := range []string{"user", "target", "prefix", "root", "python"} {
		key, value := "global."+name, values["global."+name]
		if v, ok := values[command+"."+name]; ok && v != "" {
			key, value = command+"."+name, v
		}
		if v, ok := values[":env:."+name]; ok && v != "" {
			key, value = "PIP_"+strings.ToUpper(name), v
		}
		// Inspect the environment too: config-list fixtures and older pip
		// versions may omit environment options from their output.
		for _, entry := range tool.env {
			k, v, _ := strings.Cut(entry, "=")
			if k == "PIP_"+strings.ToUpper(name) && v != "" {
				key, value = k, v
			}
		}
		if value == "" {
			continue
		}
		if name == "user" && falsePipUser(value) {
			continue
		}
		return pipScopeConflict(key)
	}
	return nil
}

// pipUpstream resolves every index pip would consult. extra-index-url values
// are aggregated by oo so that a single index is presented to pip; find-links
// cannot be aggregated safely and is rejected instead.
func (m *Manager) pipUpstream(ctx context.Context, tool languageToolchain) (pipUpstream, error) {
	var result pipUpstream
	out, err := m.probe(ctx, tool.program, tool.env, "-B", "-m", "pip", "config", "list")
	if err != nil {
		return result, fmt.Errorf("read pip configuration: %w", err)
	}
	primary, extras, findLinks := parsePipConfig(string(out))
	if len(findLinks) != 0 {
		return result, fmt.Errorf("oo only supports index-style pip sources; remove find-links from the pip configuration (%s)", strings.Join(findLinks, ", "))
	}
	seen := map[string]bool{}
	add := func(address string) {
		address = strings.TrimSpace(address)
		if address == "" || seen[address] {
			return
		}
		seen[address] = true
		result.Indexes = append(result.Indexes, address)
	}
	for _, address := range primary {
		add(address)
	}
	for _, address := range extras {
		add(address)
	}
	if len(result.Indexes) == 0 {
		result.Indexes = []string{defaultPipIndex}
	}
	for _, address := range result.Indexes {
		if err := catalog.ValidateURL(address); err != nil {
			return result, fmt.Errorf("configured pip index is not a usable HTTPS URL: %s", address)
		}
		if u, err := url.Parse(address); err == nil && u.User != nil {
			// Report the host only; never echo credentials from the URL.
			result.Auth = append(result.Auth, u.Hostname())
		}
	}
	sort.Strings(result.Auth)
	return result, nil
}

// parsePipConfig understands `pip config list` output, including the `:env:`
// prefix pip uses for environment-provided values.
func parsePipConfig(output string) (primary, extras, findLinks []string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "'\"")
		if value == "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(key))
		name = strings.TrimPrefix(name, ":env:.")
		name = strings.TrimPrefix(name, "global.")
		switch name {
		case "index-url":
			primary = append(primary, value)
		case "extra-index-url":
			extras = append(extras, value)
		case "find-links":
			findLinks = append(findLinks, value)
		}
	}
	return primary, extras, findLinks
}

// describeAnonymousUpstream reports the one limitation oo cannot fix: it never
// handles credentials, so authenticated sources are read anonymously.
func (m *Manager) describeAnonymousUpstream(backend string, hosts []string) {
	if len(hosts) == 0 {
		return
	}
	fmt.Fprintf(m.Out, "Warning: %s is configured with credentials for %s.\n", backend, strings.Join(hosts, ", "))
	fmt.Fprintf(m.Out, "oo only forwards metadata and cannot pass those credentials on, so those requests will be anonymous and may be rejected (401/403).\n")
}
