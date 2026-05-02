// Package runtimeid defines the safe identifier format for rat runtime names.
package runtimeid

import (
	"fmt"
	"regexp"
	"strings"
)

var validNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]*$`)

const allowedNameDescription = "letters, numbers, '.', '_', '-', and '@'; must start with a letter or number"

// IsValidName reports whether name is safe to use as a rat runtime name.
func IsValidName(name string) bool {
	return validNameRE.MatchString(name)
}

// ValidateName returns an error if name is not safe to use as a rat runtime
// name. Runtime names are used in cache/log paths, so path separators and
// shell-special names are intentionally rejected.
func ValidateName(name string) error {
	if IsValidName(name) {
		return nil
	}
	return fmt.Errorf("invalid runtime name %q: use only %s", name, allowedNameDescription)
}

// SlugPart turns a user/project-facing label into a safe runtime-name segment.
// It is used for generated names such as "py@<project>" so projects named
// "my app" produce a safe runtime name like "py@my-app".
func SlugPart(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	dash := false
	for _, r := range s {
		if isSlugRune(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), ".-_")
	if out == "" {
		return "project"
	}
	return out
}

func isSlugRune(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '.' || r == '_' || r == '-'
}
