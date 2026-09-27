package notebook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJuliaRef(t *testing.T) {
	cases := []struct{ ref, pkg, version, url, path string }{
		{"DataFrames", "DataFrames", "", "", ""},
		{"Plots@1.40", "Plots", "1.40", "", ""},
		{"https://github.com/org/Foo.jl#main", "Foo", "", "https://github.com/org/Foo.jl#main", ""},
		{"https://github.com/org/Bar.git", "Bar", "", "https://github.com/org/Bar.git", ""},
		{"Baz=https://example.org/repo#v2", "Baz", "", "https://example.org/repo#v2", ""},
		{"./MyPkg", "", "", "", "./MyPkg"},
		{".", "", "", "", "."},
	}
	for _, c := range cases {
		got, err := ParseJuliaRef(c.ref)
		if err != nil {
			t.Errorf("%s: %v", c.ref, err)
			continue
		}
		if got.Package != c.pkg || got.Version != c.version || got.URL != c.url || got.Path != c.path {
			t.Errorf("%s: got %+v", c.ref, got)
		}
	}
	for _, bad := range []string{"", "Data Frames", "Plots@latest", "9lives", "https://x.org/a-b-c", "Pkg; run(`x`)"} {
		if _, err := ParseJuliaRef(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, c := range []struct {
		have, want string
		ok         bool
	}{{"1.40.2", "1.40", true}, {"1.4.2", "1.40", false}, {"1.40.2", "1.40.2", true}, {"1.40.2", "", true}} {
		if juliaVersionOK(c.have, c.want) != c.ok {
			t.Errorf("juliaVersionOK(%s, %s)", c.have, c.want)
		}
	}
}

func TestJuliaManifest(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Project.toml"), []byte("name = \"Mine\"\n[deps]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Manifest.toml"), []byte("julia_version = \"1.12.7\"\nmanifest_format = \"2.0\"\n\n[[deps.Example]]\ngit-tree-sha1 = \"x\"\nuuid = \"7876af07-990d-54b4-ab0e-23690620f79a\"\nversion = \"0.5.5\"\n\n[[deps.Dates]]\nuuid = \"ade2ca70-3891-5945-98fb-dc099432e06a\"\nversion = \"1.11.0\"\n"), 0o644)
	m := juliaManifest(filepath.Join(dir, "Project.toml"))
	if m["Example"] != "0.5.5" || m["Dates"] != "1.11.0" {
		t.Fatalf("manifest = %v", m)
	}
	if juliaProjectName(dir) != "Mine" {
		t.Fatal("project name")
	}
	nb, err := Parse([]byte("---\nrat:\n  julia:\n    dependencies: [DataFrames, ./Mine]\n---\n"))
	if err != nil || nb.Julia == nil || strings.Join(nb.Julia.Dependencies, ",") != "DataFrames,./Mine" {
		t.Fatalf("julia = %+v, %v", nb.Julia, err)
	}
	if _, err := Parse([]byte("---\nrat:\n  julia:\n    dependencies: [\"a b\"]\n---\n")); err == nil || !strings.Contains(err.Error(), "rat.julia.dependencies[0]") {
		t.Fatalf("bad ref accepted: %v", err)
	}
}
