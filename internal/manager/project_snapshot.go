package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// snapshotPackage verifies and installs the same private bytes, using a simple
// file name independent of the editable project's directory name.
func (i *projectInstaller) snapshotPackage(ctx context.Context, source string) (string, error) {
	extension := strings.ToLower(filepath.Ext(source))
	if extension != ".hap" && extension != ".hsp" {
		return "", fmt.Errorf("unsupported SDK application package extension")
	}
	input, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	const limit = int64(2 << 30)
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return "", fmt.Errorf("application package must be a nonempty regular file up to 2 GiB")
	}
	output, err := os.CreateTemp(i.work, "install-*"+extension)
	if err != nil {
		return "", err
	}
	path := output.Name()
	n, copyErr := io.CopyBuffer(output, io.LimitReader(projectEmbeddedContextReader{ctx, input}, limit+1), make([]byte, 32<<10))
	closeErr := output.Close()
	if copyErr != nil {
		return "", fmt.Errorf("snapshot built application package: %w", copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if n != info.Size() || n > limit {
		return "", fmt.Errorf("application package changed size while taking its installation snapshot")
	}
	return path, nil
}
