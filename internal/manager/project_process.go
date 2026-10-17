package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Project commands use argv, the caller's ordinary credential environment and
// an owned process group. Login alone inherits stdin/stdout for browser auth.
type projectCommand func(context.Context, string, []string, string, []string, bool) ([]byte, error)

type projectOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *projectOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const limit = 4 << 20
	b.data = append(b.data, p...)
	if len(b.data) > limit {
		b.data = append([]byte{}, b.data[len(b.data)-limit:]...)
	}
	return len(p), nil
}
func (m *Manager) projectCommand(ctx context.Context, program string, args []string, dir string, env []string, interactive bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env = dir, env
	configureLanguageCancellation(cmd)
	var output projectOutput
	if interactive {
		cmd.Stdin = m.In
		cmd.Stdout = m.Out
		cmd.Stderr = m.Out
	} else {
		cmd.Stdout = &output
		cmd.Stderr = &output
	}
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return output.data, ctx.Err()
	}
	return output.data, err
}
func projectFailure(stage string, output []byte, err error, secrets []string) error {
	if err == nil {
		return nil
	}
	text := string(output)
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
	}
	text = strings.TrimSpace(text)
	if len(text) > 8192 {
		text = text[len(text)-8192:]
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", stage, err)
	}
	if text == "" {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return fmt.Errorf("%s: %w\n%s", stage, err, text)
}
func (i *projectInstaller) command(ctx context.Context, timeout time.Duration, stage, program string, args ...string) ([]byte, error) {
	return i.commandWithEnv(ctx, timeout, stage, program, i.tools.env, args...)
}
func (i *projectInstaller) commandWithEnv(ctx context.Context, timeout time.Duration, stage, program string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := i.run(ctx, program, args, i.directory, env, false)
	return output, projectFailure(stage, output, err, i.secrets)
}
func projectJSONFile(filename string, out any) error {
	data, err := readProjectFile(filename)
	if err != nil {
		return err
	}
	if err := decodeProjectJSON(data, out); err != nil {
		return fmt.Errorf("invalid DevEco configuration %s: %w", filename, err)
	}
	return nil
}
func projectOutputSucceeded(output []byte) bool {
	text := strings.ToLower(string(output))
	if strings.Contains(text, "[fail]") || strings.Contains(text, "failed") || strings.Contains(text, "error:") {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "[success]") && strings.Contains(line, "install package successfully") {
			return true
		}
		if strings.Contains(line, "[info]app install path:") && strings.HasSuffix(strings.TrimSpace(line), "msg:install bundle successfully.") && strings.Contains(text, "appmod finish") {
			return true
		}
	}
	return false
}

var _ io.Writer = (*projectOutput)(nil)
