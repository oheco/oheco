package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// Project is an editable project archive, independent of installable binaries.
// Its map key in Version.Projects is the name accepted by oo export.
type Project struct {
	Description     string `json:"description,omitempty"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	Size            int64  `json:"size"`
	Format          string `json:"format"`
	StripComponents int    `json:"strip_components"`
}

func (p Project) Archive() Artifact {
	return Artifact{URL: p.URL, SHA256: p.SHA256, Size: p.Size, Format: p.Format,
		StripComponents: p.StripComponents, Binaries: map[string]string{}}
}

func (v Version) ProjectNames() []string {
	names := make([]string, 0, len(v.Projects))
	for name := range v.Projects {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProjectVersion follows the platform's latest selection. Export also works on
// other development hosts when all published latest entries agree on a version.
func (p Package) ProjectVersion(version, platform string) (Version, error) {
	if version == "" {
		version = p.Latest[platform]
		if version == "" {
			for _, candidate := range p.Latest {
				if version != "" && version != candidate {
					return Version{}, fmt.Errorf("%s has different latest versions across platforms; specify %s@version", p.Name, p.Name)
				}
				version = candidate
			}
		}
	}
	for _, v := range p.Versions {
		if v.Version == version {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("%s@%s is unavailable; run oo info %s", p.Name, version, p.Name)
}

func (p Package) ResolveProject(version, name, platform string) (string, string, Project, error) {
	v, err := p.ProjectVersion(version, platform)
	if err != nil {
		return "", "", Project{}, err
	}
	names := v.ProjectNames()
	if len(names) == 0 {
		return "", "", Project{}, fmt.Errorf("%s@%s provides no projects; run oo info %s", p.Name, v.Version, p.Name)
	}
	if name == "" {
		if len(names) != 1 {
			return "", "", Project{}, fmt.Errorf("%s@%s provides multiple projects (%s); use oo export %s@%s <project>", p.Name, v.Version, strings.Join(names, ", "), p.Name, v.Version)
		}
		name = names[0]
	}
	project, ok := v.Projects[name]
	if !ok {
		return "", "", Project{}, fmt.Errorf("project %q not found in %s@%s; available: %s", name, p.Name, v.Version, strings.Join(names, ", "))
	}
	return v.Version, name, project, nil
}
