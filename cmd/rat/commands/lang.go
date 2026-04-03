package commands

import (
	"fmt"
	"strings"
)

// langAliases maps all accepted language names to their canonical short form.
var langAliases = map[string]string{
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

// resolveLang returns the canonical short name for a language,
// or an error if the name isn't recognized.
func resolveLang(name string) (string, error) {
	if canon, ok := langAliases[name]; ok {
		return canon, nil
	}
	return "", fmt.Errorf("unknown language %q (supported: py, r, jl, sh, js)", name)
}

// isLangAlias returns true if name is a known language name or alias.
func isLangAlias(name string) bool {
	_, ok := langAliases[name]
	return ok
}

// inferLangFromName tries to guess the language from a runtime name.
// It first splits at '-' or '_' separators, then tries known prefixes.
// Returns (canonical lang, true) on success, ("", false) on failure.
func inferLangFromName(name string) (string, bool) {
	// 1. Exact match
	if canon, ok := langAliases[name]; ok {
		return canon, true
	}

	// 2. Split at '-' or '_' separator: "py-ml" → "py"
	for _, sep := range []string{"-", "_"} {
		if i := strings.Index(name, sep); i > 0 {
			prefix := name[:i]
			if canon, ok := langAliases[prefix]; ok {
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
			return langAliases[p], true
		}
	}

	return "", false
}
