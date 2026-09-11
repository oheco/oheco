package manager

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func archivePath(name string, strip int) (string, error) {
	if strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	parts := []string{}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("unsafe archive path %q", name)
		}
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	if len(parts) <= strip {
		return "", nil
	}
	return strings.Join(parts[strip:], "/"), nil
}

func extract(ctx context.Context, filename, destination string, a catalog.Artifact) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	links := map[string]string{}
	var total int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if count > 250000 {
			return fmt.Errorf("archive contains too many entries")
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, err := archivePath(h.Name, a.StripComponents)
		if err != nil {
			return err
		}
		if rel == "" {
			if h.Typeflag != tar.TypeDir {
				return fmt.Errorf("strip_components discards file %q", h.Name)
			}
			continue
		}
		if seen[rel] {
			return fmt.Errorf("duplicate archive entry %q", rel)
		}
		seen[rel] = true
		filename := filepath.Join(destination, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filename, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || h.Size > 8<<30-total {
				return fmt.Errorf("unpacked archive exceeds 8 GiB")
			}
			total += h.Size
			mode := os.FileMode(0644)
			if h.Mode&0111 != 0 {
				mode = 0755
			}
			out, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, contextReader{ctx, tr})
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Chmod(filename, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if h.Linkname == "" || strings.ContainsAny(h.Linkname, "\\\x00") || path.IsAbs(h.Linkname) {
				return fmt.Errorf("unsafe archive symlink %q", h.Name)
			}
			resolved := path.Clean(path.Join(path.Dir(rel), h.Linkname))
			if !catalog.SafePath(resolved) {
				return fmt.Errorf("archive symlink escapes package: %s", rel)
			}
			links[rel] = h.Linkname
		default:
			return fmt.Errorf("unsupported archive entry type %d: %s (hard links and special files are not supported)", h.Typeflag, h.Name)
		}
	}
	// Install symlinks last so regular files cannot be written through a symlink.
	for rel, target := range links {
		filename := filepath.Join(destination, filepath.FromSlash(rel))
		for parent := filepath.Dir(filename); parent != destination; parent = filepath.Dir(parent) {
			info, err := os.Lstat(parent)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("symlink parent is not a directory: %s", parent)
			}
		}
		if err := os.Symlink(target, filename); err != nil {
			return err
		}
	}
	// Resolve chains too: lexical validation alone does not catch symlink chains.
	for rel := range links {
		resolved, err := filepath.EvalSymlinks(filepath.Join(destination, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if !within(destination, resolved) {
			return fmt.Errorf("archive symlink escapes package: %s", rel)
		}
	}
	// Read gzip trailer so a truncated or corrupt stream cannot pass as a tar EOF.
	if n, err := io.Copy(io.Discard, io.LimitReader(contextReader{ctx, gz}, (1<<20)+1)); err != nil {
		return err
	} else if n > 1<<20 {
		return fmt.Errorf("archive has excessive trailing padding")
	}
	return verifyBinaries(destination, a)
}

func within(root, filename string) bool {
	rel, err := filepath.Rel(root, filename)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func verifyBinaries(directory string, a catalog.Artifact) error {
	for name, rel := range a.Binaries {
		filename, err := filepath.EvalSymlinks(filepath.Join(directory, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("binary %s: %w", name, err)
		}
		if !within(directory, filename) {
			return fmt.Errorf("binary %s escapes its package", name)
		}
		info, err := os.Stat(filename)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("binary %s is not an executable regular file", name)
		}
	}
	return nil
}
