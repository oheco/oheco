package catalog

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A native version has a numeric core of any length, an optional lowercase
// letter revision (tmux 3.5a), an optional prerelease, then an optional -ohos.N
// packaging revision. Other spellings are opaque, not implicitly SemVer.
var orderedVersion = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)*)([a-z]?)(?:-([0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*))?$`)
var ohosRevision = regexp.MustCompile(`-ohos\.([0-9]+)$`)
var versionTokens = regexp.MustCompile(`[0-9]+|[^0-9]+`)

type comparableVersion struct {
	core       []string
	letter     string
	prerelease string
	revision   string
}

func parseComparableVersion(s string) (comparableVersion, bool) {
	if !ValidComponent(s) {
		return comparableVersion{}, false
	}
	revision := "0"
	if loc := ohosRevision.FindStringSubmatchIndex(s); loc != nil {
		revision = s[loc[2]:loc[3]]
		s = s[:loc[0]]
	}
	// Do not mistake a misspelled/repeated packaging revision for a prerelease.
	if strings.Contains(s, "-ohos.") {
		return comparableVersion{}, false
	}
	m := orderedVersion.FindStringSubmatch(s)
	if m == nil {
		return comparableVersion{}, false
	}
	return comparableVersion{core: strings.Split(m[1], "."), letter: m[2], prerelease: m[3], revision: revision}, true
}

func compareNumber(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// Natural comparison is bounded by the input length and cannot overflow an int.
func compareNatural(a, b string) int {
	aa, bb := versionTokens.FindAllString(a, -1), versionTokens.FindAllString(b, -1)
	for i := 0; i < len(aa) && i < len(bb); i++ {
		var c int
		an, bn := aa[i][0] >= '0' && aa[i][0] <= '9', bb[i][0] >= '0' && bb[i][0] <= '9'
		if an && bn {
			c = compareNumber(aa[i], bb[i])
		} else if an != bn {
			c = 1
			if an {
				c = -1
			}
		} else {
			c = strings.Compare(aa[i], bb[i])
		}
		if c != 0 {
			return c
		}
	}
	if len(aa) < len(bb) {
		return -1
	}
	if len(aa) > len(bb) {
		return 1
	}
	return 0
}

func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	// Dots and hyphens separate identifiers; shorter equal prefixes sort first.
	aa := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' })
	bb := strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' })
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if c := compareNatural(aa[i], bb[i]); c != 0 {
			return c
		}
	}
	if len(aa) < len(bb) {
		return -1
	}
	if len(aa) > len(bb) {
		return 1
	}
	return 0
}

func compareParsed(a, b comparableVersion) int {
	for i := 0; i < len(a.core) || i < len(b.core); i++ {
		x, y := "0", "0"
		if i < len(a.core) {
			x = a.core[i]
		}
		if i < len(b.core) {
			y = b.core[i]
		}
		if c := compareNumber(x, y); c != 0 {
			return c
		}
	}
	if c := strings.Compare(a.letter, b.letter); c != 0 {
		return c
	}
	if c := comparePrerelease(a.prerelease, b.prerelease); c != 0 {
		return c
	}
	return compareNumber(a.revision, b.revision)
}

// CompareVersions returns -1, 0 or 1 for sorting, not for interpreting ranges.
// Numeric cores ignore leading/trailing zero padding (1 == 01.0.0). A letter
// revision follows its unlettered release (3.5 < 3.5a < 3.5b < 3.6). Prereleases
// precede the corresponding release; identifiers are case-sensitive, compared
// naturally (Beta2 < Beta10, rc.2 < rc.10). -ohos.N is compared numerically after
// the base version; a missing revision means zero. Build metadata is not special.
// Opaque versions sort before ordered versions, naturally amongst themselves,
// with a bytewise tiebreaker. Keeping these groups separate preserves transitivity;
// opaque sorting must never be used to authorize an ordered constraint.
func CompareVersions(a, b string) int {
	av, aok := parseComparableVersion(a)
	bv, bok := parseComparableVersion(b)
	if aok && bok {
		return compareParsed(av, bv)
	}
	if aok != bok {
		if aok {
			return 1
		}
		return -1
	}
	if c := compareNatural(a, b); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

type versionPredicate struct {
	op      string
	version string
	parsed  comparableVersion
	ordered bool
}

type versionConstraint [][]versionPredicate

func isRange(op string) bool { return op == ">" || op == ">=" || op == "<" || op == "<=" }

func constraintKeyword(s, keyword string) bool {
	if len(s) < len(keyword) || !strings.EqualFold(s[:len(keyword)], keyword) {
		return false
	}
	if len(s) == len(keyword) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[len(keyword):])
	return unicode.IsSpace(r)
}

func parseConstraint(constraint string) (versionConstraint, error) {
	rest := strings.TrimSpace(constraint)
	if rest == "" {
		return nil, fmt.Errorf("constraint is required; use * for any version")
	}
	clauses := versionConstraint{{}}
	for {
		op := ""
		for _, candidate := range []string{">=", "<=", "==", "!=", ">", "<", "="} {
			if strings.HasPrefix(rest, candidate) {
				op = candidate
				rest = strings.TrimLeftFunc(rest[len(candidate):], unicode.IsSpace)
				break
			}
		}
		end := strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",|&<>=!", r) })
		if end < 0 {
			end = len(rest)
		}
		version := rest[:end]
		if version == "" || (version != "*" && !ValidComponent(version)) || strings.EqualFold(version, "AND") || strings.EqualFold(version, "OR") {
			return nil, fmt.Errorf("invalid version predicate near %q", rest)
		}
		if op == "" {
			op = "="
		}
		if version == "*" && op != "=" && op != "==" {
			return nil, fmt.Errorf("* supports only equality, not %s", op)
		}
		parsed, ordered := parseComparableVersion(version)
		if isRange(op) && !ordered {
			return nil, fmt.Errorf("ordered constraint %s%s uses an opaque version", op, version)
		}
		last := len(clauses) - 1
		clauses[last] = append(clauses[last], versionPredicate{op: op, version: version, parsed: parsed, ordered: ordered})
		tail := rest[end:]
		rest = strings.TrimLeftFunc(tail, unicode.IsSpace)
		if rest == "" {
			return clauses, nil
		}
		switch {
		case strings.HasPrefix(rest, "||"):
			clauses = append(clauses, nil)
			rest = rest[2:]
		case constraintKeyword(rest, "OR"):
			clauses = append(clauses, nil)
			rest = rest[2:]
		case strings.HasPrefix(rest, "&&"):
			rest = rest[2:]
		case constraintKeyword(rest, "AND"):
			rest = rest[3:]
		case strings.HasPrefix(rest, ","):
			rest = rest[1:]
		case len(rest) != len(tail): // whitespace is implicit AND
		default:
			return nil, fmt.Errorf("missing AND/OR separator near %q", rest)
		}
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nil, fmt.Errorf("constraint ends with an AND/OR separator")
		}
	}
}

// Satisfies accepts =, ==, !=, >, >=, <, <=, bare exact versions and *.
// Whitespace, comma, AND or && conjoin predicates; OR or || separates alternatives
// (AND binds tighter). All alternatives are validated, even after a match. There
// are no implicit prerelease exclusions: ordinary ordering decides membership.
// Opaque versions support exact equality/inequality and *, never ordered ranges.
// Parentheses, partial wildcards, ^, ~ and SemVer build-metadata rules are not
// supported. See CompareVersions for the explicitly supported native order.
func Satisfies(version, constraint string) (bool, error) {
	clauses, err := parseConstraint(constraint)
	if err != nil {
		return false, err
	}
	if !ValidComponent(version) {
		return false, fmt.Errorf("invalid version %q", version)
	}
	parsed, ordered := parseComparableVersion(version)
	if !ordered {
		for _, clause := range clauses {
			for _, predicate := range clause {
				if isRange(predicate.op) {
					return false, fmt.Errorf("opaque version %q cannot be evaluated against ordered constraints", version)
				}
			}
		}
	}
	for _, clause := range clauses {
		matches := true
		for _, predicate := range clause {
			if predicate.version == "*" {
				continue
			}
			c := strings.Compare(version, predicate.version)
			if ordered && predicate.ordered {
				c = compareParsed(parsed, predicate.parsed)
			}
			var ok bool
			switch predicate.op {
			case "=", "==":
				ok = c == 0
			case "!=":
				ok = c != 0
			case ">":
				ok = c > 0
			case ">=":
				ok = c >= 0
			case "<":
				ok = c < 0
			case "<=":
				ok = c <= 0
			}
			matches = matches && ok
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}
