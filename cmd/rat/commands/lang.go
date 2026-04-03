package commands

import (
	"github.com/maximerivest/rat/internal/lang"
)

// resolveLang returns the canonical short name for a language,
// or an error if the name isn't recognized.
func resolveLang(name string) (string, error) {
	return lang.Resolve(name)
}

// isLangAlias returns true if name is a known language name or alias.
func isLangAlias(name string) bool {
	return lang.IsAlias(name)
}

// inferLangFromName tries to guess the language from a runtime name.
func inferLangFromName(name string) (string, bool) {
	return lang.InferFromName(name)
}
