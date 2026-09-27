package runtimes

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Guides: how to install a language on this computer, written for a
// person and for their AI alike (guides/<lang>.md). A guide has an
// introduction, one section per system (## macOS, ## Windows, ## Linux,
// ## NixOS) and sections for everyone; Guide keeps the introduction,
// this system's section and the others, and fills in {notebook}.

var systems = map[string]bool{"macOS": true, "Windows": true, "Linux": true, "NixOS": true}

// HasGuide reports whether rat has a setup guide for lang.
func HasGuide(lang string) bool {
	_, err := embedded.ReadFile("guides/" + lang + ".md")
	return err == nil
}

// System names this computer as the guides do.
func System() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	if _, err := os.Stat("/etc/NIXOS"); err == nil {
		return "NixOS"
	}
	return "Linux"
}

// Guide returns the setup guide for lang on system ("" for this one),
// for the notebook at notebook ("" for a placeholder).
func Guide(lang, system, notebook string) (string, error) {
	data, err := embedded.ReadFile("guides/" + lang + ".md")
	if err != nil {
		return "", fmt.Errorf("no setup guide for %q (there are guides for py, r and jl)", lang)
	}
	if system == "" {
		system = System()
	}
	if notebook == "" {
		notebook = "<notebook.md>"
	}
	var out []string
	keep := true
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			keep = !systems[name] || name == system
			if keep && systems[name] {
				line = "## On this computer (" + system + ")"
			}
		}
		if keep {
			out = append(out, line)
		}
	}
	return strings.ReplaceAll(strings.TrimSpace(strings.Join(out, "\n")), "{notebook}", notebook) + "\n", nil
}
