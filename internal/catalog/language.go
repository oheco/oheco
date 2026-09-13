package catalog

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// File records immutable bytes. Registry clients never receive its external URL.
type File struct {
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
}

type PipArtifact struct {
	File
	RequiresPython string   `json:"requires_python,omitempty"`
	RequiresDist   []string `json:"requires_dist,omitempty"`
}

type NpmArtifact struct {
	File
	// PackageJSON is extracted from package/package.json in the tarball.
	// It preserves dependencies, optionalDependencies, engines, os/cpu and bin.
	PackageJSON json.RawMessage `json:"package_json"`
}

var pythonName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
var pythonSeparators = regexp.MustCompile(`[-_.]+`)
var npmName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
var npmVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func NormalizePythonName(s string) string {
	return pythonSeparators.ReplaceAllString(strings.ToLower(s), "-")
}
func ValidEcosystemName(manager, name string) bool {
	if manager == "pip" {
		return pythonName.MatchString(name)
	}
	return manager == "npm" && len(name) <= 214 && npmName.MatchString(name)
}
func (p Package) Manager() string {
	if p.PackageManager == "" {
		return "oheco"
	}
	return p.PackageManager
}
func (p Package) EcosystemName() string {
	if p.Manager() == "pip" {
		return NormalizePythonName(p.PackageName)
	}
	return p.PackageName
}
func (p Package) validateManager() error {
	if p.SchemaVersion < 3 && (p.PackageManager != "" || p.PackageName != "") {
		return fmt.Errorf("package_manager/package_name require schema_version=3")
	}
	if p.Manager() == "oheco" {
		if p.PackageName != "" {
			return fmt.Errorf("native packages must not set package_name")
		}
		return nil
	}
	if p.Name == "oheco" || !ValidEcosystemName(p.Manager(), p.PackageName) {
		return fmt.Errorf("invalid package_manager or package_name")
	}
	return nil
}
func (f File) Validate() error {
	if err := ValidateURL(f.URL); err != nil {
		return err
	}
	if !ValidHash(f.SHA256) || f.Size <= 0 || f.Size > 8<<30 {
		return fmt.Errorf("invalid artifact hash or size")
	}
	if !SafePath(f.Filename) || path.Base(f.Filename) != f.Filename || strings.ContainsAny(f.Filename, "\r\n\"<>#?%") {
		return fmt.Errorf("invalid artifact filename %q", f.Filename)
	}
	return nil
}
func (p Package) validateVersion(v Version) error {
	if err := p.validateDependencies(v); err != nil {
		return err
	}
	if v.Artifacts != nil && len(v.Artifacts) == 0 {
		return fmt.Errorf("artifacts must contain at least one platform when present")
	}
	if v.Projects != nil {
		if p.SchemaVersion < 4 || p.Name == "oheco" {
			return fmt.Errorf("projects require schema_version=4 and are not allowed in the oheco bootstrap package")
		}
		if len(v.Projects) == 0 {
			return fmt.Errorf("projects must contain at least one named project")
		}
		for name, project := range v.Projects {
			if !ValidComponent(name) {
				return fmt.Errorf("invalid project name %q", name)
			}
			if err := project.Archive().Validate(); err != nil {
				return fmt.Errorf("project %s: %w", name, err)
			}
		}
	}
	switch p.Manager() {
	case "oheco":
		if (len(v.Artifacts) == 0 && len(v.Projects) == 0) || v.PipArtifacts != nil || v.NpmArtifacts != nil {
			return fmt.Errorf("native version requires artifacts and/or projects, without pip_artifacts or npm_artifacts")
		}
	case "pip":
		if v.Artifacts != nil || v.NpmArtifacts != nil || len(v.PipArtifacts) == 0 {
			return fmt.Errorf("pip version requires only pip_artifacts")
		}
		seen := map[string]bool{}
		for _, a := range v.PipArtifacts {
			if err := a.File.Validate(); err != nil {
				return err
			}
			parts := strings.Split(strings.TrimSuffix(a.Filename, ".whl"), "-")
			if !strings.HasSuffix(a.Filename, ".whl") || (len(parts) != 5 && len(parts) != 6) || NormalizePythonName(parts[0]) != p.EcosystemName() || parts[1] != v.Version {
				return fmt.Errorf("wheel filename does not match package name/version: %s", a.Filename)
			}
			if seen[a.Filename] {
				return fmt.Errorf("duplicate wheel %s", a.Filename)
			}
			seen[a.Filename] = true
		}
	case "npm":
		if v.Artifacts != nil || v.PipArtifacts != nil || v.NpmArtifacts == nil {
			return fmt.Errorf("npm version requires only npm_artifacts")
		}
		a := v.NpmArtifacts
		if err := a.File.Validate(); err != nil {
			return err
		}
		if !strings.HasSuffix(a.Filename, ".tgz") || !npmVersion.MatchString(v.Version) {
			return fmt.Errorf("npm requires a .tgz and a semantic version")
		}
		var meta map[string]any
		if err := json.Unmarshal(a.PackageJSON, &meta); err != nil {
			return err
		}
		if meta["name"] != p.PackageName || meta["version"] != v.Version {
			return fmt.Errorf("package_json name/version mismatch")
		}
		if _, ok := meta["dist"]; ok {
			return fmt.Errorf("package_json must contain the tarball manifest, without registry dist data")
		}
	}
	return nil
}

func (p Package) LanguageVersion(version, platform string) (Version, error) {
	if version == "" {
		version = p.Latest[platform]
	}
	for _, v := range p.Versions {
		if v.Version == version {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("%s@%s is unavailable for %s", p.Name, version, platform)
}
