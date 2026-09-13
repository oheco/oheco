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
	"text/tabwriter"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

type Manager struct {
	Root     string
	Platform string
	IndexURL string
	Client   *http.Client
	Out      io.Writer
	In       io.Reader
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
	return &Manager{Root: root, Platform: runtime.GOOS + "-" + runtime.GOARCH, IndexURL: indexURL, Client: HTTPClient(), Out: out, In: os.Stdin}, nil
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
	return m.InstallMany(ctx, []string{spec}, InstallOptions{NoSwitch: noSwitch})
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
		check := exec.CommandContext(checkCtx, filepath.Join(stage, a.Binaries["oo"]), "--version")
		check.Env = append(os.Environ(), "OHECO_NO_AUTO_UPDATE=1")
		output, err := check.CombinedOutput()
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
	return m.SwitchContext(context.Background(), name, version)
}

func (m *Manager) SwitchContext(ctx context.Context, name, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		idx := catalog.Index{SchemaVersion: catalog.SchemaVersion}
		for installedName, installed := range before.Packages {
			entry := catalog.Package{Name: installedName}
			for v, receipt := range installed.Versions {
				entry.Versions = append(entry.Versions, receiptVersion(v, receipt))
			}
			idx.Packages = append(idx.Packages, entry)
		}
		plan, err := m.planNative(idx, before, []nativeRequest{{Name: name, Version: version}})
		if err != nil {
			return err
		}
		for _, step := range plan.Steps {
			if !step.Reused {
				return fmt.Errorf("switch requires already installed dependencies; run oo install %s@%s", name, version)
			}
		}
		m.printNativePlan(plan)
		if err := m.executeNativePlan(ctx, plan); err != nil {
			return err
		}
		fmt.Fprintf(m.Out, "Switched %s to %s\n", name, version)
		return nil
	})
}

func (m *Manager) Remove(spec string, all bool) error {
	return m.RemoveMany(context.Background(), []string{spec}, RemoveOptions{All: all})
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
		fmt.Fprintln(m.Out, "No native packages installed. npm/pip installations are managed by their respective package managers.")
		return nil
	}
	// Maintainer metadata is optional here: installed versions remain listable
	// even if the local index is missing or unusable.
	idx, indexErr := m.LoadIndex()
	if indexErr != nil {
		idx = catalog.Index{}
	}
	maintainers := make(map[string]string, len(idx.Packages))
	for _, p := range idx.Packages {
		maintainers[p.Name] = maintainerNames(p.Maintainers)
	}
	table := tabwriter.NewWriter(m.Out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tMANAGER\tVERSIONS (* = active)\tMAINTAINERS")
	for _, name := range names {
		p := s.Packages[name]
		versions := installedVersions(p, "")
		for i, v := range versions {
			if p.Active == v {
				versions[i] += "*"
			}
		}
		who := maintainers[name]
		if who == "" {
			who = "-"
		}
		fmt.Fprintf(table, "%s\toheco\t%s\t%s\n", name, strings.Join(versions, ", "), who)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(m.Out, "npm/pip: installations are managed by their respective package managers; run npm list or pip list directly.")
	return nil
}

func (m *Manager) Search(ctx context.Context, query string) error {
	idx, err := m.LoadIndex()
	if err != nil {
		return err
	}
	return m.searchLocal(ctx, idx, query)
}

func (m *Manager) searchLocal(ctx context.Context, idx catalog.Index, query string) error {
	state, err := m.LoadState()
	if err != nil {
		return err
	}
	query = strings.ToLower(query)
	count := 0
	table := tabwriter.NewWriter(m.Out, 0, 8, 2, ' ', 0)
	for _, p := range idx.Packages {
		if err := ctx.Err(); err != nil {
			return err
		}
		text := p.Name + " " + p.PackageName + " " + p.Manager() + " " + p.Description
		for _, v := range p.Versions {
			for name, project := range v.Projects {
				text += " " + name + " " + project.Description
			}
			for _, a := range v.Artifacts {
				for bin := range a.Binaries {
					text += " " + bin
				}
			}
		}
		if strings.Contains(strings.ToLower(text), query) {
			latest := p.Latest[m.Platform]
			projectVersion, projectErr := p.ProjectVersion("", m.Platform)
			if latest == "" && projectErr == nil && len(projectVersion.Projects) > 0 {
				latest = projectVersion.Version
			}
			size := "-"
			if latest == "" {
				latest = "unavailable for " + m.Platform
			} else if p.Manager() == "oheco" {
				_, a, err := p.Resolve(latest, m.Platform)
				if err != nil && (projectErr != nil || len(projectVersion.Projects) == 0) {
					return err
				}
				if err == nil {
					size = byteSize(float64(a.Size))
				} else {
					var total int64
					for _, project := range projectVersion.Projects {
						total += project.Size
					}
					size = byteSize(float64(total)) + " (projects)"
				}
			}
			if count == 0 {
				fmt.Fprintln(table, "NAME\tMANAGER\tLATEST\tINSTALLED\tSIZE\tMAINTAINERS\tDESCRIPTION")
			}
			installed := "-"
			versions := installedVersions(state.Packages[p.Name], m.Platform)
			if len(versions) > 0 {
				installed = versions[len(versions)-1]
				if len(versions) > 1 {
					installed += fmt.Sprintf(" (%d)", len(versions))
				}
			}
			if p.Manager() != "oheco" {
				installed = "<由 " + p.Manager() + " 管理>"
			}
			description := strings.Join(strings.Fields(p.Description), " ")
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Manager(), latest, installed, size, maintainerNames(p.Maintainers), description)
			count++
		}
	}
	if count == 0 {
		fmt.Fprintln(m.Out, "No matching packages.")
	}
	return table.Flush()
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
	fmt.Fprintf(m.Out, "%s — %s\nPackage manager: %s\nUpstream: %s\nPort: %s\nLicense: %s\n", p.Name, p.Description, p.Manager(), p.Upstream, p.Repository, p.License)
	if p.Manager() != "oheco" {
		fmt.Fprintf(m.Out, "Package name: %s\nInstalled: <由 %s 管理> (management ownership, not an installed-state assertion)\n", p.PackageName, p.Manager())
		for _, v := range p.Versions {
			fmt.Fprintf(m.Out, "%s", v.Version)
			if p.Latest[m.Platform] == v.Version {
				fmt.Fprint(m.Out, " (latest)")
			}
			fmt.Fprintln(m.Out)
			for _, a := range v.PipArtifacts {
				fmt.Fprintf(m.Out, "  %s\n", a.Filename)
			}
			if v.NpmArtifacts != nil {
				fmt.Fprintf(m.Out, "  %s\n", v.NpmArtifacts.Filename)
			}
		}
	}
	for _, who := range p.Maintainers {
		fmt.Fprintf(m.Out, "Maintainer: @%s %s\n", who.GitHub, who.Name)
	}
	for _, v := range p.Versions {
		for _, d := range v.Dependencies {
			basis := d.VersionBasis
			if basis == "" {
				basis = "package"
			}
			platforms := "all artifact platforms"
			if len(d.Platforms) > 0 {
				platforms = strings.Join(d.Platforms, ", ")
			}
			fmt.Fprintf(m.Out, "%s requires %s %s (%s version; %s)\n", v.Version, d.Name, d.Constraint, basis, platforms)
		}
		for _, name := range v.ProjectNames() {
			project := v.Projects[name]
			fmt.Fprintf(m.Out, "%s project %s (%s)\n  oo export %s@%s %s\n  %s\n", v.Version, name, byteSize(float64(project.Size)), p.Name, v.Version, name, project.URL)
			if project.Description != "" {
				fmt.Fprintf(m.Out, "  %s\n", project.Description)
			}
		}
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
