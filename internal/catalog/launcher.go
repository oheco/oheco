package catalog

import (
	"fmt"
	"regexp"
	"strings"
)

// LauncherOptions applies only to the command's child process. Environment and
// Path contain paths relative to the package root, never shell expressions.
// Arguments may use ${PACKAGE_ROOT} to refer to that relocated root.
type LauncherOptions struct {
	Environment map[string]string `json:"environment,omitempty"`
	Path        []string          `json:"path,omitempty"`
	Interpreter string            `json:"interpreter,omitempty"`
	Arguments   []string          `json:"arguments,omitempty"`
}

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func (o LauncherOptions) Validate() error {
	if o.Interpreter != "" && o.Interpreter != "sh" {
		return fmt.Errorf("interpreter must be sh or omitted")
	}
	for name, value := range o.Environment {
		if !environmentName.MatchString(name) || name == "PATH" || name == "HOME" || name == "TMPDIR" || strings.HasPrefix(name, "XDG_") {
			return fmt.Errorf("invalid package-path environment variable %q", name)
		}
		if !SafePath(value) || strings.Split(value, "/")[0] == LauncherDirectory {
			return fmt.Errorf("invalid package-relative environment path %q", value)
		}
	}
	for _, value := range o.Path {
		if !SafePath(value) || strings.Split(value, "/")[0] == LauncherDirectory {
			return fmt.Errorf("invalid package-relative PATH entry %q", value)
		}
	}
	for _, value := range o.Arguments {
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("argument contains NUL")
		}
	}
	return nil
}
