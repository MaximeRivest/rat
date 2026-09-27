package runtimes

import (
	"strings"
	"testing"
)

func TestGuidesCoverEverySystemAndKeepOnlyOne(t *testing.T) {
	for _, lang := range []string{"py", "r", "jl"} {
		if !HasGuide(lang) {
			t.Fatalf("no guide for %s", lang)
		}
		for sys := range systems {
			g, err := Guide(lang, sys, "docs/a.md")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(g, "## On this computer ("+sys+")") {
				t.Errorf("%s guide has no %s section", lang, sys)
			}
			for other := range systems {
				if other != sys && strings.Contains(g, "## "+other) {
					t.Errorf("%s guide for %s keeps the %s section", lang, sys, other)
				}
			}
			if !strings.Contains(g, "rat doctor docs/a.md") || strings.Contains(g, "{notebook}") {
				t.Errorf("%s guide does not name the notebook", lang)
			}
			if !strings.Contains(g, "## For an AI agent doing this") {
				t.Errorf("%s guide has no agent rules", lang)
			}
		}
	}
	if _, err := Guide("cobol", "", ""); err == nil {
		t.Error("a guide for a language rat has none for")
	}
}
