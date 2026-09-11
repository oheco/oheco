package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

type Manager struct {
	Root     string
	Platform string
	IndexURL string
	Client   *http.Client
	Out      io.Writer
}

func New(root, indexURL string, out io.Writer) (*Manager, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(home, ".oheco")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if root == string(filepath.Separator) {
		return nil, fmt.Errorf("OHECO_ROOT cannot be the filesystem root")
	}
	if indexURL == "" {
		indexURL = catalog.DefaultURL
	}
	if err := catalog.ValidateURL(indexURL); err != nil {
		return nil, err
	}
	if out == nil {
		out = io.Discard
	}
	return &Manager{Root: root, Platform: runtime.GOOS + "-" + runtime.GOARCH, IndexURL: indexURL, Client: HTTPClient(), Out: out}, nil
}

func plainDir(filename string) error {
	if err := os.Mkdir(filename, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("expected a real directory: %s", filename)
	}
	return nil
}
func (m *Manager) prepare() error {
	if err := os.MkdirAll(m.Root, 0755); err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return err
	}
	if root == string(filepath.Separator) {
		return fmt.Errorf("OHECO_ROOT cannot resolve to the filesystem root")
	}
	m.Root = root
	for _, rel := range []string{"bin", "packages", "index", "state", "cache", "cache/downloads", "tmp"} {
		if err := plainDir(filepath.Join(m.Root, rel)); err != nil {
			return err
		}
	}
	return nil
}

func SplitSpec(spec string) (string, string, error) {
	name, version, has := strings.Cut(spec, "@")
	if !catalog.ValidName(name) || (has && !catalog.ValidComponent(version)) {
		return "", "", fmt.Errorf("invalid package specification %q; use name or name@version", spec)
	}
	return name, version, nil
}

func (m *Manager) Install(ctx context.Context, spec string, noSwitch bool) error {
	name, version, err := SplitSpec(spec)
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
		version, a, err := p.Resolve(version, m.Platform)
		if err != nil {
			return err
		}
		return m.installLocked(ctx, name, version, a, "", noSwitch)
	})
}

// Bootstrap imports an already verified official archive through the same installer.
func (m *Manager) Bootstrap(ctx context.Context, manifest, archive, version string) error {
	var p catalog.Package
	if err := readJSON(manifest, &p); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Name != "oheco" {
		return fmt.Errorf("bootstrap only accepts the oheco package")
	}
	version, a, err := p.Resolve(version, m.Platform)
	if err != nil {
		return err
	}
	if a.Binaries["oo"] != "bin/oo" || a.StripComponents != 0 {
		return fmt.Errorf("bootstrap requires bin/oo at the archive root")
	}
	if err := verifyFile(archive, a); err != nil {
		return err
	}
	return m.withLock(func() error { return m.installLocked(ctx, p.Name, version, a, archive, false) })
}

func (m *Manager) installLocked(ctx context.Context, name, version string, a catalog.Artifact, archive string, noSwitch bool) error {
	before, err := m.LoadState()
	if err != nil {
		return err
	}
	after := cloneState(before)
	p := after.Packages[name]
	if p.Versions == nil {
		p.Versions = map[string]Receipt{}
	}
	if r, exists := p.Versions[version]; exists {
		if r.Platform != m.Platform || r.Artifact.SHA256 != a.SHA256 || r.Artifact.Size != a.Size || r.Artifact.Format != a.Format || r.Artifact.StripComponents != a.StripComponents || !reflect.DeepEqual(r.Artifact.Binaries, a.Binaries) || !reflect.DeepEqual(r.Artifact.Launchers, a.Launchers) {
			return fmt.Errorf("%s@%s is already installed with different contents; publish a new version", name, version)
		}
		if err := verifyInstalled(filepath.Join(m.Root, "packages", name, version), r.Artifact); err != nil {
			return err
		}
		if !noSwitch {
			p.Active = version
		}
		after.Packages[name] = p
		if err := m.commit(before, after, "", ""); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "%s@%s is already installed\n", name, version)
		return nil
	}
	for _, r := range p.Versions {
		if r.Platform != m.Platform {
			return fmt.Errorf("cannot mix platforms in one OHECO_ROOT")
		}
	}
	p.Versions[version] = Receipt{Platform: m.Platform, InstalledAt: time.Now().UTC().Format(time.RFC3339), Artifact: a}
	if !noSwitch {
		p.Active = version
	}
	after.Packages[name] = p
	if err := after.validate(); err != nil {
		return err
	}
	if err := m.checkLinks(stateLinks(before), stateLinks(after), false); err != nil {
		return err
	}
	if err := plainDir(filepath.Join(m.Root, "packages", name)); err != nil {
		return err
	}
	destination := filepath.Join(m.Root, "packages", name, version)
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("unmanaged package directory already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if archive == "" {
		archive, err = m.download(ctx, a)
		if err != nil {
			return err
		}
	}
	stage, err := os.MkdirTemp(filepath.Join(m.Root, "tmp"), "install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extract(ctx, archive, stage, a); err != nil {
		return err
	}
	if err := createLaunchers(stage, a); err != nil {
		return err
	}
	if name == "oheco" {
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		output, err := exec.CommandContext(checkCtx, filepath.Join(stage, a.Binaries["oo"]), "--version").CombinedOutput()
		if err != nil || !strings.HasPrefix(string(output), "oo "+version+" ") {
			return fmt.Errorf("new oo failed version/startup check: %s (%v)", strings.TrimSpace(string(output)), err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.commit(before, after, stage, destination); err != nil {
		return err
	}
	fmt.Fprintf(m.Out, "Installed %s@%s", name, version)
	if !noSwitch {
		fmt.Fprint(m.Out, " (active)")
	}
	fmt.Fprintln(m.Out)
	return nil
}

func (m *Manager) Switch(name, version string) error {
	if !catalog.ValidName(name) || !catalog.ValidComponent(version) {
		return fmt.Errorf("usage: oo switch <package> <version>")
	}
	return m.withLock(func() error {
		before, err := m.LoadState()
		if err != nil {
			return err
		}
		p, ok := before.Packages[name]
		if !ok {
			return fmt.Errorf("%s is not installed", name)
		}
		r, ok := p.Versions[version]
		if !ok {
			return fmt.Errorf("%s@%s is not installed; run oo install %s@%s", name, version, name, version)
		}
		if err := verifyInstalled(filepath.Join(m.Root, "packages", name, version), r.Artifact); err != nil {
			return err
		}
		after := cloneState(before)
		p = after.Packages[name]
		p.Active = version
		after.Packages[name] = p
		if err := m.commit(before, after, "", ""); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "Switched %s to %s\n", name, version)
		return nil
	})
}

func (m *Manager) Remove(spec string, all bool) error {
	name, version, err := SplitSpec(spec)
	if err != nil {
		return err
	}
	if all && version != "" {
		return fmt.Errorf("--all cannot be combined with a version")
	}
	return m.withLock(func() error {
		before, err := m.LoadState()
		if err != nil {
			return err
		}
		p, ok := before.Packages[name]
		if !ok {
			return fmt.Errorf("%s is not installed", name)
		}
		if !all && version == "" {
			version = p.Active
			if version == "" {
				return fmt.Errorf("%s has no active version; specify name@version or --all", name)
			}
		}
		if !all {
			if _, ok := p.Versions[version]; !ok {
				return fmt.Errorf("%s@%s is not installed", name, version)
			}
		}
		after := cloneState(before)
		p = after.Packages[name]
		if all {
			delete(after.Packages, name)
		} else {
			delete(p.Versions, version)
			if p.Active == version {
				p.Active = ""
			}
			if len(p.Versions) == 0 {
				delete(after.Packages, name)
			} else {
				after.Packages[name] = p
			}
		}
		if err := m.commit(before, after, "", ""); err != nil {
			return err
		}
		if all {
			fmt.Fprintf(m.Out, "Removed all versions of %s\n", name)
		} else {
			fmt.Fprintf(m.Out, "Removed %s@%s\n", name, version)
		}
		return nil
	})
}

func (m *Manager) List() error {
	s, err := m.LoadState()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(s.Packages))
	for n := range s.Packages {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(m.Out, "No packages installed.")
		return nil
	}
	for _, name := range names {
		p := s.Packages[name]
		versions := make([]string, 0, len(p.Versions))
		for v := range p.Versions {
			versions = append(versions, v)
		}
		sort.Strings(versions)
		for _, v := range versions {
			marker := ""
			if p.Active == v {
				marker = " *"
			}
			fmt.Fprintf(m.Out, "%s@%s%s\n", name, v, marker)
		}
	}
	return nil
}

func (m *Manager) Search(query string) error {
	idx, err := m.LoadIndex()
	if err != nil {
		return err
	}
	query = strings.ToLower(query)
	count := 0
	for _, p := range idx.Packages {
		text := p.Name + " " + p.Description
		for _, v := range p.Versions {
			for _, a := range v.Artifacts {
				for bin := range a.Binaries {
					text += " " + bin
				}
			}
		}
		if strings.Contains(strings.ToLower(text), query) {
			latest := p.Latest[m.Platform]
			if latest == "" {
				latest = "unavailable for " + m.Platform
			}
			fmt.Fprintf(m.Out, "%s\t%s\t%s\n", p.Name, latest, p.Description)
			count++
		}
	}
	if count == 0 {
		fmt.Fprintln(m.Out, "No matching packages.")
	}
	return nil
}

func (m *Manager) Info(name string) error {
	idx, err := m.LoadIndex()
	if err != nil {
		return err
	}
	p, err := idx.Find(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(m.Out, "%s — %s\nUpstream: %s\nPort: %s\nLicense: %s\n", p.Name, p.Description, p.Upstream, p.Repository, p.License)
	for _, who := range p.Maintainers {
		fmt.Fprintf(m.Out, "Maintainer: @%s %s\n", who.GitHub, who.Name)
	}
	for _, v := range p.Versions {
		if a, ok := v.Artifacts[m.Platform]; ok {
			marker := ""
			if p.Latest[m.Platform] == v.Version {
				marker = " (latest)"
			}
			bins := make([]string, 0, len(a.Binaries))
			for b := range a.Binaries {
				bins = append(bins, b)
			}
			sort.Strings(bins)
			fmt.Fprintf(m.Out, "%s%s [%s]\n  %s\n", v.Version, marker, strings.Join(bins, ", "), a.URL)
		}
	}
	if p.Notes != "" {
		fmt.Fprintln(m.Out, p.Notes)
	}
	return nil
}
