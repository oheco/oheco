package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

type runnerEvent struct {
	Type     string `json:"type"`
	PID      int    `json:"pid,omitempty"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// The runner is deliberately independent of the daemon's signal context. Its
// stdin is a liveness pipe owned only by the daemon. EOF initiates group cleanup;
// the per-service flock remains held until every managed group member is gone.
func runService(ctx context.Context, p paths, name string) (result error) {
	// Go otherwise terminates on EPIPE from stdout, skipping service cleanup
	// if the daemon dies between exec and the first startup report.
	signal.Ignore(syscall.SIGPIPE)
	syscall.CloseOnExec(4) // Pinned executable: never leak it into the target service.
	encoder := json.NewEncoder(os.Stdout)
	started := false
	defer func() {
		if result != nil && !started {
			_ = encoder.Encode(runnerEvent{Type: "error", ExitCode: -1, Error: result.Error()})
		}
	}()
	if err := validName(name); err != nil {
		return err
	}
	lock, err := inheritedLock(3, p.serviceLock(name))
	if err != nil {
		return err
	}
	defer lock.Close()
	var cfg Config
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 1024*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return fmt.Errorf("runner configuration: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	parentGone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(parentGone) }()
	// Adopt orphaned grandchildren so zombie processes do not retain the group.
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("enable child subreaper: %w", errno)
	}
	stdout, err := openRegular(filepath.Join(p.logs, name+".stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := openRegular(filepath.Join(p.logs, name+".stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer stderr.Close()
	select {
	case <-parentGone:
		return fmt.Errorf("daemon disconnected before service startup")
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	env := make(map[string]string, len(cfg.Environment)+1)
	for k, v := range cfg.Environment {
		env[k] = v
	}
	servicePath := os.Getenv("PATH")
	if value, ok := env["PATH"]; ok {
		servicePath = value
	}
	env["PATH"] = filepath.Join(p.root, "bin") + string(os.PathListSeparator) + servicePath
	command := cfg.Command[0]
	// exec.Command uses the caller's PATH, not Cmd.Env. Resolve using the service's
	// effective PATH, including its working directory for relative PATH entries.
	if !stringsContainSlash(command) {
		command, err = serviceExecutable(command, env["PATH"], cfg.WorkingDirectory)
		if err != nil {
			return err
		}
	}
	cmd := exec.Command(command, cfg.Command[1:]...)
	cmd.Dir = cfg.WorkingDirectory
	cmd.Env = replaceEnv(os.Environ(), env)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	// Pdeathsig is tied to the creating thread. Keep that thread alive for the
	// full lifetime of the service, rather than allowing the Go runtime to retire it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		return err
	}
	started = true
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	reportErr := encoder.Encode(runnerEvent{Type: "started", PID: cmd.Process.Pid})
	var waitErr error
	leaderDone := false
	if reportErr == nil {
		select {
		case waitErr = <-wait:
			leaderDone = true
		case <-parentGone:
		case <-ctx.Done():
		}
	}
	stopStarted := time.Now()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	deadline := time.NewTimer(duration(cfg.StopTimeout))
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	killed := false
	for {
		if leaderDone {
			reapChildren()
		}
		groupErr := syscall.Kill(-cmd.Process.Pid, 0)
		if leaderDone && errors.Is(groupErr, syscall.ESRCH) {
			break
		}
		if groupErr != nil && !errors.Is(groupErr, syscall.ESRCH) {
			return fmt.Errorf("inspect service process group: %w", groupErr)
		}
		select {
		case waitErr = <-wait:
			leaderDone = true
		case <-deadline.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			killed = true
		case <-tick.C:
			if killed {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	}
	// Children can create their own process groups without leaving our session.
	// As a subreaper we now own them; clean those groups before releasing the lock.
	grace := duration(cfg.StopTimeout) - time.Since(stopStarted)
	if grace < 0 {
		grace = 0
	}
	if err := cleanupSession(os.Getpid(), grace); err != nil {
		return err
	}
	code := cmd.ProcessState.ExitCode()
	message := ""
	if waitErr != nil {
		message = waitErr.Error()
	}
	_ = encoder.Encode(runnerEvent{Type: "exited", ExitCode: code, Error: message})
	return nil
}
func reapChildren() {
	for {
		pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}
func stringsContainSlash(value string) bool {
	for _, r := range value {
		if r == '/' {
			return true
		}
	}
	return false
}
func serviceExecutable(name, path, cwd string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = cwd
		} else if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && syscall.Access(candidate, 1) == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q not found in service PATH", name)
}
