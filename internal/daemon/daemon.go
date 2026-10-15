package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type endpoint struct {
	Protocol int    `json:"protocol"`
	Instance string `json:"instance"`
	PID      int    `json:"pid"`
	Socket   string `json:"socket"`
	Root     string `json:"root"`
	Version  string `json:"version"`
}
type request struct {
	Protocol int      `json:"protocol"`
	Instance string   `json:"instance"`
	Action   string   `json:"action"`
	Names    []string `json:"names,omitempty"`
}
type response struct {
	Protocol     int      `json:"protocol"`
	Instance     string   `json:"instance"`
	PID          int      `json:"pid"`
	Version      string   `json:"version"`
	Services     []Status `json:"services,omitempty"`
	Error        string   `json:"error,omitempty"`
	ShutdownWait string   `json:"shutdown_wait,omitempty"`
}
type readyMessage struct {
	Error string `json:"error,omitempty"`
}

func (p paths) endpointPath() string { return filepath.Join(p.state, "endpoint.json") }
func (p paths) diagnostic() (*os.File, error) {
	return openRegular(filepath.Join(p.state, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
}

func startDaemon(ctx context.Context, p paths, foreground bool, version string) (result error) {
	if err := p.prepare(); err != nil {
		return err
	}
	canonical, err := newPaths(p.root)
	if err != nil {
		return err
	}
	p = canonical
	lock, err := tryLock(p.daemonLock())
	if errors.Is(err, errBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	diagnostic, err := p.diagnostic()
	if err != nil {
		return err
	}
	defer diagnostic.Close()
	defer func() {
		if result != nil {
			fmt.Fprintf(diagnostic, "%s startup: %v\n", time.Now().Format(time.RFC3339), result)
		}
	}()
	if foreground {
		return serve(ctx, p, lock, nil, diagnostic, version)
	}
	// Keep the signed executable open across upgrades/removal while handing it to
	// the detached process. Only explicitly listed descriptors reach exec.
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer executable.Close()
	readReady, writeReady, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readReady.Close()
	cmd := exec.Command("/proc/self/fd/5", "_daemon")
	cmd.Env = replaceEnv(os.Environ(), map[string]string{"OHECO_ROOT": p.root})
	cmd.ExtraFiles = []*os.File{lock, writeReady, executable}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = diagnostic
	cmd.Stderr = diagnostic
	if err = cmd.Start(); err != nil {
		writeReady.Close()
		return err
	}
	writeReady.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := make(chan error, 1)
	go func() {
		var message readyMessage
		err := json.NewDecoder(readReady).Decode(&message)
		if err == nil && message.Error != "" {
			err = errors.New(message.Error)
		}
		ready <- err
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err = <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			<-done
		}
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("daemon initialization timed out; see %s", diagnostic.Name())
	}
}

func internalDaemon(ctx context.Context, p paths, version string) (result error) {
	syscall.CloseOnExec(4)
	syscall.CloseOnExec(5)
	ready := os.NewFile(4, "daemon-ready")
	if ready == nil {
		return fmt.Errorf("missing startup pipe")
	}
	defer ready.Close()
	defer func() {
		message := readyMessage{}
		if result != nil {
			message.Error = result.Error()
		}
		_ = json.NewEncoder(ready).Encode(message)
	}()
	info, err := ready.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("invalid startup pipe")
	}
	lock, err := inheritedLock(3, p.daemonLock())
	if err != nil {
		return err
	}
	defer lock.Close()
	diagnostic, err := p.diagnostic()
	if err != nil {
		return err
	}
	defer diagnostic.Close()
	return serve(ctx, p, lock, ready, diagnostic, version)
}

func serve(parent context.Context, p paths, lock, ready, diagnostic *os.File, version string) (result error) {
	_ = lock // Kept live by the caller until all service stop requests complete.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if err := enableSubreaper(); err != nil {
		return err
	}
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer executable.Close()
	// Winning the global lock makes the previous published endpoint stale.
	p.cleanupOldEndpoint()
	// The runtime directory must really honor permissions (HOME/hmdfs does not).
	if !filepath.IsAbs(os.TempDir()) {
		return fmt.Errorf("TMPDIR must be an absolute application-private path")
	}
	runtimeDir, err := os.MkdirTemp(os.TempDir(), "ood-")
	if err != nil {
		return err
	}
	defer func() {
		actual, e := filepath.EvalSymlinks(runtimeDir)
		base, b := filepath.EvalSymlinks(os.TempDir())
		if e == nil && b == nil && filepath.Dir(actual) == base && filepath.Base(actual) == filepath.Base(runtimeDir) {
			_ = os.RemoveAll(actual)
		}
	}()
	if err = privateDirectory(runtimeDir); err != nil {
		return err
	}
	socketPath := filepath.Join(runtimeDir, "control.sock")
	if len(socketPath) > 103 {
		return fmt.Errorf("TMPDIR is too long for a Unix socket: %s", os.TempDir())
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socketPath, 0600); err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	ep := endpoint{Protocol: protocol, Instance: id, PID: os.Getpid(), Socket: socketPath, Root: p.root, Version: version}
	if err = writeJSON(p.endpointPath(), ep); err != nil {
		return err
	}
	defer func() {
		var saved endpoint
		if readJSON(p.endpointPath(), &saved) == nil && saved.Instance == id {
			_ = os.Remove(p.endpointPath())
		}
	}()
	c := &controller{ctx: ctx, cancel: cancel, paths: p, self: executable, diagnostic: diagnostic, services: map[string]*service{}}
	defer func() { cancel(); c.stopAll() }()
	// enabled.lock makes the startup snapshot consistent with offline enable/disable.
	configLock, err := waitLock(ctx, filepath.Join(p.state, "enabled.lock"), 5*time.Second)
	if err != nil {
		return err
	}
	err = c.startEnabled()
	configLock.Close()
	if err != nil {
		return err
	}
	if ready != nil {
		if err = json.NewEncoder(ready).Encode(readyMessage{}); err != nil {
			return err
		}
		ready.Close()
	}
	fmt.Fprintf(diagnostic, "%s daemon %s started pid=%d\n", time.Now().Format(time.RFC3339), version, os.Getpid())
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go c.handle(connection, ep)
	}
}

// Called only while holding the root's singleton lock. Remove only the known
// socket and its empty private directory, never recursively delete a locator's path.
func (p paths) cleanupOldEndpoint() {
	var old endpoint
	if readJSON(p.endpointPath(), &old) != nil || old.Protocol != protocol {
		return
	}
	actual, a := os.Stat(p.root)
	expected, b := os.Stat(old.Root)
	if a != nil || b != nil || !os.SameFile(actual, expected) {
		return
	}
	dir := filepath.Dir(old.Socket)
	if filepath.Base(old.Socket) != "control.sock" || !strings.HasPrefix(filepath.Base(dir), "ood-") || privateDirectory(dir) != nil {
		return
	}
	if info, err := os.Lstat(old.Socket); err == nil && info.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(old.Socket)
	}
	_ = os.Remove(dir)
}
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("runtime directory must be private (0700); use an application-private TMPDIR: %s", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("runtime directory belongs to another user: %s", path)
	}
	return nil
}
func (c *controller) handle(conn net.Conn, ep endpoint) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var req request
	decoder := json.NewDecoder(io.LimitReader(conn, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return
	}
	answer := response{Protocol: protocol, Instance: ep.Instance, PID: ep.PID, Version: ep.Version}
	var result error
	if req.Protocol != protocol || req.Instance != ep.Instance {
		result = fmt.Errorf("daemon protocol or instance mismatch")
	} else if len(req.Names) > 256 {
		result = fmt.Errorf("too many services")
	} else {
		for _, name := range req.Names {
			if err := validName(name); err != nil {
				result = err
				break
			}
		}
		if result == nil {
			switch req.Action {
			case "ping":
				if len(req.Names) != 0 {
					result = fmt.Errorf("ping takes no service names")
				}
			case "status", "list":
				answer.Services, result = c.snapshot(req.Names)
			case "start", "stop", "restart":
				if len(req.Names) == 0 {
					result = fmt.Errorf("%s requires service names", req.Action)
					break
				}
				var all []error
				for _, name := range req.Names {
					var err error
					switch req.Action {
					case "start":
						err = c.start(name)
					case "stop":
						err = c.stop(name)
					case "restart":
						err = c.restart(name)
					}
					if err != nil {
						all = append(all, fmt.Errorf("%s: %w", name, err))
					}
				}
				result = errors.Join(all...)
			case "shutdown":
				if len(req.Names) != 0 {
					result = fmt.Errorf("shutdown takes no service names")
					break
				}
				c.mu.Lock()
				c.stopping = true
				all := make([]*service, 0, len(c.services))
				for _, s := range c.services {
					all = append(all, s)
				}
				c.mu.Unlock()
				wait := 10 * time.Second
				for _, s := range all {
					s.mu.Lock()
					d := duration(s.cfg.StopTimeout) + 10*time.Second
					s.mu.Unlock()
					if d > wait {
						wait = d
					}
				}
				answer.ShutdownWait = wait.String()
			default:
				result = fmt.Errorf("unknown daemon request %q", req.Action)
			}
		}
	}
	if result != nil {
		answer.Error = result.Error()
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = json.NewEncoder(conn).Encode(answer)
	if req.Action == "shutdown" && result == nil {
		c.cancel()
	}
}
func call(p paths, action string, names []string) (response, error) {
	var ep endpoint
	if err := readJSON(p.endpointPath(), &ep); err != nil {
		return response{}, fmt.Errorf("daemon is not running; run oo daemon (%w)", err)
	}
	if ep.Protocol != protocol {
		return response{}, fmt.Errorf("daemon protocol %d is unsupported; shut it down with its matching oo version", ep.Protocol)
	}
	actual, rootErr := os.Stat(p.root)
	expected, savedErr := os.Stat(ep.Root)
	if rootErr != nil || savedErr != nil || !os.SameFile(actual, expected) {
		return response{}, fmt.Errorf("daemon root mismatch")
	}
	if err := privateDirectory(filepath.Dir(ep.Socket)); err != nil {
		return response{}, err
	}
	conn, err := net.DialTimeout("unix", ep.Socket, time.Second)
	if err != nil {
		return response{}, fmt.Errorf("daemon is unavailable; run oo daemon: %w", err)
	}
	defer conn.Close()
	deadline := 6 * time.Minute
	if action == "ping" || action == "status" || action == "list" {
		deadline = 2 * time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(deadline))
	if err = json.NewEncoder(conn).Encode(request{Protocol: protocol, Instance: ep.Instance, Action: action, Names: names}); err != nil {
		return response{}, err
	}
	var answer response
	if err = json.NewDecoder(io.LimitReader(conn, 1024*1024)).Decode(&answer); err != nil {
		return answer, err
	}
	if answer.Protocol != protocol || answer.Instance != ep.Instance {
		return answer, fmt.Errorf("daemon response protocol or instance mismatch")
	}
	if answer.Error != "" {
		return answer, errors.New(answer.Error)
	}
	return answer, nil
}
