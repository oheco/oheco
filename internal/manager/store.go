package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/oheco/oheco/internal/catalog"
)

type Receipt struct {
	Platform    string           `json:"platform"`
	InstalledAt string           `json:"installed_at"`
	Artifact    catalog.Artifact `json:"artifact"`
}
type InstalledPackage struct {
	Active   string             `json:"active,omitempty"`
	Versions map[string]Receipt `json:"versions"`
}
type State struct {
	SchemaVersion int                         `json:"schema_version"`
	Packages      map[string]InstalledPackage `json:"packages"`
}
type transaction struct {
	Before    State `json:"before"`
	After     State `json:"after"`
	Committed bool  `json:"committed"`
}

func emptyState() State { return State{SchemaVersion: 1, Packages: map[string]InstalledPackage{}} }
func cloneState(s State) State {
	b, _ := json.Marshal(s)
	var result State
	_ = json.Unmarshal(b, &result)
	return result
}

func atomicWrite(filename string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(filename), ".oo-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}
func writeJSON(filename string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filename, append(b, '\n'), 0600)
}
func readJSON(filename string, v any) error {
	b, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	return catalog.Decode(bytes.NewReader(b), v)
}

func (s State) validate() error {
	if s.SchemaVersion != 1 || s.Packages == nil {
		return fmt.Errorf("unsupported or invalid local state")
	}
	owners := map[string]string{}
	for name, p := range s.Packages {
		if !catalog.ValidName(name) || len(p.Versions) == 0 {
			return fmt.Errorf("invalid installed package %q", name)
		}
		if p.Active != "" {
			if _, ok := p.Versions[p.Active]; !ok {
				return fmt.Errorf("active version is not installed for %s", name)
			}
		}
		for version, r := range p.Versions {
			if !catalog.ValidComponent(version) || !catalog.ValidPlatform(r.Platform) {
				return fmt.Errorf("invalid installed version")
			}
			if err := r.Artifact.Validate(); err != nil {
				return fmt.Errorf("invalid receipt for %s@%s: %w", name, version, err)
			}
			for bin := range r.Artifact.Binaries {
				if owner, ok := owners[bin]; ok && owner != name {
					return fmt.Errorf("command %s already belongs to %s", bin, owner)
				}
				owners[bin] = name
			}
		}
	}
	return nil
}

func (m *Manager) LoadState() (State, error) {
	s := emptyState()
	err := readJSON(filepath.Join(m.Root, "state", "installed.json"), &s)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := s.validate(); err != nil {
		return s, err
	}
	return s, m.checkPackageDirs(s)
}

// A user-replaced package directory must never redirect cleanup outside Root.
func (m *Manager) checkPackageDirs(states ...State) error {
	for _, s := range states {
		for name, p := range s.Packages {
			paths := []string{filepath.Join(m.Root, "packages", name)}
			for version := range p.Versions {
				paths = append(paths, filepath.Join(m.Root, "packages", name, version))
			}
			for _, filename := range paths {
				info, err := os.Lstat(filename)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("package directory was replaced: %s", filename)
				}
			}
		}
	}
	return nil
}

// withLock also recovers an interrupted operation before starting a new one.
// The kernel releases flock when a process exits, including SIGKILL.
func (m *Manager) withLock(fn func() error) error {
	if err := m.prepare(); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.Root, "state", "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("cannot lock %s (another oo may be running): %w", m.Root, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := m.recoverTransaction(); err != nil {
		return fmt.Errorf("recover interrupted operation: %w", err)
	}
	return fn()
}

func stateLinks(s State) map[string]string {
	links := map[string]string{}
	for name, p := range s.Packages {
		for version, r := range p.Versions {
			for bin, rel := range r.Artifact.Binaries {
				versioned := bin + "@" + version
				links[versioned] = filepath.ToSlash(filepath.Join("..", "packages", name, version, filepath.FromSlash(rel)))
				if p.Active == version {
					links[bin] = versioned
				}
			}
		}
	}
	return links
}
func unionKeys(a, b map[string]string) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func (m *Manager) checkLinks(before, after map[string]string, recovering bool) error {
	for _, name := range unionKeys(before, after) {
		filename := filepath.Join(m.Root, "bin", name)
		info, err := os.Lstat(filename)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("refusing to overwrite unmanaged file %s", filename)
		}
		target, err := os.Readlink(filename)
		if err != nil {
			return err
		}
		if expected, ok := before[name]; ok && target == expected {
			continue
		}
		if recovering {
			if expected, ok := after[name]; ok && target == expected {
				continue
			}
		}
		return fmt.Errorf("refusing to overwrite unmanaged or modified link %s -> %s", filename, target)
	}
	return nil
}
func (m *Manager) applyLinks(before, after map[string]string) error {
	names := unionKeys(before, after)
	priority := func(name string) int {
		if strings.Contains(name, "@") {
			if _, keep := after[name]; keep {
				return 0
			}
			return 2
		}
		return 1
	}
	// Create every versioned target before activating it. Remove versioned links
	// only after the default links have been switched or removed.
	sort.SliceStable(names, func(i, j int) bool { return priority(names[i]) < priority(names[j]) })
	for _, name := range names {
		filename := filepath.Join(m.Root, "bin", name)
		target, keep := after[name]
		if !keep {
			if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if current, err := os.Readlink(filename); err == nil && current == target {
			continue
		}
		f, err := os.CreateTemp(filepath.Dir(filename), ".oo-link-*")
		if err != nil {
			return err
		}
		tmp := f.Name()
		f.Close()
		os.Remove(tmp)
		if err := os.Symlink(target, tmp); err != nil {
			return err
		}
		err = os.Rename(tmp, filename)
		os.Remove(tmp)
		if err != nil {
			return err
		}
	}
	return nil
}

func versionDirs(s State) map[string]bool {
	result := map[string]bool{}
	for name, p := range s.Packages {
		for version := range p.Versions {
			result[filepath.Join(name, version)] = true
		}
	}
	return result
}
func (m *Manager) removeDifference(from, to State) error {
	keep := versionDirs(to)
	for rel := range versionDirs(from) {
		if !keep[rel] {
			if err := os.RemoveAll(filepath.Join(m.Root, "packages", rel)); err != nil {
				return err
			}
			// Only remove empty package directories.
			_ = os.Remove(filepath.Join(m.Root, "packages", filepath.Dir(rel)))
		}
	}
	return nil
}

func (m *Manager) recoverTransaction() error {
	filename := filepath.Join(m.Root, "state", "transaction.json")
	var tx transaction
	if err := readJSON(filename, &tx); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := tx.Before.validate(); err != nil {
		return err
	}
	if err := tx.After.validate(); err != nil {
		return err
	}
	if err := m.checkPackageDirs(tx.Before, tx.After); err != nil {
		return err
	}
	before, after := stateLinks(tx.Before), stateLinks(tx.After)
	if err := m.checkLinks(before, after, true); err != nil {
		return err
	}
	desired, unwanted := tx.Before, tx.After
	if tx.Committed {
		desired, unwanted = tx.After, tx.Before
	}
	if err := m.applyLinks(mergeLinks(before, after), stateLinks(desired)); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), desired); err != nil {
		return err
	}
	if err := m.removeDifference(unwanted, desired); err != nil {
		return err
	}
	return os.Remove(filename)
}
func mergeLinks(a, b map[string]string) map[string]string {
	r := map[string]string{}
	for k, v := range a {
		r[k] = v
	}
	for k, v := range b {
		r[k] = v
	}
	return r
}

// stage, when nonempty, is renamed only after the rollback journal is durable.
func (m *Manager) commit(before, after State, stage, destination string) error {
	if err := after.validate(); err != nil {
		return err
	}
	oldLinks, newLinks := stateLinks(before), stateLinks(after)
	if err := m.checkLinks(oldLinks, newLinks, false); err != nil {
		return err
	}
	if reflect.DeepEqual(before, after) && stage == "" {
		return m.applyLinks(oldLinks, newLinks)
	}
	filename := filepath.Join(m.Root, "state", "transaction.json")
	tx := transaction{Before: before, After: after}
	if err := writeJSON(filename, tx); err != nil {
		return err
	}
	apply := func() error {
		if stage != "" {
			if err := os.Rename(stage, destination); err != nil {
				return err
			}
		}
		if err := m.applyLinks(oldLinks, newLinks); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(m.Root, "state", "installed.json"), after); err != nil {
			return err
		}
		tx.Committed = true
		return writeJSON(filename, tx)
	}
	if err := apply(); err != nil {
		if rollbackErr := m.recoverTransaction(); rollbackErr != nil {
			return fmt.Errorf("%w; rollback needs oo recover: %v", err, rollbackErr)
		}
		return err
	}
	return m.recoverTransaction()
}

func (m *Manager) Recover() error { return m.withLock(func() error { return nil }) }
