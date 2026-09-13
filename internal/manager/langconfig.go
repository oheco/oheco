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
func (m *Manager) npmUpstream(ctx context.Context, tool languageToolchain) (npmUpstream, error) {
	var result npmUpstream
	out, err := m.probe(ctx, tool.program, tool.env, "config", "get", "registry")
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
	out, err = m.probe(ctx, tool.program, tool.env, "config", "ls", "--json")
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
	result.Auth = npmCredentialHosts(tool.env)
	return result, nil
}

// npmCredentialHosts reports which hosts appear to need credentials, by reading
// only the KEY NAMES of the user/project npmrc files and the environment. npm
// hides credential values from `config ls`, and oo never needs them: file
// contents and values are neither copied nor printed.
func npmCredentialHosts(env []string) []string {
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
	if home != "" {
		files = append(files, filepath.Join(home, ".npmrc"))
	}
	if dir, err := os.Getwd(); err == nil {
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
