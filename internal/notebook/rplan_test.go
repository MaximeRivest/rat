package notebook

import (
	"strings"
	"testing"
)

func TestParseRRef(t *testing.T) {
	cases := []struct {
		ref, pkg, version string
		remote            bool
	}{
		{"dplyr", "dplyr", "", false},
		{"data.table", "data.table", "", false},
		{"ggplot2@3.5.1", "ggplot2", "3.5.1", false},
		{"jsonlite@>=1.8", "jsonlite", ">=1.8", false},
		{"dplyr@current", "dplyr", "", false},
		{"cran::glue", "glue", "", false},
		{"bioc::DESeq2", "DESeq2", "", false},
		{"tidyverse/dplyr@main", "dplyr", "", true},
		{"r-lib/cli#123", "cli", "", true},
		{"github::org/mono/pkgdir@v1", "pkgdir", "", true},
		{"mypkg=github::org/repo", "mypkg", "", true},
		{"thing=url::https://example.org/thing_1.0.tar.gz", "thing", "", true},
	}
	for _, c := range cases {
		got, err := ParseRRef(c.ref)
		if err != nil {
			t.Errorf("%s: %v", c.ref, err)
			continue
		}
		if got.Package != c.pkg || got.Version != c.version || got.Remote != c.remote {
			t.Errorf("%s: got %+v", c.ref, got)
		}
	}
	if r, err := ParseRRef("local::."); err != nil || r.Local != "." || !r.Remote {
		t.Errorf("local::. = %+v, %v", r, err)
	}
	for _, bad := range []string{"", "dplyr; rm -rf /", "url::https://x/y.tar.gz", "2cool", "dplyr@latest!", "foo::bar", "owner/"} {
		if _, err := ParseRRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRVersions(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{
		{"1.2.3", "", true},
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
		{"1.10.0", ">=1.9", true},
		{"1.8.9", ">=1.9", false},
		{"0.2-1", ">=0.2.1", true},
	} {
		if got := rVersionOK(c.have, c.want); got != c.ok {
			t.Errorf("rVersionOK(%s, %s) = %v", c.have, c.want, got)
		}
	}
}

func TestRDependenciesInFrontMatter(t *testing.T) {
	nb, err := Parse([]byte("---\nrat:\n  r:\n    dependencies: [dplyr, tidyverse/ggplot2]\n---\n\n```r\n1\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if nb.R == nil || strings.Join(nb.R.Dependencies, ",") != "dplyr,tidyverse/ggplot2" {
		t.Fatalf("R = %+v", nb.R)
	}
	if _, err := Parse([]byte("---\nrat:\n  r:\n    dependencies: [\"a b\"]\n---\n")); err == nil || !strings.Contains(err.Error(), "rat.r.dependencies[0]") {
		t.Fatalf("bad ref accepted: %v", err)
	}
}
