package manager

import (
	"archive/tar"
	"archive/zip"
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
	return extractArchive(ctx, filename, destination, a, false)
}

func extractArchive(ctx context.Context, filename, destination string, a catalog.Artifact, project bool) error {
	x := archiveExtractor{ctx: ctx, destination: destination, strip: a.StripComponents, project: project, seen: map[string]bool{}, links: map[string]string{}}
	var err error
	switch a.Format {
	case "tar.gz":
		err = x.tarGzip(filename)
	case "zip":
		err = x.zip(filename)
	default:
		return fmt.Errorf("unsupported archive format %q", a.Format)
	}
	if err != nil {
		return err
	}
	if err := x.finish(); err != nil {
		return err
	}
	return verifyBinaries(destination, a)
}

type archiveExtractor struct {
	ctx         context.Context
	destination string
	strip       int
	project     bool
	seen        map[string]bool
	links       map[string]string
	nodes       map[string]archiveNode
	total       int64
	count       int
}

type archiveNode struct {
	name      string
	kind      os.FileMode
	ambiguous bool
}

// claimPath keeps the all-lowercase basename when regular files in the same
// directory differ only in case. This is independent of archive order and the
// destination filesystem. Exact duplicates are rejected separately by seen.
func (x *archiveExtractor) claimPath(rel string, kind os.FileMode) (bool, error) {
	if x.nodes == nil {
		x.nodes = map[string]archiveNode{}
	}
	key := strings.ToLower(rel)
	previous, exists := x.nodes[key]
	if !exists {
		x.nodes[key] = archiveNode{name: rel, kind: kind}
		return true, nil
	}
	if previous.name == rel && previous.kind == os.ModeDir && kind == os.ModeDir {
		return true, nil // An explicit directory may follow an implicit parent.
	}
	if x.project {
		return false, fmt.Errorf("project archive has conflicting paths %q and %q", previous.name, rel)
	}
	if previous.kind != 0 || kind != 0 || path.Dir(previous.name) != path.Dir(rel) {
		return false, fmt.Errorf("conflicting archive paths %q and %q (only regular filenames may differ in case)", previous.name, rel)
	}
	if path.Base(rel) == strings.ToLower(path.Base(rel)) {
		// Remove instead of truncating to preserve the lowercase spelling and
		// the winner's permissions on case-insensitive filesystems too.
		if err := os.Remove(filepath.Join(x.destination, filepath.FromSlash(previous.name))); err != nil {
			return false, err
		}
		x.nodes[key] = archiveNode{name: rel}
		return true, nil
	}
	if path.Base(previous.name) != strings.ToLower(path.Base(previous.name)) {
		// A lowercase entry can still occur later in a streaming tar archive.
		previous.ambiguous = true
		x.nodes[key] = previous
	}
	return false, nil
}

func (x *archiveExtractor) tarGzip(filename string) error {
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
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		var mode os.FileMode
		switch h.Typeflag {
		case tar.TypeDir:
			mode = os.ModeDir
		case tar.TypeReg, tar.TypeRegA:
			mode = os.FileMode(h.Mode) & 0777
		case tar.TypeSymlink:
			mode = os.ModeSymlink
		default:
			return fmt.Errorf("unsupported archive entry type %d: %s (hard links and special files are not supported)", h.Typeflag, h.Name)
		}
		if err := x.add(h.Name, mode, h.Size, h.Linkname, tr); err != nil {
			return err
		}
	}
	// Consume the gzip trailer so truncated streams cannot pass at tar EOF.
	if n, err := io.Copy(io.Discard, io.LimitReader(contextReader{x.ctx, gz}, (1<<20)+1)); err != nil {
		return err
	} else if n > 1<<20 {
		return fmt.Errorf("archive has excessive trailing padding")
	}
	return nil
}

func (x *archiveExtractor) zip(filename string) error {
	z, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer z.Close()
	if len(z.File) > 250000 {
		return fmt.Errorf("archive contains too many entries")
	}
	var declared uint64
	for _, f := range z.File {
		if f.UncompressedSize64 > 8<<30-declared {
			return fmt.Errorf("unpacked archive exceeds 8 GiB")
		}
		declared += f.UncompressedSize64
	}
	for _, f := range z.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		if f.Flags&1 != 0 {
			return fmt.Errorf("encrypted ZIP entries are unsupported: %s", f.Name)
		}
		if f.UncompressedSize64 > uint64(8<<30-x.total) {
			return fmt.Errorf("unpacked archive exceeds 8 GiB")
		}
		mode := f.Mode()
		if mode.IsDir() {
			if f.UncompressedSize64 != 0 {
				return fmt.Errorf("ZIP directory has file contents: %s", f.Name)
			}
			if err := x.add(f.Name, mode, 0, "", nil); err != nil {
				return err
			}
			continue
		}
		if mode.Type() != 0 && mode.Type() != os.ModeSymlink {
			return fmt.Errorf("unsupported ZIP entry type: %s", f.Name)
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		link := ""
		if mode&os.ModeSymlink != 0 {
			if f.UncompressedSize64 > 4096 {
				in.Close()
				return fmt.Errorf("ZIP symlink target exceeds 4096 bytes: %s", f.Name)
			}
			data, readErr := io.ReadAll(io.LimitReader(contextReader{x.ctx, in}, 4097))
			if readErr != nil || uint64(len(data)) != f.UncompressedSize64 {
				in.Close()
				return fmt.Errorf("invalid ZIP symlink %s: %v", f.Name, readErr)
			}
			link = string(data)
		}
		err = x.add(f.Name, mode, int64(f.UncompressedSize64), link, in)
		closeErr := in.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (x *archiveExtractor) add(name string, mode os.FileMode, size int64, link string, in io.Reader) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	x.count++
	if x.count > 250000 {
		return fmt.Errorf("archive contains too many entries")
	}
	rel, err := archivePath(name, x.strip)
	if err != nil {
		return err
	}
	if rel == "" {
		if !mode.IsDir() {
			return fmt.Errorf("strip_components discards file %q", name)
		}
		return nil
	}
	if !x.project && strings.EqualFold(strings.Split(rel, "/")[0], catalog.LauncherDirectory) {
		return fmt.Errorf("archive uses reserved launcher directory: %s", rel)
	}
	if x.seen[rel] {
		return fmt.Errorf("duplicate archive entry %q", rel)
	}
	x.seen[rel] = true
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if _, err := x.claimPath(strings.Join(parts[:i], "/"), os.ModeDir); err != nil {
			return err
		}
	}
	keep, err := x.claimPath(rel, mode.Type())
	if err != nil {
		return err
	}
	filename := filepath.Join(x.destination, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	if mode.IsDir() {
		return os.MkdirAll(filename, 0755)
	}
	if size < 0 || size > 8<<30-x.total {
		return fmt.Errorf("unpacked archive exceeds 8 GiB")
	}
	x.total += size
	if !keep {
		// Discarded variants still count toward limits and must pass ZIP CRC
		// and size checks; the next tar header is not used to skip their bytes.
		n, err := io.Copy(io.Discard, io.LimitReader(contextReader{x.ctx, in}, size+1))
		if err != nil {
			return err
		}
		if n != size {
			return fmt.Errorf("archive entry size mismatch: %s", name)
		}
		return nil
	}
	if mode&os.ModeSymlink != 0 {
		if link == "" || strings.ContainsAny(link, "\\\x00") || path.IsAbs(link) {
			return fmt.Errorf("unsafe archive symlink %q", name)
		}
		resolved := path.Clean(path.Join(path.Dir(rel), link))
		if !catalog.SafePath(resolved) || (!x.project && strings.EqualFold(strings.Split(resolved, "/")[0], catalog.LauncherDirectory)) {
			return fmt.Errorf("archive symlink escapes package or targets reserved directory: %s", rel)
		}
		x.links[rel] = link
		return nil
	}
	perm := os.FileMode(0644)
	if mode.Perm()&0111 != 0 {
		perm = 0755
	}
	out, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	// Read through EOF to verify ZIP CRC and detect dishonest size headers.
	n, copyErr := io.Copy(out, io.LimitReader(contextReader{x.ctx, in}, size+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		return fmt.Errorf("archive entry size mismatch: %s", name)
	}
	return os.Chmod(filename, perm)
}

func (x *archiveExtractor) finish() error {
	destination := x.destination
	links := x.links
	if err := x.ctx.Err(); err != nil {
		return err
	}
	for _, node := range x.nodes {
		if node.ambiguous {
			return fmt.Errorf("case-conflicting archive files have no lowercase variant: %s", node.name)
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
	return nil
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
