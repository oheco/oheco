package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/oheco/oheco/internal/manager"
)

func execute(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "_update" {
		if len(args) != 1 {
			return fmt.Errorf("invalid background update arguments")
		}
		m, err := manager.New(os.Getenv("OHECO_ROOT"), os.Getenv("OHECO_INDEX_URL"), io.Discard)
		if err != nil {
			return err
		}
		lock := os.NewFile(3, "index-update-lock")
		if lock == nil {
			return fmt.Errorf("missing background update lock")
		}
		defer lock.Close()
		return m.BackgroundUpdate(ctx, lock)
	}
	// Explicit updates already perform this work. Bootstrap and installation
	// startup checks must not spawn an updater from a temporary executable.
	if os.Getenv("OHECO_NO_AUTO_UPDATE") != "1" &&
		(len(args) == 0 || args[0] != "update" && args[0] != "_bootstrap") {
		_ = startBackgroundUpdate()
	}
	return run(ctx, args)
}

func startBackgroundUpdate() error {
	m, err := manager.New(os.Getenv("OHECO_ROOT"), os.Getenv("OHECO_INDEX_URL"), io.Discard)
	if err != nil {
		return err
	}
	lock, err := m.TryUpdateLock()
	if err != nil {
		return err
	}
	// Close only: LOCK_UN would also unlock the child's inherited descriptor.
	defer lock.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "_update")
	cmd.Env = append(os.Environ(), "OHECO_ROOT="+m.Root, "OHECO_INDEX_URL="+m.IndexURL)
	cmd.ExtraFiles = []*os.File{lock}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Nil standard streams become /dev/null, so no terminal or caller's pipe
	// remains open in the child. It survives the foreground command exiting.
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
