package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"
)

type Status struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	PID       int    `json:"pid,omitempty"`
	RunnerPID int    `json:"runner_pid,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
	Starts    int    `json:"starts"`
}
type process struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	closeOnce sync.Once
	ready     chan error
	done      chan struct{}
	lock      *os.File
}

func (p *process) stop() { p.closeOnce.Do(func() { _ = p.input.Close() }) }

type service struct {
	op             sync.Mutex
	mu             sync.Mutex
	status         Status
	cfg            Config
	proc           *process
	desired        bool
	generation     uint64
	attempts       []time.Time
	cleanupSession int
	cleanupLock    *os.File
}
type controller struct {
	ctx        context.Context
	cancel     context.CancelFunc
	paths      paths
	self       *os.File
	diagnostic *os.File
	mu         sync.Mutex
	services   map[string]*service
	stopping   bool
}

func (c *controller) get(name string, require bool) (*service, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.services[name]; s != nil {
		return s, nil
	}
	if require {
		if _, err := os.Lstat(c.paths.services + "/" + name + ".json"); err != nil {
			return nil, fmt.Errorf("unknown service %q: %w", name, err)
		}
	}
	s := &service{status: Status{Name: name, State: "inactive"}}
	c.services[name] = s
	return s, nil
}
func (c *controller) isStopping() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopping || c.ctx.Err() != nil
}
func (c *controller) start(name string) error {
	if c.isStopping() {
		return fmt.Errorf("daemon is shutting down")
	}
	s, err := c.get(name, true)
	if err != nil {
		return err
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.proc != nil {
		s.mu.Unlock()
		return nil
	}
	s.desired = true
	s.generation++
	s.attempts = nil
	s.mu.Unlock()
	return c.startLocked(s, true)
}
func (c *controller) startLocked(s *service, reload bool) error {
	fail := func(err error) error {
		s.mu.Lock()
		s.status.State = "failed"
		s.status.Error = err.Error()
		s.desired = false
		s.mu.Unlock()
		return err
	}
	if c.isStopping() {
		return fail(fmt.Errorf("daemon is shutting down"))
	}
	if err := c.finishCleanup(s); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	cfg := s.cfg
	name := s.status.Name
	s.mu.Unlock()
	if reload {
		var err error
		cfg, err = c.paths.config(name)
		if err != nil {
			return fail(err)
		}
	}
	now := time.Now()
	window := duration(cfg.StartLimit.Interval)
	s.mu.Lock()
	recent := s.attempts[:0]
	for _, t := range s.attempts {
		if now.Sub(t) < window {
			recent = append(recent, t)
		}
	}
	s.attempts = recent
	if len(s.attempts) >= cfg.StartLimit.Burst {
		s.mu.Unlock()
		return fail(fmt.Errorf("start limit reached: %d attempts in %s", cfg.StartLimit.Burst, cfg.StartLimit.Interval))
	}
	s.attempts = append(s.attempts, now)
	s.cfg = cfg
	s.status.State = "starting"
	s.status.Error = ""
	s.mu.Unlock()
	lock, err := waitLock(c.ctx, c.paths.serviceLock(name), duration(cfg.StopTimeout)+5*time.Second)
	if err != nil {
		return fail(err)
	}
	transferred := false
	defer func() {
		if !transferred {
			lock.Close()
		}
	}() // Never LOCK_UN.
	if c.isStopping() {
		return fail(fmt.Errorf("daemon is shutting down"))
	}
	cmd := exec.Command("/proc/self/fd/4", "_service", name)
	cmd.Env = replaceEnv(os.Environ(), map[string]string{"OHECO_ROOT": c.paths.root})
	cmd.ExtraFiles = []*os.File{lock, c.self}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stderr = c.diagnostic
	input, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return fail(err)
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return fail(err)
	}
	p := &process{cmd: cmd, input: input, ready: make(chan error, 1), done: make(chan struct{}), lock: lock}
	transferred = true
	s.mu.Lock()
	s.proc = p
	s.status.RunnerPID = cmd.Process.Pid
	s.mu.Unlock()
	go c.monitor(s, p, output)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	written := make(chan error, 1)
	go func() { written <- json.NewEncoder(input).Encode(cfg) }()
	select {
	case err = <-written:
		if err != nil {
			p.stop()
			return err
		}
	case <-c.ctx.Done():
		p.stop()
		return c.ctx.Err()
	case <-timer.C:
		p.stop()
		return fmt.Errorf("%s: runner configuration timed out", name)
	}
	select {
	case err = <-p.ready:
		return err
	case <-c.ctx.Done():
		p.stop()
		return c.ctx.Err()
	case <-timer.C:
		p.stop()
		return fmt.Errorf("%s: runner startup timed out", name)
	}
}
func (c *controller) monitor(s *service, p *process, output io.ReadCloser) {
	defer output.Close()
	decoder := json.NewDecoder(io.LimitReader(output, 1024*1024))
	ready, reported := false, false
	exitCode := -1
	message := "runner disconnected without an exit report"
	for {
		var event runnerEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		switch event.Type {
		case "started":
			s.mu.Lock()
			if s.proc == p {
				s.status.State = "running"
				s.status.PID = event.PID
				s.status.Starts++
			}
			s.mu.Unlock()
			if !ready {
				p.ready <- nil
				ready = true
			}
		case "error":
			message = event.Error
			if !ready {
				p.ready <- errors.New(message)
				ready = true
			}
		case "exited":
			reported = true
			exitCode = event.ExitCode
			message = event.Error
		}
	}
	waitErr := p.cmd.Wait()
	p.stop()
	if waitErr != nil {
		message = fmt.Sprintf("runner failed: %v; %s", waitErr, message)
		exitCode = -1
	}
	grace := time.Duration(0)
	if !reported {
		s.mu.Lock()
		grace = duration(s.cfg.StopTimeout)
		s.mu.Unlock()
	}
	cleanupErr := cleanupSession(p.cmd.Process.Pid, grace)
	if cleanupErr == nil {
		p.lock.Close()
	}
	if !ready {
		p.ready <- errors.New(message)
	}
	s.mu.Lock()
	if cleanupErr != nil {
		s.cleanupSession = p.cmd.Process.Pid
		s.cleanupLock = p.lock
		s.desired = false
		message = cleanupErr.Error()
	}
	if s.proc == p {
		s.proc = nil
		s.status.PID = 0
		s.status.RunnerPID = 0
		s.status.ExitCode = &exitCode
		s.status.Error = message
		retry := s.desired && (s.cfg.Restart == "always" || s.cfg.Restart == "on-failure" && exitCode != 0) && !c.isStopping()
		if retry {
			s.status.State = "restarting"
		} else if s.desired && exitCode != 0 {
			s.status.State = "failed"
			s.desired = false
		} else {
			s.status.State = "inactive"
			s.desired = false
		}
		generation := s.generation
		delay := duration(s.cfg.RestartDelay)
		s.mu.Unlock()
		close(p.done)
		if retry {
			go c.retry(s, generation, delay)
		}
		return
	}
	s.mu.Unlock()
	close(p.done)
}
func (c *controller) retry(s *service, generation uint64, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return
	case <-timer.C:
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	valid := s.desired && s.generation == generation && s.proc == nil
	s.mu.Unlock()
	if valid {
		_ = c.startLocked(s, false)
	}
}

// Called with the service operation mutex held, including after runner failure.
func (c *controller) finishCleanup(s *service) error {
	s.mu.Lock()
	session, lock, grace := s.cleanupSession, s.cleanupLock, duration(s.cfg.StopTimeout)
	s.mu.Unlock()
	if session == 0 {
		return nil
	}
	if err := cleanupSession(session, grace); err != nil {
		return err
	}
	if lock != nil {
		lock.Close()
	}
	s.mu.Lock()
	s.cleanupSession = 0
	s.cleanupLock = nil
	s.mu.Unlock()
	return nil
}
func (c *controller) stop(name string) error {
	s, err := c.get(name, true)
	if err != nil {
		return err
	}
	s.op.Lock()
	defer s.op.Unlock()
	return c.stopLocked(s)
}
func (c *controller) stopLocked(s *service) error {
	s.mu.Lock()
	s.desired = false
	s.generation++
	p := s.proc
	timeout := duration(s.cfg.StopTimeout)
	if p == nil {
		s.mu.Unlock()
		if err := c.finishCleanup(s); err != nil {
			return err
		}
		s.mu.Lock()
		s.status.State = "inactive"
		s.mu.Unlock()
		return nil
	}
	s.status.State = "stopping"
	s.mu.Unlock()
	p.stop()
	timer := time.NewTimer(timeout + 5*time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("%s: runner is still cleaning the process group; refusing a duplicate start", s.status.Name)
	}
}
func (c *controller) restart(name string) error {
	if c.isStopping() {
		return fmt.Errorf("daemon is shutting down")
	}
	s, err := c.get(name, true)
	if err != nil {
		return err
	}
	s.op.Lock()
	defer s.op.Unlock()
	if err = c.stopLocked(s); err != nil {
		return err
	}
	s.mu.Lock()
	s.desired = true
	s.generation++
	s.attempts = nil
	s.mu.Unlock()
	return c.startLocked(s, true)
}
func (c *controller) startEnabled() error {
	enabled, err := c.paths.enabled()
	if err != nil {
		return err
	}
	for name := range enabled {
		s, _ := c.get(name, false)
		s.mu.Lock()
		s.desired = true
		s.generation++
		generation := s.generation
		s.status.State = "starting"
		s.mu.Unlock()
		go func(s *service, generation uint64) {
			s.op.Lock()
			defer s.op.Unlock()
			s.mu.Lock()
			valid := s.desired && s.generation == generation
			s.mu.Unlock()
			if valid {
				_ = c.startLocked(s, true)
			}
		}(s, generation)
	}
	return nil
}
func (c *controller) stopAll() {
	c.mu.Lock()
	c.stopping = true
	all := make([]*service, 0, len(c.services))
	for _, s := range c.services {
		all = append(all, s)
	}
	c.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func(s *service) {
			defer wg.Done()
			s.op.Lock()
			defer s.op.Unlock()
			if err := c.stopLocked(s); err != nil {
				fmt.Fprintln(c.diagnostic, err)
			}
		}(s)
	}
	wg.Wait()
}
func (c *controller) snapshot(names []string) ([]Status, error) {
	enabled, err := c.paths.enabled()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		names, err = c.paths.names()
		if err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, name := range names {
			known[name] = true
		}
		for name := range enabled {
			known[name] = true
		}
		c.mu.Lock()
		for name := range c.services {
			known[name] = true
		}
		c.mu.Unlock()
		names = names[:0]
		for name := range known {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	result := make([]Status, 0, len(names))
	for _, name := range names {
		s, err := c.get(name, !enabled[name])
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		status := s.status
		s.mu.Unlock()
		status.Enabled = enabled[name]
		if status.State == "inactive" {
			if _, err := c.paths.config(name); err != nil {
				status.State = "failed"
				status.Error = err.Error()
			}
		}
		result = append(result, status)
	}
	return result, nil
}
