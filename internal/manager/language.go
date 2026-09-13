package manager

import (
	"os"
	"strings"
)

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
