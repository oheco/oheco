package manager

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

type nativeRequest struct {
	Name, Version string
	NoSwitch      bool
}

type nativeStep struct {
	Name                        string
	Version                     catalog.Version
	Reused, Automatic, Activate bool
}

type nativePlan struct {
	Before, After   State
	Steps           []nativeStep
	Required        []nativeStep // every reused version needed by the plan
	HasDependencies bool
}

type nativeRequirement struct {
	Dependency  catalog.Dependency
	Exact, From string
}

func receiptVersion(version string, r Receipt) catalog.Version {
	v := catalog.Version{Version: version, UpstreamVersion: r.UpstreamVersion, Artifacts: map[string]catalog.Artifact{r.Platform: r.Artifact}}
	for _, d := range r.Dependencies {
		v.Dependencies = append(v.Dependencies, d.Dependency)
	}
	return v
}

func sameArtifact(a, b catalog.Artifact) bool {
	// URLs may change mirrors; immutable content and installed layout may not.
	return a.SHA256 == b.SHA256 && a.Size == b.Size && a.Format == b.Format && a.StripComponents == b.StripComponents && reflect.DeepEqual(a.Binaries, b.Binaries) && reflect.DeepEqual(a.Launchers, b.Launchers)
}

func requirementMatches(r nativeRequirement, v catalog.Version) (bool, error) {
	if r.Exact != "" {
		return v.Version == r.Exact, nil
	}
	return r.Dependency.Matches(v)
}

// nativeOrder detects cycles in the selected versions, not the union of every
// version in the catalog. This lets backtracking choose a non-cyclic version.
func nativeOrder(selected map[string]catalog.Version, platform string) ([]string, error) {
	marks := map[string]int{}
	var order, stack []string
	var visit func(string) error
	visit = func(name string) error {
		if marks[name] == 2 {
			return nil
		}
		if marks[name] == 1 {
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(append([]string{}, stack...), name), " -> "))
		}
		marks[name] = 1
		stack = append(stack, name)
		for _, d := range selected[name].Dependencies {
			if d.AppliesTo(platform) {
				if _, ok := selected[d.Name]; !ok {
					return fmt.Errorf("unresolved dependency %s -> %s", name, d.Name)
				}
				if err := visit(d.Name); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		marks[name] = 2
		order = append(order, name)
		return nil
	}
	var names []string
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (m *Manager) planNative(idx catalog.Index, before State, requests []nativeRequest) (*nativePlan, error) {
	plan := &nativePlan{Before: before, After: cloneState(before)}
	if len(requests) == 0 {
		return plan, nil
	}
	pool := map[string][]catalog.Version{}
	for _, p := range idx.Packages {
		if p.Manager() != "oheco" {
			continue
		}
		for _, v := range p.Versions {
			if _, ok := v.Artifacts[m.Platform]; ok {
				pool[p.Name] = append(pool[p.Name], v)
			}
		}
	}
	for name, p := range before.Packages {
		for version, r := range p.Versions {
			if r.Platform != m.Platform {
				return nil, fmt.Errorf("cannot mix platforms in one OHECO_ROOT")
			}
			found := false
			for _, v := range pool[name] {
				if v.Version == version {
					found = true
					break
				}
			}
			if !found {
				pool[name] = append(pool[name], receiptVersion(version, r))
			}
		}
	}
	for name := range pool {
		p := before.Packages[name]
		sort.SliceStable(pool[name], func(i, j int) bool {
			a, b := pool[name][i].Version, pool[name][j].Version
			if (a == p.Active) != (b == p.Active) {
				return a == p.Active
			}
			_, ai := p.Versions[a]
			_, bi := p.Versions[b]
			if ai != bi {
				return ai
			}
			return catalog.CompareVersions(a, b) > 0
		})
	}
	roots := map[string]nativeRequest{}
	passive := map[string]catalog.Version{}
	affected := map[string]bool{}
	var mark func(string)
	mark = func(name string) {
		if affected[name] {
			return
		}
		affected[name] = true
		for _, v := range pool[name] {
			for _, d := range v.Dependencies {
				if d.AppliesTo(m.Platform) {
					mark(d.Name)
				}
			}
		}
	}
	requirements := map[string][]nativeRequirement{}
	for _, request := range requests {
		if prev, exists := roots[request.Name]; exists && prev.Version != request.Version {
			return nil, fmt.Errorf("conflicting versions requested for %s: %s and %s", request.Name, prev.Version, request.Version)
		}
		roots[request.Name] = request
		if request.NoSwitch {
			found := false
			for _, v := range pool[request.Name] {
				if v.Version != request.Version {
					continue
				}
				found = true
				passive[request.Name] = v
				for _, d := range v.Dependencies {
					if d.AppliesTo(m.Platform) {
						mark(d.Name)
						requirements[d.Name] = append(requirements[d.Name], nativeRequirement{Dependency: d, From: "inactive " + request.Name + "@" + request.Version})
					}
				}
				break
			}
			if !found {
				return nil, fmt.Errorf("%s@%s is unavailable for %s", request.Name, request.Version, m.Platform)
			}
			continue
		}
		mark(request.Name)
		requirements[request.Name] = append(requirements[request.Name], nativeRequirement{Exact: request.Version, From: "requested " + request.Name + "@" + request.Version})
	}
	// Keep unrelated active consumers fixed. A dependency which may be changed
	// is selected under all their constraints, rather than silently breaking it.
	for name, p := range before.Packages {
		_, preserveActive := passive[name]
		if p.Active == "" || (affected[name] && !preserveActive) {
			continue
		}
		v := receiptVersion(p.Active, p.Versions[p.Active])
		pool[name] = []catalog.Version{v}
		requirements[name] = append(requirements[name], nativeRequirement{Exact: p.Active, From: "active " + name + "@" + p.Active})
	}
	attempts := 0
	var solve func(map[string]catalog.Version, map[string][]nativeRequirement) (map[string]catalog.Version, error)
	solve = func(selected map[string]catalog.Version, reqs map[string][]nativeRequirement) (map[string]catalog.Version, error) {
		attempts++
		if attempts > 10000 || len(reqs) > 512 {
			return nil, fmt.Errorf("dependency resolution limit exceeded; use more specific constraints")
		}
		var pending []string
		for name, rs := range reqs {
			if v, exists := selected[name]; exists {
				for _, r := range rs {
					ok, err := requirementMatches(r, v)
					if err != nil {
						return nil, fmt.Errorf("%s -> %s: %w", r.From, name, err)
					}
					if !ok {
						return nil, fmt.Errorf("dependency conflict: %s requires %s %s, selected %s", r.From, name, r.Dependency.Constraint+r.Exact, v.Version)
					}
				}
			} else {
				pending = append(pending, name)
			}
		}
		if len(pending) == 0 {
			if _, err := nativeOrder(selected, m.Platform); err != nil {
				return nil, err
			}
			return selected, nil
		}
		sort.Strings(pending)
		name := pending[0]
		var last error
		for _, v := range pool[name] {
			valid := true
			for _, r := range reqs[name] {
				ok, err := requirementMatches(r, v)
				if err != nil {
					last = fmt.Errorf("%s -> %s: %w", r.From, name, err)
					valid = false
					break
				}
				if !ok {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			next := map[string]catalog.Version{}
			for key, value := range selected {
				next[key] = value
			}
			next[name] = v
			nextReqs := map[string][]nativeRequirement{}
			for key, rs := range reqs {
				nextReqs[key] = append([]nativeRequirement{}, rs...)
			}
			for _, d := range v.Dependencies {
				if d.AppliesTo(m.Platform) {
					nextReqs[d.Name] = append(nextReqs[d.Name], nativeRequirement{Dependency: d, From: name + "@" + v.Version})
				}
			}
			result, err := solve(next, nextReqs)
			if err == nil {
				return result, nil
			}
			last = err
		}
		if last != nil {
			return nil, last
		}
		var reasons []string
		for _, r := range reqs[name] {
			reasons = append(reasons, r.From+" requires "+name+" "+r.Dependency.Constraint+r.Exact)
		}
		return nil, fmt.Errorf("dependency conflict: no compatible version for %s on %s: %s", name, m.Platform, strings.Join(reasons, "; "))
	}
	selected, err := solve(map[string]catalog.Version{}, requirements)
	if err != nil {
		return nil, err
	}
	order, err := nativeOrder(selected, m.Platform)
	if err != nil {
		return nil, err
	}
	plan.After.SchemaVersion = 2
	for _, name := range order {
		v := selected[name]
		p := plan.After.Packages[name]
		if p.Versions == nil {
			p.Versions = map[string]Receipt{}
		}
		old, reused := p.Versions[v.Version]
		if reused && !sameArtifact(old.Artifact, v.Artifacts[m.Platform]) {
			return nil, fmt.Errorf("%s@%s is already installed with different contents; publish a new version", name, v.Version)
		}
		request, explicit := roots[name]
		explicit = explicit && request.Version == v.Version
		r := old
		if !reused {
			r = Receipt{Platform: m.Platform, InstalledAt: time.Now().UTC().Format(time.RFC3339), Artifact: v.Artifacts[m.Platform], Automatic: !explicit}
		}
		if explicit {
			r.Automatic = false
		}
		r.UpstreamVersion = v.UpstreamVersion
		r.Dependencies = nil
		for _, d := range v.Dependencies {
			if d.AppliesTo(m.Platform) {
				r.Dependencies = append(r.Dependencies, InstalledDependency{Dependency: d, Version: selected[d.Name].Version})
				if affected[name] {
					plan.HasDependencies = true
				}
			}
		}
		activate := !(explicit && request.NoSwitch)
		if _, noSwitchRoot := passive[name]; noSwitchRoot {
			activate = false
		}
		changed := !reflect.DeepEqual(old, r) || (activate && p.Active != v.Version)
		if activate {
			p.Active = v.Version
		}
		p.Versions[v.Version] = r
		plan.After.Packages[name] = p
		if explicit || changed {
			plan.Steps = append(plan.Steps, nativeStep{Name: name, Version: v, Reused: reused, Automatic: r.Automatic, Activate: activate})
		}
	}
	var passiveNames []string
	for name := range passive {
		passiveNames = append(passiveNames, name)
	}
	sort.Strings(passiveNames)
	for _, name := range passiveNames {
		v := passive[name]
		// Passive roots are stored independently from the selected active version.
		// The same version may also already be active; merge its receipt once.
		p := plan.After.Packages[name]
		if p.Versions == nil {
			p.Versions = map[string]Receipt{}
		}
		r, reused := p.Versions[v.Version]
		if reused && !sameArtifact(r.Artifact, v.Artifacts[m.Platform]) {
			return nil, fmt.Errorf("%s@%s is already installed with different contents; publish a new version", name, v.Version)
		}
		if !reused {
			r = Receipt{Platform: m.Platform, InstalledAt: time.Now().UTC().Format(time.RFC3339), Artifact: v.Artifacts[m.Platform]}
		}
		r.Automatic = false
		r.UpstreamVersion = v.UpstreamVersion
		r.Dependencies = nil
		for _, d := range v.Dependencies {
			if d.AppliesTo(m.Platform) {
				dependency, ok := selected[d.Name]
				if !ok {
					return nil, fmt.Errorf("unresolved dependency of inactive %s@%s: %s", name, v.Version, d.Name)
				}
				r.Dependencies = append(r.Dependencies, InstalledDependency{Dependency: d, Version: dependency.Version})
				plan.HasDependencies = true
			}
		}
		p.Versions[v.Version] = r
		plan.After.Packages[name] = p
		steps := plan.Steps[:0]
		for _, step := range plan.Steps {
			if step.Name == name && step.Version.Version == v.Version {
				reused = step.Reused
				continue
			}
			steps = append(steps, step)
		}
		plan.Steps = append(steps, nativeStep{Name: name, Version: v, Reused: reused})
	}
	for _, name := range order {
		v := selected[name]
		if _, reused := before.Packages[name].Versions[v.Version]; reused {
			plan.Required = append(plan.Required, nativeStep{Name: name, Version: v, Reused: true})
		}
	}
	for _, name := range passiveNames {
		v := passive[name]
		if _, reused := before.Packages[name].Versions[v.Version]; reused {
			plan.Required = append(plan.Required, nativeStep{Name: name, Version: v, Reused: true})
		}
	}
	if err := rebindActive(&plan.After); err != nil {
		return nil, err
	}
	if err := plan.After.validate(); err != nil {
		return nil, err
	}
	if err := m.checkLinks(stateLinks(before), stateLinks(plan.After), false); err != nil {
		return nil, err
	}
	return plan, nil
}

// Only active consumers constrain the shared command environment. Inactive
// versions keep exact dependency bindings so they can be switched back safely.
func rebindActive(s *State) error {
	for name, p := range s.Packages {
		if p.Active == "" {
			continue
		}
		r := p.Versions[p.Active]
		for i, d := range r.Dependencies {
			target := s.Packages[d.Name]
			if target.Active == "" {
				return fmt.Errorf("%s@%s requires active %s %s", name, p.Active, d.Name, d.Constraint)
			}
			actual := target.Versions[target.Active]
			ok, err := d.Matches(receiptVersion(target.Active, actual))
			if err != nil || !ok {
				return fmt.Errorf("%s@%s requires %s %s; active version %s is incompatible", name, p.Active, d.Name, d.Constraint, target.Active)
			}
			r.Dependencies[i].Version = target.Active
		}
		p.Versions[p.Active] = r
		s.Packages[name] = p
	}
	return nil
}
