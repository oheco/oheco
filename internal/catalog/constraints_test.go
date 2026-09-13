package catalog

import (
	"sort"
	"strings"
	"testing"
)

func TestSatisfiesNativeConstraints(t *testing.T) {
	for _, tt := range []struct {
		version, constraint string
		want                bool
	}{
		{"1.2.3", "=1.2.3", true},
		{"1.2.3", "==1.2.3", true},
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "=1.2.4", false},
		{"1.2.3", "!=1.2.3", false},
		{"1.2.3", "!=1.2.4", true},
		{"1", "=01.0.0", true},
		{"1.2.0.0", "==1.2", true},
		{"1.2", "!=1.2.0", false},
		{"1.2.3", ">1.2.3", false},
		{"1.2.3", ">=1.2.3", true},
		{"1.2.3", "<1.2.3", false},
		{"1.2.3", "<=1.2.3", true},
		{"1.10", ">1.9", true},
		{"1.9", "<1.10", true},
		{"1.5", ">=1 <2", true},
		{"2", ">=1 <2", false},
		{"1.5", ">= 1, < 2, !=1.6", true},
		{"1.5", ">=1 AND <2", true},
		{"1.5", ">=1 && <2", true},
		{"1.5", " >= 1\t<2\n!=1.6 ", true},
		{"1.5", ">=1\u2003<2", true},
		{"1.5", ">=1 AND\u2003<2 OR\u2003=3", true},
		{"3.5", ">=1,<2 OR >=3,<4", true},
		{"3.5", ">=1,<2 || >=3,<4", true},
		{"3.5", ">=1 and <2 or >=3 and <4", true},
		{"2.5", ">=1,<2 OR >=3,<4", false},
		{"1", "=1 || =2 AND =3", true},
		{"2", "=1 || =2 AND =3", false},
		{"1.2", "*", true},
		{"nightly-build", "*", true},
		{"nightly-build", "=nightly-build", true},
		{"nightly-build", "==nightly-build", true},
		{"nightly-build", "!=nightly-build", false},
		{"nightly-build", "!=other-build", true},
		{"nightly-build", "nightly-build || other-build", true},
		{"snapshot-01", "=snapshot-1", false},
		{"v1.2.3", "=1.2.3", false},
		{"1.2+build.1", "=1.2+build.2", false},
		{"1.2+build.1", "=1.2+build.1", true},
		{"1.2-rc.1", "<1.2", true},
		{"1.2-rc.1", ">=1.2", false},
		{"1.2-rc.2", ">1.2-rc.1 <1.2-rc.10", true},
		{"1.2-alpha", "<1.2-beta", true},
		{"1.2-beta", ">=1.1 <1.3", true}, // no SemVer prerelease exclusion
		{"26.0.0.35-Beta", "<26.0.0.35", true},
		{"26.0.0.35-Beta", ">26.0.0.34 <=26.0.0.35-Beta", true},
		{"26.0.0.35-Beta2", "<26.0.0.35-Beta10", true},
		{"26.0.0.35", ">26.0.0.9", true},
		{"1.2.3.4.5", ">1.2.3.4", true},
		{"1.2.3-ohos.2", ">1.2.3-ohos.1 <1.2.3-ohos.10", true},
		{"1.2.3-ohos.1", ">1.2.3", true},
		{"1.2.3-ohos.0", "=1.2.3", true},
		{"1.2.3-ohos.0002", "=1.2.3-ohos.2", true},
		{"1.2.3-ohos.10", "<1.2.4", true},
		{"1.2.3-rc.1-ohos.10", "<1.2.3", true},
		{"1.2.3-rc.1-ohos.10", ">1.2.3-rc.1-ohos.2", true},
		{"3.5a", ">3.5 <3.5b", true},
		{"3.5a", "=3.5", false},
		{"3.5b", "<3.6", true},
		{"3.5a-ohos.10", ">3.5a-ohos.2 <3.5b", true},
		{"3.10", ">3.9a", true},
		{"999999999999999999999999999999", ">999999999999999999999999999998", true},
	} {
		t.Run(tt.version+"/"+tt.constraint, func(t *testing.T) {
			got, err := Satisfies(tt.version, tt.constraint)
			if err != nil || got != tt.want {
				t.Fatalf("Satisfies(%q, %q) = %v, %v; want %v", tt.version, tt.constraint, got, err, tt.want)
			}
		})
	}
}

func TestSatisfiesRejectsMalformedAndOpaqueRanges(t *testing.T) {
	for _, constraint := range []string{
		"", " ", ">", ">=", "!1", "===1", "=>1", ">=1<2", "1 | 2", "1 & 2",
		"|| 1", "AND 1", "1 ||", "1 OR", "1 AND", "1,", ",1", "1,,2", "1, OR 2",
		"1 || || 2", "1 AND AND 2", "* || >nightly", "1 OR >=snapshot-2", ">=1-ohos.x",
		"^1.2", "~1.2", "1.*", "1.x.*", ">*", "!=*", "(>=1 <2)", "1 - 2", "../../bad",
	} {
		t.Run(constraint, func(t *testing.T) {
			if _, err := Satisfies("1", constraint); err == nil {
				t.Fatalf("accepted constraint %q", constraint)
			}
		})
	}
	for _, version := range []string{"nightly", "v1.2", "1.2+build.1", "1-ohos.x", "3.5aa"} {
		for _, constraint := range []string{">1", ">=1", "<2", "<=2", "* OR >=1"} {
			if _, err := Satisfies(version, constraint); err == nil || !strings.Contains(err.Error(), "opaque") {
				t.Fatalf("opaque range %q %q: %v", version, constraint, err)
			}
		}
	}
	for _, version := range []string{"", "../1", "1 2", strings.Repeat("1", 129)} {
		if _, err := Satisfies(version, "*"); err == nil {
			t.Fatalf("accepted invalid version %q", version)
		}
	}
}

func TestCompareVersionsNativeOrder(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1", "01.0.0", 0},
		{"1.2", "1.2.0.0.0", 0},
		{"1.10", "1.9", 1},
		{"1.0-ohos.10", "1.0-ohos.2", 1},
		{"1.0", "1.0-ohos.0", 0},
		{"1-rc.2", "1-rc.10", -1},
		{"1-rc.1", "1", -1},
		{"1-1", "1-alpha", -1},
		{"1-alpha", "1-alpha.1", -1},
		{"1-rc.1-ohos.9", "1-rc.2", -1},
		{"26.0.0.35-Beta", "26.0.0.35", -1},
		{"26.0.0.35-Beta2", "26.0.0.35-Beta10", -1},
		{"3.5", "3.5a", -1},
		{"3.5a", "3.5b", -1},
		{"3.9b", "3.10", -1},
		{"snapshot-9", "snapshot-10", -1},
		{"snapshot-01", "snapshot-1", -1},
		{"z-999", "1", -1}, // opaque group precedes structured group
	} {
		if got := CompareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := CompareVersions(tt.b, tt.a); got != -tt.want {
			t.Errorf("reverse CompareVersions(%q, %q) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
	versions := []string{"3.10", "3.5a-ohos.10", "3.5", "3.9", "3.5a-ohos.2", "3.5a"}
	sort.Slice(versions, func(i, j int) bool { return CompareVersions(versions[i], versions[j]) < 0 })
	if got := strings.Join(versions, ","); got != "3.5,3.5a,3.5a-ohos.2,3.5a-ohos.10,3.9,3.10" {
		t.Fatal("bad numeric sorting:", got)
	}
}

func TestCompareVersionsTransitive(t *testing.T) {
	versions := []string{"0", "00.0", "0-ohos.1", "1", "1.0", "1-rc.01", "1-rc.1", "1-rc.2", "1-rc.10", "1a", "1a-rc.1", "2", "10", "10-1", "10-a", "3.5a", "3.5a-ohos.1", "3.5a-ohos.10", "26.0.0.35-Beta", "nightly2", "nightly10", "snapshot01", "snapshot1", "v1", "1.0+build"}
	for _, a := range versions {
		for _, b := range versions {
			if CompareVersions(a, b) != -CompareVersions(b, a) {
				t.Fatalf("not antisymmetric: %s %s", a, b)
			}
			for _, c := range versions {
				if CompareVersions(a, b) <= 0 && CompareVersions(b, c) <= 0 && CompareVersions(a, c) > 0 {
					t.Fatalf("not transitive: %s <= %s <= %s", a, b, c)
				}
			}
		}
	}
}

func FuzzSatisfies(f *testing.F) {
	for _, seed := range [][2]string{{"1.2.3", ">=1 <2"}, {"3.5a-ohos.10", "*"}, {"nightly", "=nightly"}, {"26.0.0.35-Beta", "<26.0.0.35"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, version, constraint string) {
		got, err := Satisfies(version, constraint)
		again, againErr := Satisfies(version, constraint)
		if got != again || (err == nil) != (againErr == nil) {
			t.Fatal("nondeterministic constraint evaluation")
		}
	})
}
