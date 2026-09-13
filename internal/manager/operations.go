package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
)

type InstallOptions struct {
	NoSwitch, Yes, DryRun bool
	Args                  []string
}

type RemoveOptions struct {
	All, AutoRemove, Cascade, Yes, DryRun bool
	Args                                  []string
}

type operationTarget struct {
	Name, Backend, Ecosystem, Version string
	Package                           *catalog.Package
}

func resolveTarget(idx catalog.Index, spec string) (operationTarget, error) {
	backend, raw := "", spec
	if prefix, tail, ok := strings.Cut(spec, ":"); ok {
		if prefix != "npm" && prefix != "pip" {
			return operationTarget{}, fmt.Errorf("unknown package manager %q", prefix)
		}
		backend, raw = prefix, tail
	}
	name, version := raw, ""
	if at := strings.LastIndex(raw, "@"); at > 0 {
		name, version = raw[:at], raw[at+1:]
		if version == "" {
			return operationTarget{}, fmt.Errorf("missing version in %q", spec)
		}
	}
	if version != "" && !catalog.ValidComponent(version) {
		return operationTarget{}, fmt.Errorf("invalid version in %q; use an exact version", spec)
	}
	if backend == "" {
		if !catalog.ValidName(name) {
			return operationTarget{}, fmt.Errorf("invalid package name %q; use the catalog name or npm:/pip: prefix", name)
		}
		p, err := idx.Find(name)
		if err != nil {
			return operationTarget{}, err
		}
		return operationTarget{Name: p.Name, Backend: p.Manager(), Ecosystem: p.PackageName, Version: version, Package: &p}, nil
	}
	if !catalog.ValidEcosystemName(backend, name) {
		return operationTarget{}, fmt.Errorf("invalid %s package name %q", backend, name)
	}
	if backend == "pip" {
		name = catalog.NormalizePythonName(name)
	}
	for _, p := range idx.Packages {
		if p.Manager() == backend && p.EcosystemName() == name {
			copy := p
			return operationTarget{Name: p.Name, Backend: backend, Ecosystem: p.PackageName, Version: version, Package: &copy}, nil
		}
	}
	return operationTarget{Name: backend + ":" + name, Backend: backend, Ecosystem: name, Version: version}, nil
}

func validateTail(groups map[string][]string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(groups) == 0 {
		return fmt.Errorf("arguments after -- require an npm or pip target")
	}
	if len(groups) != 1 {
		return fmt.Errorf("arguments after -- cannot target multiple package managers; split npm and pip operations")
	}
	return nil
}

func backendNames(groups map[string][]string) []string {
	var names []string
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *Manager) InstallMany(ctx context.Context, specs []string, options InstallOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(specs) == 0 {
		return fmt.Errorf("install requires at least one package")
	}
	return m.withLock(func() error {
		idx, err := m.LoadIndex()
		if err != nil {
			return err
		}
		before, err := m.LoadState()
		if err != nil {
			return err
		}
		var native []nativeRequest
		groups := map[string][]string{}
		seen := map[string]string{}
		for _, spec := range specs {
			t, err := resolveTarget(idx, spec)
			if err != nil {
				return err
			}
			if t.Backend == "oheco" {
				v, _, err := t.Package.Resolve(t.Version, m.Platform)
				if err != nil {
					return err
				}
				t.Version = v
			} else {
				if options.NoSwitch {
					return fmt.Errorf("--no-switch is only supported for native packages")
				}
				if t.Package != nil {
					if t.Package.Latest[m.Platform] == "" {
						return fmt.Errorf("%s is unavailable for %s", t.Name, m.Platform)
					}
					v, err := t.Package.LanguageVersion(t.Version, m.Platform)
					if err != nil {
						return err
					}
					t.Version = v.Version
				}
			}
			key := t.Backend + ":" + t.Name
			if previous, exists := seen[key]; exists {
				if previous != t.Version {
					return fmt.Errorf("conflicting versions requested for %s", t.Name)
				}
				continue
			}
			seen[key] = t.Version
			if t.Backend == "oheco" {
				native = append(native, nativeRequest{Name: t.Name, Version: t.Version, NoSwitch: options.NoSwitch})
				continue
			}
			spec := t.Ecosystem
			if t.Version != "" {
				separator := "@"
				if t.Backend == "pip" {
					separator = "=="
				}
				spec += separator + t.Version
			}
			groups[t.Backend] = append(groups[t.Backend], spec)
		}
		if err := validateTail(groups, options.Args); err != nil {
			return err
		}
		missing := map[string]bool{}
		for _, backend := range backendNames(groups) {
			if err := validateLanguageTargets(backend, groups[backend], true); err != nil {
				return err
			}
			if err := m.CheckLanguageInstall(ctx, backend, options.Args); err != nil {
				var tool *MissingToolchainError
				if !errors.As(err, &tool) {
					return err
				}
				if tool.Package == "python3" && os.Getenv("OHECO_PYTHON") != "" {
					return fmt.Errorf("%w; fix or unset OHECO_PYTHON first (installing python3 will not override your explicit interpreter)", err)
				}
				fmt.Fprintf(m.Out, "%s is not available: %s\nRecommended toolchain: oo install %s\n", backend, tool.Reason, tool.Package)
				missing[tool.Package] = true
			}
		}
		for _, name := range sortedKeys(missing) {
			found := false
			for _, r := range native {
				if r.Name == name {
					found = true
					break
				}
			}
			if found {
				continue
			}
			p, err := idx.Find(name)
			if err != nil {
				return fmt.Errorf("missing toolchain %s is not available in this index: %w", name, err)
			}
			v, _, err := p.Resolve("", m.Platform)
			if err != nil {
				return err
			}
			// Treat a consented toolchain as user-owned: external environments
			// are not mirrored, so it must never be orphan-collected on their behalf.
			native = append(native, nativeRequest{Name: name, Version: v})
		}
		plan, err := m.planNative(idx, before, native)
		if err != nil {
			return err
		}
		fmt.Fprintln(m.Out, "Installation plan:")
		m.printNativePlan(plan)
		for _, backend := range backendNames(groups) {
			fmt.Fprintf(m.Out, "  %s: %s (the backend's own default scope; change it with arguments after --)\n", backend, strings.Join(groups[backend], ", "))
			fmt.Fprintf(m.Out, "    %s resolves its own dependencies; oo does not track this installation.\n", backend)
		}
		if len(missing) > 0 {
			fmt.Fprintln(m.Out, "The selected toolchain(s) will be installed first. Dependency details will be resolved by the backend afterwards.")
		}
		if options.DryRun {
			fmt.Fprintln(m.Out, "Dry run: no packages downloaded or installed; external dependency plans are not resolved.")
			return nil
		}
		if plan.HasDependencies || len(groups) > 0 || len(missing) > 0 {
			if err := m.confirmContext(ctx, "Continue with this installation and its dependencies?", options.Yes); err != nil {
				return err
			}
		}
		if len(native) > 0 {
			if err := m.executeNativePlan(ctx, plan); err != nil {
				return err
			}
		}
		for _, backend := range backendNames(groups) {
			if err := m.InstallLanguage(ctx, idx, backend, groups[backend], LanguageOptions{Args: options.Args, Yes: true}); err != nil {
				return fmt.Errorf("%s installation failed: %w; completed native/backend operations are retained, later backends were not run", backend, err)
			}
		}
		return nil
	})
}

func sortedKeys(set map[string]bool) []string {
	var keys []string
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (m *Manager) RemoveMany(ctx context.Context, specs []string, options RemoveOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(specs) == 0 {
		return fmt.Errorf("remove requires at least one package")
	}
	return m.withLock(func() error {
		before, err := m.LoadState()
		if err != nil {
			return err
		}
		// Native removal and explicit npm:/pip: names work without the index.
		idx, _ := m.LoadIndex()
		var native []nativeRequest
		groups := map[string][]string{}
		seen := map[string]bool{}
		for _, spec := range specs {
			name, version, nativeErr := SplitSpec(spec)
			if nativeErr == nil {
				if _, exists := before.Packages[name]; exists {
					key := "oheco:" + name + "@" + version
					if !seen[key] {
						native = append(native, nativeRequest{Name: name, Version: version})
						seen[key] = true
					}
					continue
				}
			}
			t, err := resolveTarget(idx, spec)
			if err != nil {
				return err
			}
			if t.Backend == "oheco" {
				return fmt.Errorf("%s is not installed", t.Name)
			}
			if t.Version != "" {
				return fmt.Errorf("%s owns installed versions; remove %s without @version (oo does not keep an external installation mirror)", t.Backend, t.Name)
			}
			if options.All || options.Cascade || options.AutoRemove {
				return fmt.Errorf("--all, --cascade and --autoremove apply only to native packages; split this operation")
			}
			key := t.Backend + ":" + t.Ecosystem
			if !seen[key] {
				groups[t.Backend] = append(groups[t.Backend], t.Ecosystem)
				seen[key] = true
			}
		}
		if err := validateTail(groups, options.Args); err != nil {
			return err
		}
		for _, backend := range backendNames(groups) {
			if err := m.CheckLanguage(ctx, backend, options.Args); err != nil {
				return err
			}
		}
		removing, err := removalKeys(before, native, options.All)
		if err != nil {
			return err
		}
		if options.Cascade {
			expandCascade(before, removing)
		}
		for _, key := range orderedRemoveKeys(removing) {
			if users := reverseUsers(before, key, removing); len(users) != 0 {
				return fmt.Errorf("cannot remove %s@%s; required by %s (remove those packages too or review --cascade --dry-run)", key.Name, key.Version, strings.Join(users, ", "))
			}
		}
		candidates, kept := cleanupCandidates(before, removing)
		fmt.Fprintln(m.Out, "Removal plan:")
		needsConfirmation := len(candidates) > 0 || len(kept) > 0 || options.Cascade || len(groups) > 0
		for _, key := range orderedRemoveKeys(removing) {
			fmt.Fprintf(m.Out, "  remove %s@%s [oheco]\n", key.Name, key.Version)
			if key.Name == "nodejs" || key.Name == "python3" {
				fmt.Fprintln(m.Out, "    WARNING: this toolchain may contain global npm/pip installations. External reverse dependencies are not tracked by oo.")
				needsConfirmation = true
			}
		}
		for _, key := range orderedRemoveKeys(candidates) {
			fmt.Fprintf(m.Out, "  unused automatic dependency: %s@%s\n", key.Name, key.Version)
		}
		for _, key := range orderedRemoveKeys(mapReasons(kept)) {
			fmt.Fprintf(m.Out, "  keep %s@%s: %s\n", key.Name, key.Version, kept[key])
		}
		for _, backend := range backendNames(groups) {
			fmt.Fprintf(m.Out, "  %s removes %s; external reverse dependencies are not guaranteed protected by oo.\n", backend, strings.Join(groups[backend], ", "))
			if backend == "pip" {
				fmt.Fprintln(m.Out, "    pip dependencies are retained; no automatic dependency cleanup.")
			}
		}
		cleanup := options.AutoRemove
		if options.DryRun {
			if cleanup {
				fmt.Fprintln(m.Out, "Dry run: unused automatic native dependencies would also be removed.")
			} else {
				fmt.Fprintln(m.Out, "Dry run: dependencies are retained unless --autoremove is selected.")
			}
			return nil
		}
		if len(candidates) > 0 && !cleanup && !options.Yes {
			cleanup, err = m.askContext(ctx, "Also remove the unused automatic dependencies listed above?")
			if err != nil {
				return err
			}
		}
		if cleanup {
			for key := range candidates {
				removing[key] = true
			}
		} else if len(candidates) > 0 {
			fmt.Fprintln(m.Out, "Keeping unused dependencies; use --autoremove to explicitly include them.")
		}
		after := stateWithout(before, removing)
		if err := after.validate(); err != nil {
			return err
		}
		if err := rebindActive(&after); err != nil {
			return err
		}
		if err := m.checkLinks(stateLinks(before), stateLinks(after), false); err != nil {
			return err
		}
		if needsConfirmation {
			if err := m.confirmContext(ctx, "Continue with this removal?", options.Yes); err != nil {
				return err
			}
		}
		// Uninstall external targets before removing a Node/Python toolchain.
		for _, backend := range backendNames(groups) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := m.RemoveLanguage(ctx, backend, groups[backend], LanguageOptions{Args: options.Args, Yes: true}); err != nil {
				return fmt.Errorf("%s removal failed: %w; native packages were retained; earlier external removals may have completed", backend, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(removing) > 0 {
			if err := m.commit(before, after, "", ""); err != nil {
				return err
			}
			for _, key := range orderedRemoveKeys(removing) {
				fmt.Fprintf(m.Out, "Removed %s@%s\n", key.Name, key.Version)
			}
		}
		return nil
	})
}

func mapReasons(reasons map[removeKey]string) map[removeKey]bool {
	result := map[removeKey]bool{}
	for key := range reasons {
		result[key] = true
	}
	return result
}
