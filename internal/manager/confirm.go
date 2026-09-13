package manager

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

func (m *Manager) ask(prompt string) (bool, error) { return m.askContext(context.Background(), prompt) }

// askContext never assumes consent on EOF or redirected stdin. Terminal input
// uses select so SIGINT/SIGTERM cancels a prompt without a leaked read goroutine
// or changing the caller's terminal/file-descriptor flags.
func (m *Manager) askContext(ctx context.Context, prompt string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if m.In == nil {
		return false, fmt.Errorf("confirmation required; use --yes for a reviewed non-interactive operation")
	}
	var file *os.File
	if f, ok := m.In.(*os.File); ok {
		info, err := f.Stat()
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeCharDevice == 0 {
			return false, fmt.Errorf("confirmation required; use --yes for a reviewed non-interactive operation")
		}
		file = f
	}
	fmt.Fprintf(m.Out, "%s [y/N] ", prompt)
	var line string
	var err error
	if file != nil {
		line, err = terminalConfirmation(ctx, file)
	} else {
		reader, ok := m.In.(*bufio.Reader)
		if !ok {
			reader = bufio.NewReader(m.In)
			m.In = reader
		}
		line, err = reader.ReadString('\n')
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			return false, fmt.Errorf("confirmation cancelled (end of input)")
		}
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func terminalConfirmation(ctx context.Context, file *os.File) (string, error) {
	fd := int(file.Fd())
	var bits syscall.FdSet
	if fd < 0 || fd/64 >= len(bits.Bits) {
		return "", fmt.Errorf("terminal descriptor cannot be polled; use --yes")
	}
	var line strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		bits = syscall.FdSet{}
		bits.Bits[fd/64] |= int64(1) << uint(fd%64)
		timeout := syscall.Timeval{Usec: 100000}
		ready, err := syscall.Select(fd+1, &bits, nil, nil, &timeout)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return "", err
		}
		if ready == 0 {
			continue
		}
		var one [1]byte
		n, err := file.Read(one[:])
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", io.EOF
		}
		if one[0] == '\n' {
			return line.String(), nil
		}
		if line.Len() >= 4096 {
			return "", fmt.Errorf("confirmation input is too long")
		}
		line.WriteByte(one[0])
	}
}

func (m *Manager) confirm(prompt string, yes bool) error {
	return m.confirmContext(context.Background(), prompt, yes)
}

func (m *Manager) confirmContext(ctx context.Context, prompt string, yes bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if yes {
		return nil
	}
	ok, err := m.askContext(ctx, prompt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("operation cancelled; no installation changes made")
	}
	return nil
}
