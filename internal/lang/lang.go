// Package lang defines language alias resolution for rat.
//
// Every language name the user can type (py, python, bash, julia, etc.)
// maps to a canonical short form (py, sh, r, jl, js). This package is
// the single source of truth for that mapping.
package lang

import (
	"fmt"
	"strings"
)

// Aliases maps all accepted language names to their canonical short form.
// This must stay in sync with the CLI spec's "Language aliases" section.
var Aliases = map[string]string{
	"py":         "py",
	"python":     "py",
	"r":          "r",
	"jl":         "jl",
	"ju":         "jl",
	"julia":      "jl",
	"sh":         "sh",
	"bash":       "sh",
	"js":         "js",
	"node":       "js",
	"javascript": "js",
}

// Resolve returns the canonical short name for a language,
// or an error if the name isn't recognized.
func Resolve(name string) (string, error) {
	if canon, ok := Aliases[name]; ok {
		return canon, nil
	}
	return "", fmt.Errorf("unknown language %q (supported: py, r, jl, sh, js)", name)
}

// IsAlias returns true if name is a known language name or alias.
func IsAlias(name string) bool {
	_, ok := Aliases[name]
	return ok
}

// InferFromName tries to guess the language from a runtime name.
// It first splits at '-' or '_' separators, then tries known prefixes.
// Returns (canonical lang, true) on success, ("", false) on failure.
func InferFromName(name string) (string, bool) {
	// 1. Exact match
	if canon, ok := Aliases[name]; ok {
		return canon, true
	}

	// 2. Split at '-' or '_' separator: "py-ml" → "py"
	for _, sep := range []string{"-", "_"} {
		if i := strings.Index(name, sep); i > 0 {
			prefix := name[:i]
			if canon, ok := Aliases[prefix]; ok {
				return canon, true
			}
		}
	}

	// 3. Try known aliases as prefixes (longer first to avoid
	//    "py" matching before "python").
	prefixes := []string{
		"python", "javascript", "julia", "bash", "node",
		"py", "sh", "js", "jl", "ju",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return Aliases[p], true
		}
	}

	return "", false
}
