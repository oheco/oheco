package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (m *Manager) printNativePlan(plan *nativePlan) {
	for _, step := range plan.Steps {
		action := "install"
		if step.Reused {
			action = "reuse"
		}
		reason := "requested"
		if step.Automatic {
			reason = "automatic dependency"
		}
		activation := "active"
		if !step.Activate {
			activation = "no switch"
		}
		fmt.Fprintf(m.Out, "  %s %s@%s [oheco; %s; %s]\n", action, step.Name, step.Version.Version, reason, activation)
		for _, d := range step.Version.Dependencies {
			if !d.AppliesTo(m.Platform) {
				continue
			}
			basis := d.VersionBasis
			if basis == "" {
				basis = "package"
			}
			fmt.Fprintf(m.Out, "    requires %s %s (%s version)\n", d.Name, d.Constraint, basis)
		}
	}
}

func (m *Manager) executeNativePlan(ctx context.Context, plan *nativePlan) error {
	stages := map[string]string{}
	defer func() {
		for _, stage := range stages {
			_ = os.RemoveAll(stage)
		}
	}()
	// An unchanged dependency may not be an action, but still has to exist.
	for _, required := range plan.Required {
		directory := filepath.Join(m.Root, "packages", required.Name, required.Version.Version)
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("required installed %s@%s is unavailable: %w", required.Name, required.Version.Version, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("required package directory was replaced: %s", directory)
		}
		if err := verifyInstalled(directory, required.Version.Artifacts[m.Platform]); err != nil {
			return fmt.Errorf("required installed %s@%s is damaged: %w", required.Name, required.Version.Version, err)
		}
	}
	// Validate every destination before downloading anything. All native files
	// are staged and verified before the first installation state is changed.
	for _, step := range plan.Steps {
		a := step.Version.Artifacts[m.Platform]
		destination := filepath.Join(m.Root, "packages", step.Name, step.Version.Version)
		if step.Reused {
			if err := verifyInstalled(destination, a); err != nil {
				return err
			}
			continue
		}
		if err := plainDir(filepath.Join(m.Root, "packages", step.Name)); err != nil {
			return err
		}
		if _, err := os.Lstat(destination); err == nil {
			return fmt.Errorf("unmanaged package directory already exists: %s", destination)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, step := range plan.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if step.Reused {
			continue
		}
		a := step.Version.Artifacts[m.Platform]
		archive, err := m.download(ctx, a)
		if err != nil {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Join(m.Root, "tmp"), "install-*")
		if err != nil {
			return err
		}
		stages[filepath.Join(m.Root, "packages", step.Name, step.Version.Version)] = stage
		if err := extract(ctx, archive, stage, a); err != nil {
			return err
		}
		if err := createLaunchers(stage, a); err != nil {
			return err
		}
		if step.Name == "oheco" {
			checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			check := exec.CommandContext(checkCtx, filepath.Join(stage, a.Binaries["oo"]), "--version")
			check.Env = append(os.Environ(), "OHECO_NO_AUTO_UPDATE=1")
			output, err := check.CombinedOutput()
			cancel()
			if err != nil || !strings.HasPrefix(string(output), "oo "+step.Version.Version+" ") {
				return fmt.Errorf("new oo failed version/startup check: %s (%v)", strings.TrimSpace(string(output)), err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkActiveClient(ctx, plan.After, stages); err != nil {
		return err
	}
	if err := m.commitStages(plan.Before, plan.After, stages); err != nil {
		return err
	}
	for _, step := range plan.Steps {
		if step.Reused {
			fmt.Fprintf(m.Out, "%s@%s is already installed", step.Name, step.Version.Version)
		} else {
			fmt.Fprintf(m.Out, "Installed %s@%s", step.Name, step.Version.Version)
		}
		if step.Activate {
			fmt.Fprint(m.Out, " (active)")
		}
		fmt.Fprintln(m.Out)
	}
	return nil
}

// A migrated root must not be left with a default oo that cannot read it.
// This also protects explicit downgrades and direct use of a newer build/oo
// against a root whose managed active client has not been upgraded yet.
func (m *Manager) checkActiveClient(ctx context.Context, after State, stages map[string]string) error {
	p := after.Packages["oheco"]
	if after.SchemaVersion < 2 || p.Active == "" {
		return nil
	}
	directory := filepath.Join(m.Root, "packages", "oheco", p.Active)
	if stage, exists := stages[directory]; exists {
		directory = stage
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	check := exec.CommandContext(checkCtx, filepath.Join(directory, p.Versions[p.Active].Artifact.Binaries["oo"]), "--state-schema")
	check.Env = append(os.Environ(), "OHECO_NO_AUTO_UPDATE=1")
	output, err := check.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "2" {
		return fmt.Errorf("active oheco@%s cannot manage local state schema 2; upgrade the active client to 0.7.0 or newer before this operation (no native changes committed)", p.Active)
	}
	return nil
}

type removeKey struct{ Name, Version string }

func removalKeys(s State, requests []nativeRequest, all bool) (map[removeKey]bool, error) {
	keys := map[removeKey]bool{}
	for _, request := range requests {
		p, exists := s.Packages[request.Name]
		if !exists {
			return nil, fmt.Errorf("%s is not installed", request.Name)
		}
		if all {
			if request.Version != "" {
				return nil, fmt.Errorf("--all cannot be combined with a version")
			}
			for version := range p.Versions {
				keys[removeKey{request.Name, version}] = true
			}
			continue
		}
		version := request.Version
		if version == "" {
			version = p.Active
		}
		if version == "" {
			return nil, fmt.Errorf("%s has no active version; specify name@version or --all", request.Name)
		}
		if _, ok := p.Versions[version]; !ok {
			return nil, fmt.Errorf("%s@%s is not installed", request.Name, version)
		}
		keys[removeKey{request.Name, version}] = true
	}
	return keys, nil
}

func orderedRemoveKeys(keys map[removeKey]bool) []removeKey {
	result := make([]removeKey, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Version < result[j].Version
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func reverseUsers(s State, target removeKey, removing map[removeKey]bool) []string {
	var users []string
	for name, p := range s.Packages {
		for version, r := range p.Versions {
			if removing[removeKey{name, version}] {
				continue
			}
			for _, d := range r.Dependencies {
				if d.Name == target.Name && d.Version == target.Version {
					users = append(users, name+"@"+version)
					break
				}
			}
		}
	}
	sort.Strings(users)
	return users
}

func expandCascade(s State, removing map[removeKey]bool) {
	for changed := true; changed; {
		changed = false
		for name, p := range s.Packages {
			for version, r := range p.Versions {
				key := removeKey{name, version}
				if removing[key] {
					continue
				}
				for _, d := range r.Dependencies {
					if removing[removeKey{d.Name, d.Version}] {
						removing[key] = true
						changed = true
						break
					}
				}
			}
		}
	}
}

// cleanupCandidates computes the whole reachable dependency closure first,
// then protects manually installed/shared nodes and everything they still use.
func cleanupCandidates(s State, roots map[removeKey]bool) (map[removeKey]bool, map[removeKey]string) {
	candidates := map[removeKey]bool{}
	var visit func(removeKey)
	visit = func(key removeKey) {
		for _, d := range s.Packages[key.Name].Versions[key.Version].Dependencies {
			target := removeKey{d.Name, d.Version}
			if roots[target] || candidates[target] {
				continue
			}
			candidates[target] = true
			visit(target)
		}
	}
	for key := range roots {
		visit(key)
	}
	kept := map[removeKey]string{}
	for changed := true; changed; {
		changed = false
		removing := map[removeKey]bool{}
		for key := range roots {
			removing[key] = true
		}
		for key := range candidates {
			removing[key] = true
		}
		for _, key := range orderedRemoveKeys(candidates) {
			r := s.Packages[key.Name].Versions[key.Version]
			reason := ""
			if !r.Automatic {
				reason = "explicitly installed by the user"
			} else if users := reverseUsers(s, key, removing); len(users) != 0 {
				reason = "still required by " + strings.Join(users, ", ")
			}
			if reason != "" {
				delete(candidates, key)
				kept[key] = reason
				changed = true
			}
		}
	}
	return candidates, kept
}

func stateWithout(s State, removing map[removeKey]bool) State {
	after := cloneState(s)
	for key := range removing {
		p := after.Packages[key.Name]
		delete(p.Versions, key.Version)
		if p.Active == key.Version {
			p.Active = ""
		}
		if len(p.Versions) == 0 {
			delete(after.Packages, key.Name)
		} else {
			after.Packages[key.Name] = p
		}
	}
	return after
}
