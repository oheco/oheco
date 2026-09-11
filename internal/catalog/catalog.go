// Package catalog defines the versioned, platform-specific public package index.
package catalog

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 1
const DefaultURL = "https://oheco.github.io/oheco-packages/index/v1/index.json"

type Maintainer struct {
	GitHub string `json:"github"`
	Name   string `json:"name,omitempty"`
}

type Artifact struct {
	URL             string            `json:"url"`
	SHA256          string            `json:"sha256"`
	Size            int64             `json:"size"`
	Format          string            `json:"format"`
	StripComponents int               `json:"strip_components"`
	Binaries        map[string]string `json:"binaries"`
}

type Version struct {
	Version         string              `json:"version"`
	UpstreamVersion string              `json:"upstream_version,omitempty"`
	Artifacts       map[string]Artifact `json:"artifacts"`
}

type Package struct {
	SchemaVersion int               `json:"schema_version"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Upstream      string            `json:"upstream"`
	Repository    string            `json:"repository"`
	Maintainers   []Maintainer      `json:"maintainers"`
	License       string            `json:"license"`
	Notes         string            `json:"notes,omitempty"`
	Latest        map[string]string `json:"latest"`
	Versions      []Version         `json:"versions"`
}

type Index struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   string    `json:"generated_at"`
	Packages      []Package `json:"packages"`
}

var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,127}$`)
var component = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
var platformName = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)
var githubName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func ValidName(s string) bool      { return packageName.MatchString(s) }
func ValidComponent(s string) bool { return component.MatchString(s) }
func ValidPlatform(s string) bool  { return platformName.MatchString(s) }

func SafePath(s string) bool {
	return s != "" && s != "." && !strings.ContainsAny(s, "\\\x00") && !path.IsAbs(s) &&
		path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

func ValidHash(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 64 && s == strings.ToLower(s) && err == nil
}

// HTTP is accepted only on loopback for local integration tests and previews.
func ValidateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid URL %q", s)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("URL must use HTTPS (HTTP is allowed only on loopback): %s", s)
}

func Decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected data after JSON document")
	}
	return nil
}

func (a Artifact) Validate() error {
	if err := ValidateURL(a.URL); err != nil {
		return err
	}
	if !ValidHash(a.SHA256) {
		return fmt.Errorf("invalid SHA-256")
	}
	if a.Size <= 0 || a.Size > 8<<30 {
		return fmt.Errorf("artifact size must be between 1 byte and 8 GiB")
	}
	if a.Format != "tar.gz" || a.StripComponents < 0 || a.StripComponents > 8 {
		return fmt.Errorf("unsupported archive format or strip_components")
	}
	if len(a.Binaries) == 0 {
		return fmt.Errorf("at least one binary is required")
	}
	for name, p := range a.Binaries {
		if !ValidComponent(name) || !SafePath(p) {
			return fmt.Errorf("invalid binary %q: %q", name, p)
		}
	}
	return nil
}

func (p Package) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported package schema %d", p.SchemaVersion)
	}
	if !ValidName(p.Name) || strings.TrimSpace(p.Description) == "" || p.License == "" {
		return fmt.Errorf("name, description and license are required")
	}
	if err := ValidateURL(p.Upstream); err != nil {
		return err
	}
	u, err := url.Parse(p.Repository)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("repository must be an https://github.com/oheco/<repo> URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "oheco" || !ValidComponent(parts[1]) {
		return fmt.Errorf("repository must belong to the oheco organization")
	}
	if len(p.Maintainers) == 0 {
		return fmt.Errorf("maintainers are required")
	}
	for _, m := range p.Maintainers {
		if !githubName.MatchString(m.GitHub) {
			return fmt.Errorf("invalid maintainer %q", m.GitHub)
		}
	}
	seen := map[string]Version{}
	for _, v := range p.Versions {
		if !ValidComponent(v.Version) || len(v.Artifacts) == 0 {
			return fmt.Errorf("invalid or empty version %q", v.Version)
		}
		if _, ok := seen[v.Version]; ok {
			return fmt.Errorf("duplicate version %s", v.Version)
		}
		seen[v.Version] = v
		for platform, a := range v.Artifacts {
			if !ValidPlatform(platform) {
				return fmt.Errorf("invalid platform %q", platform)
			}
			if err := a.Validate(); err != nil {
				return fmt.Errorf("%s/%s: %w", v.Version, platform, err)
			}
			if p.Name == "oheco" && (len(a.Binaries) != 1 || a.Binaries["oo"] != "bin/oo" || a.StripComponents != 0) {
				return fmt.Errorf("oheco artifacts must expose only oo at bin/oo with strip_components=0")
			}
			for bin := range a.Binaries {
				if bin == "oo" && p.Name != "oheco" {
					return fmt.Errorf("the oo command is reserved for oheco")
				}
				if len(bin)+1+len(v.Version) > 255 {
					return fmt.Errorf("versioned binary name exceeds 255 bytes: %s@%s", bin, v.Version)
				}
			}
		}
	}
	if len(p.Latest) == 0 {
		return fmt.Errorf("latest is required")
	}
	for platform, version := range p.Latest {
		v, ok := seen[version]
		if !ok {
			return fmt.Errorf("latest references missing version %s", version)
		}
		if _, ok := v.Artifacts[platform]; !ok {
			return fmt.Errorf("latest references missing platform %s", platform)
		}
	}
	return nil
}

func (idx Index) Validate() error {
	if idx.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported index schema %d; update oo", idx.SchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, idx.GeneratedAt); err != nil {
		return fmt.Errorf("invalid generated_at: %w", err)
	}
	seen := map[string]bool{}
	owners := map[string]string{}
	for _, p := range idx.Packages {
		if seen[p.Name] {
			return fmt.Errorf("duplicate package %s", p.Name)
		}
		seen[p.Name] = true
		if err := p.Validate(); err != nil {
			return fmt.Errorf("%s: %w", p.Name, err)
		}
		for _, v := range p.Versions {
			for platform, a := range v.Artifacts {
				for bin := range a.Binaries {
					key := platform + "/" + bin
					if owner, ok := owners[key]; ok && owner != p.Name {
						return fmt.Errorf("binary %s is provided by both %s and %s", bin, owner, p.Name)
					}
					owners[key] = p.Name
				}
			}
		}
	}
	return nil
}

func (idx Index) Find(name string) (Package, error) {
	for _, p := range idx.Packages {
		if p.Name == name {
			return p, nil
		}
	}
	return Package{}, fmt.Errorf("package %q not found; try oo search %s or oo update", name, name)
}

func (p Package) Resolve(version, platform string) (string, Artifact, error) {
	if version == "" {
		version = p.Latest[platform]
	}
	for _, v := range p.Versions {
		if v.Version == version {
			if a, ok := v.Artifacts[platform]; ok {
				return version, a, nil
			}
		}
	}
	return "", Artifact{}, fmt.Errorf("%s@%s is unavailable for %s", p.Name, version, platform)
}
