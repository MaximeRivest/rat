package notebook

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a release version: major.minor[.patch]. Pre-release and
// local segments are ignored; for choosing an interpreter that is the
// right granularity.
type Version struct {
	Parts []int
}

var versionRe = regexp.MustCompile(`^\s*v?(\d+(?:\.\d+)*)`)

// ParseVersion reads "3.13.13", "Python 3.12.1", "3.14.0rc1" → 3.14.0.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "Python ")
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("not a version: %q", s)
	}
	var v Version
	for _, p := range strings.Split(m[1], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, fmt.Errorf("not a version: %q", s)
		}
		v.Parts = append(v.Parts, n)
	}
	return v, nil
}

func (v Version) String() string {
	parts := make([]string, len(v.Parts))
	for i, p := range v.Parts {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ".")
}

func (v Version) part(i int) int {
	if i < len(v.Parts) {
		return v.Parts[i]
	}
	return 0
}

// compare returns -1, 0, 1 comparing v to o, padding with zeros.
func (v Version) compare(o Version) int {
	n := len(v.Parts)
	if len(o.Parts) > n {
		n = len(o.Parts)
	}
	for i := 0; i < n; i++ {
		a, b := v.part(i), o.part(i)
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
	}
	return 0
}

// samePrefix reports whether v starts with all of p's parts (for ==3.12.*).
func (v Version) samePrefix(p Version) bool {
	for i := range p.Parts {
		if v.part(i) != p.Parts[i] {
			return false
		}
	}
	return true
}

type clause struct {
	op       string
	version  Version
	wildcard bool
}

// Specifier is a PEP 440 version specifier set restricted to what an
// interpreter requirement needs: >=, >, <=, <, ==, !=, ~= and ".*"
// wildcards, comma separated.
type Specifier struct {
	clauses []clause
	text    string
}

var clauseRe = regexp.MustCompile(`^(~=|==|!=|<=|>=|<|>|===)?\s*v?(\d+(?:\.\d+)*)(\.\*)?$`)

// ParseSpecifier parses ">=3.11,<4" or "==3.12.*".
func ParseSpecifier(s string) (Specifier, error) {
	spec := Specifier{text: strings.TrimSpace(s)}
	if spec.text == "" {
		return spec, nil
	}
	for _, raw := range strings.Split(spec.text, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return Specifier{}, fmt.Errorf("empty clause in %q", s)
		}
		m := clauseRe.FindStringSubmatch(raw)
		if m == nil {
			return Specifier{}, fmt.Errorf("unsupported version clause %q (use >=, >, <=, <, ==, !=, ~=)", raw)
		}
		op := m[1]
		if op == "" || op == "===" {
			op = "=="
		}
		v, err := ParseVersion(m[2])
		if err != nil {
			return Specifier{}, err
		}
		wild := m[3] != ""
		if wild && op != "==" && op != "!=" {
			return Specifier{}, fmt.Errorf("wildcard only allowed with == or != in %q", raw)
		}
		if op == "~=" && len(v.Parts) < 2 {
			return Specifier{}, fmt.Errorf("~= needs at least major.minor in %q", raw)
		}
		spec.clauses = append(spec.clauses, clause{op: op, version: v, wildcard: wild})
	}
	return spec, nil
}

func (s Specifier) String() string { return s.text }

// IsZero reports whether the specifier constrains nothing.
func (s Specifier) IsZero() bool { return len(s.clauses) == 0 }

// Matches reports whether v satisfies every clause.
func (s Specifier) Matches(v Version) bool {
	for _, c := range s.clauses {
		if !c.matches(v) {
			return false
		}
	}
	return true
}

func (c clause) matches(v Version) bool {
	switch c.op {
	case "==":
		if c.wildcard {
			return v.samePrefix(c.version)
		}
		return v.compare(c.version) == 0
	case "!=":
		if c.wildcard {
			return !v.samePrefix(c.version)
		}
		return v.compare(c.version) != 0
	case ">=":
		return v.compare(c.version) >= 0
	case ">":
		return v.compare(c.version) > 0
	case "<=":
		return v.compare(c.version) <= 0
	case "<":
		return v.compare(c.version) < 0
	case "~=":
		// ~=3.11 means >=3.11, ==3.*; ~=3.11.2 means >=3.11.2, ==3.11.*
		if v.compare(c.version) < 0 {
			return false
		}
		prefix := Version{Parts: c.version.Parts[:len(c.version.Parts)-1]}
		return v.samePrefix(prefix)
	}
	return false
}

// UVRequest turns the specifier into what `uv venv --python` accepts. uv
// understands PEP 440 requests directly, so the text passes through.
func (s Specifier) UVRequest() string {
	return s.text
}
