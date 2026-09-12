package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
)

// VerifyProjectArchive applies the same extraction checks used by oo export.
func VerifyProjectArchive(ctx context.Context, archive string, project catalog.Project) error {
	if err := project.Archive().Validate(); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "oo-project-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	return extractProject(ctx, archive, stage, project)
}

func extractProject(ctx context.Context, archive, stage string, project catalog.Project) error {
	if err := extractArchive(ctx, archive, stage, project.Archive(), true); err != nil {
		return err
	}
	entries, err := os.ReadDir(stage)
	if err == nil && len(entries) == 0 {
		return fmt.Errorf("project archive is empty after stripping its root")
	}
	return err
}

// Export downloads a project without installing it or registering commands.
func (m *Manager) Export(ctx context.Context, spec, projectName, destination string) error {
	name, version, err := SplitSpec(spec)
	if err != nil {
		return err
	}
	if projectName != "" && !catalog.ValidComponent(projectName) {
		return fmt.Errorf("invalid project name %q", projectName)
	}
	if destination == "" {
		destination = "."
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	return m.withLock(func() error {
		idx, err := m.LoadIndex()
		if err != nil {
			return err
		}
		p, err := idx.Find(name)
		if err != nil {
			return err
		}
		version, projectName, project, err := p.ResolveProject(version, projectName, m.Platform)
		if err != nil {
			return err
		}
		// Validate all bytes and paths in private staging before touching the destination.
		archive, err := m.download(ctx, project.Archive())
		if err != nil {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Join(m.Root, "tmp"), "export-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := extractProject(ctx, archive, stage, project); err != nil {
			return err
		}
		if err := publishProject(ctx, stage, destination); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "Exported %s@%s project %s to %s\n", name, version, projectName, destination)
		return nil
	})
}

// publishProject never merges existing top-level directories. Files are created
// exclusively, so even a destination collision appearing after preflight fails.
// On failure remove only entries we created, never recursively delete user data.
func publishProject(ctx context.Context, stage, destination string) (result error) {
	created := []string{}
	defer func() {
		if result != nil {
			for i := len(created) - 1; i >= 0; i-- {
				if err := os.Remove(created[i]); err != nil && !errors.Is(err, os.ErrNotExist) {
					result = errors.Join(result, fmt.Errorf("export cleanup retained %s: %w", created[i], err))
				}
			}
		}
	}()
	// Record newly created destination parents so failures can remove empty ones.
	missing := []string{}
	for current := destination; ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			return fmt.Errorf("destination has no existing parent")
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0755); err != nil {
			return err
		}
		created = append(created, missing[i])
	}
	destination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	top, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, incoming := range top {
			if strings.EqualFold(entry.Name(), incoming.Name()) {
				return fmt.Errorf("export destination already contains %s; choose another directory", filepath.Join(destination, entry.Name()))
			}
		}
	}
	return filepath.WalkDir(stage, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if source == stage {
			return nil
		}
		rel, err := filepath.Rel(stage, source)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			if err := os.Mkdir(target, 0755); err != nil {
				return err
			}
			created = append(created, target)
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(source)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			created = append(created, target)
			return nil
		}
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		info, err := in.Stat()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		created = append(created, target)
		_, copyErr := io.Copy(out, contextReader{ctx, in})
		chmodErr := out.Chmod(info.Mode().Perm())
		return errors.Join(copyErr, chmodErr, out.Close())
	})
}
