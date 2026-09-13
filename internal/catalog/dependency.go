package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// UnmarshalJSON retains strict field checks for nested versions and rejects a
// present-but-null dependencies field. Omission means no dependencies; a present
// field must be an array (including []), so even an empty declaration reaches
// the schema/manager checks rather than being silently treated as legacy data.
func (v *Version) UnmarshalJSON(data []byte) error {
	type plainVersion Version
	var decoded plainVersion
	if err := Decode(bytes.NewReader(data), &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if strings.EqualFold(name, "dependencies") && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("dependencies must be an array when present, not null")
		}
	}
	*v = Version(decoded)
	return nil
}

// Dependency names another installable native catalog package, not an npm/pip
// distribution or an external installer. An empty VersionBasis means "package";
// an empty Platforms list applies to every artifact platform of the source version.
type Dependency struct {
	Name         string   `json:"name"`
	Constraint   string   `json:"constraint"`
	VersionBasis string   `json:"version_basis,omitempty"`
	Platforms    []string `json:"platforms,omitempty"`
}

func (d Dependency) Validate() error {
	if !ValidName(d.Name) {
		return fmt.Errorf("invalid dependency name %q", d.Name)
	}
	if d.VersionBasis != "" && d.VersionBasis != "package" && d.VersionBasis != "upstream" {
		return fmt.Errorf("dependency %s has invalid version_basis %q", d.Name, d.VersionBasis)
	}
	if _, err := parseConstraint(d.Constraint); err != nil {
		return fmt.Errorf("dependency %s: %w", d.Name, err)
	}
	seen := map[string]bool{}
	for _, platform := range d.Platforms {
		if !ValidPlatform(platform) || seen[platform] {
			return fmt.Errorf("dependency %s has invalid or duplicate platform %q", d.Name, platform)
		}
		seen[platform] = true
	}
	return nil
}

// AppliesTo checks only the platform filter, not target artifact availability.
// Invalid platform strings never apply, including with an unrestricted filter.
func (d Dependency) AppliesTo(platform string) bool {
	if !ValidPlatform(platform) {
		return false
	}
	if len(d.Platforms) == 0 {
		return true
	}
	for _, candidate := range d.Platforms {
		if candidate == platform {
			return true
		}
	}
	return false
}

// Matches evaluates only the version condition, not platform/artifact presence.
// Upstream basis requires an explicit UpstreamVersion, even for *. Missing data
// returns an error: package versions are never guessed or stripped of -ohos.N.
func (d Dependency) Matches(v Version) (bool, error) {
	if err := d.Validate(); err != nil {
		return false, err
	}
	version := v.Version
	if d.VersionBasis == "upstream" {
		if v.UpstreamVersion == "" {
			return false, fmt.Errorf("dependency %s uses upstream basis but %s has no upstream_version", d.Name, v.Version)
		}
		version = v.UpstreamVersion
	}
	return Satisfies(version, d.Constraint)
}

func (p Package) validateDependencies(v Version) error {
	if v.Dependencies == nil {
		return nil
	}
	if p.SchemaVersion < 5 {
		return fmt.Errorf("dependencies require schema_version=5")
	}
	if p.Manager() != "oheco" || len(v.Artifacts) == 0 {
		return fmt.Errorf("dependencies are allowed only on installable native versions")
	}
	seen := map[string]bool{}
	for _, d := range v.Dependencies {
		if err := d.Validate(); err != nil {
			return err
		}
		if d.Name == p.Name {
			return fmt.Errorf("dependency %s references itself", d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("duplicate dependency %s", d.Name)
		}
		seen[d.Name] = true
		for _, platform := range d.Platforms {
			if _, ok := v.Artifacts[platform]; !ok {
				return fmt.Errorf("dependency %s platform %s has no source artifact", d.Name, platform)
			}
		}
	}
	return nil
}

func (idx Index) validateDependencies() error {
	packages := make(map[string]Package, len(idx.Packages))
	for _, p := range idx.Packages {
		packages[p.Name] = p
	}
	for _, p := range idx.Packages {
		for _, v := range p.Versions {
			for _, d := range v.Dependencies {
				target, ok := packages[d.Name]
				if !ok {
					return fmt.Errorf("%s@%s: dependency %s is not in the catalog", p.Name, v.Version, d.Name)
				}
				if target.Manager() != "oheco" {
					return fmt.Errorf("%s@%s: dependency %s is not a native package", p.Name, v.Version, d.Name)
				}
				for platform := range v.Artifacts {
					if !d.AppliesTo(platform) {
						continue
					}
					matched := false
					var matchErr error
					for _, candidate := range target.Versions {
						if _, ok := candidate.Artifacts[platform]; !ok {
							continue
						}
						ok, err := d.Matches(candidate)
						if err != nil {
							// Older/opaque versions need not be candidates if another
							// fully evaluable version satisfies this edge.
							matchErr = err
							continue
						}
						if ok {
							matched = true
							break
						}
					}
					if !matched {
						if matchErr != nil {
							return fmt.Errorf("%s@%s: dependency %s (%s) has no matching native artifact for %s: %w", p.Name, v.Version, d.Name, d.Constraint, platform, matchErr)
						}
						return fmt.Errorf("%s@%s: dependency %s (%s) has no matching native artifact for %s", p.Name, v.Version, d.Name, d.Constraint, platform)
					}
				}
			}
		}
	}
	// Deliberately do not merge all optional version edges into a graph. A
	// package-level cycle may have acyclic selections; the resolver checks the
	// actual selected version/platform graph when planning an installation.
	return nil
}
