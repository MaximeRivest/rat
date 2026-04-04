// Package termstyle provides minimal ANSI styling for CLI output.
//
// Respects NO_COLOR (https://no-color.org) and non-TTY output.
// When color is disabled, all style functions return the input unchanged.
package termstyle

import (
	"os"

	"golang.org/x/term"
)

// ANSI escape sequences.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

var enabled bool

func init() {
	_, noColor := os.LookupEnv("NO_COLOR")
	enabled = !noColor && term.IsTerminal(int(os.Stdout.Fd()))
}

func style(code, s string) string {
	if !enabled || s == "" {
		return s
	}
	return code + s + reset
}

// Bold makes text bold (kernel names).
func Bold(s string) string { return style(bold, s) }

// Dim makes text subdued (secondary info like URLs, PIDs).
func Dim(s string) string { return style(dim, s) }

// Green for positive state (running).
func Green(s string) string { return style(green, s) }

// Yellow for warnings (idle).
func Yellow(s string) string { return style(yellow, s) }

// Cyan for links/URLs.
func Cyan(s string) string { return style(cyan, s) }
