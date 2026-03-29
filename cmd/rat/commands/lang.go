package commands

import "fmt"

// langAliases maps all accepted language names to their canonical short form.
var langAliases = map[string]string{
	"py":         "py",
	"python":     "py",
	"r":          "r",
	"ju":         "ju",
	"julia":      "ju",
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
	return "", fmt.Errorf("unknown language %q (supported: py, r, ju, sh, js)", name)
}

// isLangAlias returns true if name is a known language name or alias.
func isLangAlias(name string) bool {
	_, ok := langAliases[name]
	return ok
}
