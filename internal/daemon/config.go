// Package daemon implements oo's user service manager without package-index access.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const protocol = 1

var errBusy = errors.New("daemon lock is held")
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	SchemaVersion    int               `json:"schema_version"`
	Description      string            `json:"description,omitempty"`
	Command          []string          `json:"command"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Restart          string            `json:"restart,omitempty"`
	RestartDelay     string            `json:"restart_delay,omitempty"`
	StopTimeout      string            `json:"stop_timeout,omitempty"`
	StartLimit       struct {
		Interval string `json:"interval,omitempty"`
		Burst    int    `json:"burst,omitempty"`
	} `json:"start_limit,omitempty"`
}

func (c *Config) validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	if len(c.Command) == 0 || c.Command[0] == "" {
		return fmt.Errorf("command must be a nonempty argv array")
	}
	for _, a := range c.Command {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("command contains NUL")
		}
	}
	if c.WorkingDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		c.WorkingDirectory = home
	}
	if !filepath.IsAbs(c.WorkingDirectory) || strings.ContainsRune(c.WorkingDirectory, 0) {
		return fmt.Errorf("working_directory must be an absolute path")
	}
	for k, v := range c.Environment {
		if !envPattern.MatchString(k) || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid environment variable %q", k)
		}
	}
	if c.Restart == "" {
		c.Restart = "on-failure"
	}
	if c.Restart != "no" && c.Restart != "on-failure" && c.Restart != "always" {
		return fmt.Errorf("restart must be no, on-failure, or always")
	}
	if c.RestartDelay == "" {
		c.RestartDelay = "2s"
	}
	if c.StopTimeout == "" {
		c.StopTimeout = "10s"
	}
	if c.StartLimit.Interval == "" {
		c.StartLimit.Interval = "30s"
	}
	if c.StartLimit.Burst == 0 {
		c.StartLimit.Burst = 5
	}
	for _, item := range []struct {
		name, value string
		min, max    time.Duration
	}{
		{"restart_delay", c.RestartDelay, 10 * time.Millisecond, time.Hour},
		{"stop_timeout", c.StopTimeout, time.Millisecond, 5 * time.Minute},
		{"start_limit.interval", c.StartLimit.Interval, time.Millisecond, 24 * time.Hour},
	} {
		d, err := time.ParseDuration(item.value)
		if err != nil || d < item.min || d > item.max {
			return fmt.Errorf("%s must be between %s and %s", item.name, item.min, item.max)
		}
	}
	if c.StartLimit.Burst < 1 || c.StartLimit.Burst > 10000 {
		return fmt.Errorf("start_limit.burst must be between 1 and 10000")
	}
	return nil
}
func duration(value string) time.Duration { d, _ := time.ParseDuration(value); return d }

type paths struct{ root, services, state, logs string }

func newPaths(root string) (paths, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return paths{}, err
		}
		root = filepath.Join(home, ".oheco")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return paths{}, err
	}
	if actual, e := filepath.EvalSymlinks(root); e == nil {
		root = actual
	} else if !errors.Is(e, os.ErrNotExist) {
		return paths{}, e
	}
	if root == string(filepath.Separator) {
		return paths{}, fmt.Errorf("OHECO_ROOT cannot be the filesystem root")
	}
	return paths{root, filepath.Join(root, "services"), filepath.Join(root, "services", ".state"), filepath.Join(root, "services", "logs")}, nil
}
func (p paths) prepare() error {
	if err := os.MkdirAll(p.root, 0755); err != nil {
		return err
	}
	for _, dir := range []string{p.services, p.state, p.logs} {
		if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("expected a real directory: %s", dir)
		}
	}
	return nil
}
func validName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid service name %q; use 1-64 lowercase letters, digits, dots, underscores or hyphens", name)
	}
	return nil
}
func (p paths) config(name string) (Config, error) {
	var c Config
	if err := validName(name); err != nil {
		return c, err
	}
	entries, err := os.ReadDir(p.services)
	if err != nil {
		return c, err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name+".json") && entry.Name() != name+".json" {
			return c, fmt.Errorf("service filename must be lowercase: %s", entry.Name())
		}
	}
	if err := readJSON(filepath.Join(p.services, name+".json"), &c); err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	return c, nil
}
func (p paths) names() ([]string, error) {
	entries, err := os.ReadDir(p.services)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, e := range entries {
		lower := strings.ToLower(e.Name())
		if strings.HasSuffix(lower, ".json") {
			name := strings.TrimSuffix(lower, ".json")
			if validName(name) == nil && !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// O_NOFOLLOW and the regular-file check also prevent locks/logs becoming pipes.
func openRegular(path string, flags int, mode os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("expected a regular file: %s", path)
	}
	return f, nil
}
func readJSON(path string, target any) error {
	f, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 1024*1024 {
		return fmt.Errorf("JSON file exceeds 1 MiB: %s", path)
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values in %s", path)
		}
		return err
	}
	return nil
}

// The fixed .next file is the persistent commit journal, protected by the
// corresponding state lock; it remains recoverable if a process is killed.
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	next := path + ".next"
	f, err := openRegular(next, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Truncate(0); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(next, path)
}
func tryLock(path string) (*os.File, error) {
	f, err := openRegular(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errBusy
		}
		return nil, err
	}
	return f, nil
}
func waitLock(ctx context.Context, path string, limit time.Duration) (*os.File, error) {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	for {
		f, err := tryLock(path)
		if !errors.Is(err, errBusy) {
			return f, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for previous service cleanup: %s", path)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func inheritedLock(fd uintptr, path string) (*os.File, error) {
	f := os.NewFile(fd, "inherited-lock")
	if f == nil {
		return nil, fmt.Errorf("missing inherited lock")
	}
	syscall.CloseOnExec(int(fd))
	actual, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	expected, err := os.Lstat(path)
	if err != nil {
		f.Close()
		return nil, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(actual, expected) {
		f.Close()
		return nil, fmt.Errorf("invalid inherited lock")
	}
	if err = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (p paths) daemonLock() string { return filepath.Join(p.state, "daemon.lock") }
func (p paths) serviceLock(name string) string {
	return filepath.Join(p.state, "service-"+name+".lock")
}

type enabledState struct {
	SchemaVersion int      `json:"schema_version"`
	Services      []string `json:"services"`
}

func (p paths) enabled() (map[string]bool, error) {
	state := enabledState{SchemaVersion: 1}
	err := readJSON(filepath.Join(p.state, "enabled.json"), &state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if state.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported enabled state schema %d", state.SchemaVersion)
	}
	result := map[string]bool{}
	for _, name := range state.Services {
		if err := validName(name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, nil
}
func (p paths) setEnabled(ctx context.Context, names []string, on bool) error {
	if err := p.prepare(); err != nil {
		return err
	}
	lock, err := waitLock(ctx, filepath.Join(p.state, "enabled.lock"), 5*time.Second)
	if err != nil {
		return err
	}
	defer lock.Close()
	for _, name := range names {
		if err := validName(name); err != nil {
			return err
		}
		if on {
			if _, err := p.config(name); err != nil {
				return err
			}
		}
	}
	enabled, err := p.enabled()
	if err != nil {
		return err
	}
	for _, name := range names {
		if on {
			enabled[name] = true
		} else {
			delete(enabled, name)
		}
	}
	state := enabledState{SchemaVersion: 1, Services: []string{}}
	for name := range enabled {
		state.Services = append(state.Services, name)
	}
	sort.Strings(state.Services)
	return writeJSON(filepath.Join(p.state, "enabled.json"), state)
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func replaceEnv(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if _, ok := values[k]; !ok {
			result = append(result, e)
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		result = append(result, k+"="+values[k])
	}
	return result
}
