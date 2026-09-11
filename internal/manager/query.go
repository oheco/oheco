package manager

import (
	"sort"
	"strings"

	"github.com/oheco/oheco/internal/catalog"
)

func maintainerNames(maintainers []catalog.Maintainer) string {
	names := make([]string, 0, len(maintainers))
	for _, who := range maintainers {
		name := strings.Join(strings.Fields(who.Name), " ")
		if name == "" {
			name = "@" + who.GitHub
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ", ")
}

func installedVersions(p InstalledPackage, platform string) []string {
	versions := make([]string, 0, len(p.Versions))
	for version, receipt := range p.Versions {
		if platform == "" || receipt.Platform == platform {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return compareVersion(versions[i], versions[j]) < 0 })
	return versions
}

func compareVersion(a, b string) int {
	// Version labels also include four-part SDK versions and OHOS revisions.
	// Compare numeric components without integer conversion or overflow.
	core := func(v string) (string, string) {
		v, _, _ = strings.Cut(v, "+")
		i := 0
		for i < len(v) && (v[i] >= '0' && v[i] <= '9' || v[i] == '.') {
			i++
		}
		return v[:i], v[i:]
	}
	ac, as := core(a)
	bc, bs := core(b)
	if ac != "" && bc != "" {
		if n := naturalCompare(ac, bc); n != 0 {
			return n
		}
		stage := func(s string) int {
			if s == "" {
				return 1
			}
			if strings.HasPrefix(strings.ToLower(s), "-ohos") {
				return 2
			}
			return 0
		}
		if n := stage(as) - stage(bs); n != 0 {
			return n
		}
		if n := naturalCompare(as, bs); n != 0 {
			return n
		}
	}
	if n := naturalCompare(a, b); n != 0 {
		return n
	}
	return strings.Compare(a, b)
}

func naturalCompare(a, b string) int {
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	for len(a) > 0 && len(b) > 0 {
		if digit(a[0]) && digit(b[0]) {
			i, j := 0, 0
			for i < len(a) && digit(a[i]) {
				i++
			}
			for j < len(b) && digit(b[j]) {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(x) != len(y) {
				return len(x) - len(y)
			}
			if n := strings.Compare(x, y); n != 0 {
				return n
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}
