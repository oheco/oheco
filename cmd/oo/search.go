package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/oheco/oheco/internal/manager"
)

func search(ctx context.Context, m *manager.Manager, query string, in io.Reader, interactive bool) error {
	update, err := m.Search(ctx, query)
	if err != nil || update == nil {
		return err
	}
	fmt.Fprintln(m.Out, "\nA newer package index is available.")
	if !interactive {
		fmt.Fprintln(m.Out, "Run oo update, then run oo search again.")
		return nil
	}
	yes, err := confirmUpdate(ctx, in, m.Out)
	if err != nil || !yes {
		return err
	}
	changed, err := m.ApplyIndexUpdate(ctx, update)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintln(m.Out, "Index updated. Run oo search again to see the latest results.")
	} else {
		fmt.Fprintln(m.Out, "Index is already up to date. Run oo search again.")
	}
	return nil
}

func confirmUpdate(ctx context.Context, in io.Reader, out io.Writer) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := fmt.Fprint(out, "Update now? [y/N] "); err != nil {
		return false, err
	}
	type answer struct {
		text string
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		answers <- answer{line, err}
	}()
	select {
	case reply := <-answers:
		fmt.Fprintln(out)
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if reply.err != nil && !errors.Is(reply.err, io.EOF) {
			return false, reply.err
		}
		value := strings.ToLower(strings.TrimSpace(reply.text))
		return value == "y" || value == "yes", nil
	case <-ctx.Done():
		fmt.Fprintln(out)
		return false, ctx.Err()
	}
}

func terminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&termios)))
	runtime.KeepAlive(f)
	return errno == 0
}
