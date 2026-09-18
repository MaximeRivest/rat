package notebook

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sample = `---
title: A tutorial
rat:
  project: ../..
  python:
    requires: ">=3.11"
    dependencies:
      - -e .
      - websockets
---
# Heading

` + "```bash\npip install x\n```\n\n```python\nimport dspy\n```\n\n```output\nnope\n```\n\n~~~r\nx <- 1\n~~~\n"

func TestParseFrontMatterAndCells(t *testing.T) {
	nb, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if !nb.Declared || nb.Manifest.Project != "../.." {
		t.Fatalf("manifest not read: %+v", nb.Manifest)
	}
	if nb.Python == nil || nb.Python.Requires != ">=3.11" || !reflect.DeepEqual(nb.Python.Dependencies, []string{"-e .", "websockets"}) {
		t.Fatalf("python spec: %+v", nb.Python)
	}
	langs := nb.Languages()
	if !reflect.DeepEqual(langs, []string{"py", "r", "sh"}) {
		t.Fatalf("languages: %v", langs)
	}
	if nb.Cells[0].Lang != "sh" || nb.Cells[0].Line != 13 || nb.Cells[0].Code != "pip install x" {
		t.Fatalf("first cell: %+v", nb.Cells[0])
	}
	if nb.Cells[1].Code != "import dspy" {
		t.Fatalf("python cell: %+v", nb.Cells[1])
	}
}

func TestParseNoFrontMatter(t *testing.T) {
	nb, err := Parse([]byte("# Doc\n\n```python\nprint(1)\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if nb.Declared || nb.Python != nil || len(nb.Cells) != 1 || nb.Cells[0].Line != 3 {
		t.Fatalf("unexpected: declared=%v python=%v cells=%+v", nb.Declared, nb.Python, nb.Cells)
	}
}

func TestParseFrontMatterWithoutRatKey(t *testing.T) {
	nb, err := Parse([]byte("---\ntitle: x\n---\ntext\n"))
	if err != nil {
		t.Fatal(err)
	}
	if nb.Declared {
		t.Fatal("no rat key must not count as declared")
	}
}

func TestParseUnterminatedFrontMatterIsBody(t *testing.T) {
	nb, err := Parse([]byte("---\nnot: closed\n\n```python\nx=1\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if nb.Declared || len(nb.Cells) != 1 {
		t.Fatalf("unterminated front matter must be body: %+v", nb)
	}
}

func TestParseRejectsBadRequirement(t *testing.T) {
	_, err := Parse([]byte("---\nrat:\n  python:\n    dependencies: ['']\n---\n"))
	if err == nil || !strings.Contains(err.Error(), "dependencies[0]") {
		t.Fatalf("expected validation error, got %v", err)
	}
	_, err = Parse([]byte("---\nrat:\n  python:\n    requires: 'banana'\n---\n"))
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("expected requires error, got %v", err)
	}
}

func TestPEP723Merge(t *testing.T) {
	doc := "---\nrat:\n  python:\n    dependencies: [websockets]\n---\n" +
		"```python\n# /// script\n# requires-python = \">=3.12\"\n# dependencies = [\n#   \"rich\",\n#   \"websockets\",\n# ]\n# ///\nimport rich\n```\n"
	nb, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !nb.PEP723 {
		t.Fatal("PEP 723 block not detected")
	}
	if nb.Python.Requires != ">=3.12" {
		t.Fatalf("requires from PEP 723 expected, got %q", nb.Python.Requires)
	}
	if !reflect.DeepEqual(nb.Python.Dependencies, []string{"websockets", "rich"}) {
		t.Fatalf("merged deps: %v", nb.Python.Dependencies)
	}
}

func TestPEP723Only(t *testing.T) {
	doc := "```python\n# /// script\n# dependencies = [\"httpx\"]\n# ///\n```\n"
	nb, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if nb.Python == nil || nb.Python.Dependencies[0] != "httpx" || nb.Declared {
		t.Fatalf("unexpected: %+v declared=%v", nb.Python, nb.Declared)
	}
}

func TestCellLanguage(t *testing.T) {
	cases := map[string]string{
		"python": "python", "python title=\"x\"": "python", "{python}": "python",
		"{.python}": "python", "{r, echo=FALSE}": "r", "": "", "PYTHON": "python",
	}
	for in, want := range cases {
		if got := cellLanguage(in); got != want {
			t.Errorf("cellLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectRoot(t *testing.T) {
	nb := &Notebook{Dir: "/a/b/c", Manifest: Manifest{Project: "../.."}}
	root, ok := nb.ProjectRoot()
	if !ok || root != "/a" {
		t.Fatalf("got %q %v", root, ok)
	}
	nb.Manifest.Project = "/x/y"
	root, _ = nb.ProjectRoot()
	if root != "/x/y" {
		t.Fatalf("absolute: %q", root)
	}
	nb.Manifest.Project = ""
	if _, ok := nb.ProjectRoot(); ok {
		t.Fatal("empty project must not pin")
	}
}

func TestProjectPackage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"dspy\"\nversion = \"1\"\n"), 0644)
	if got := ProjectPackage(dir); got != "dspy" {
		t.Fatalf("got %q", got)
	}
	if got := ProjectPackage(t.TempDir()); got != "" {
		t.Fatalf("no pyproject: got %q", got)
	}
}

func TestDistributionName(t *testing.T) {
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, "pyproject.toml"), []byte("[project]\nname = \"my_pkg\"\n"), 0644)
	cases := map[string]string{
		"websockets":              "websockets",
		"Django>=4,<5":            "Django",
		"lm15[extra]==1.0":        "lm15",
		"lm15 @ git+https://x/y":  "lm15",
		"git+https://x/y":         "",
		"-r other.txt":            "",
		"-e .":                    "my_pkg",
		".":                       "my_pkg",
		"--editable ./":           "my_pkg",
		"https://x/y/pkg.whl":     "",
		"  numpy ; python<'3.12'": "numpy",
	}
	for in, want := range cases {
		if got := distributionName(in, proj); got != want {
			t.Errorf("distributionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	if normalizeName("Python_Dotenv") != "python-dotenv" || normalizeName("a.b--c") != "a-b-c" {
		t.Fatal("normalization")
	}
}

func TestVersionSpecifiers(t *testing.T) {
	v := func(s string) Version { x, _ := ParseVersion(s); return x }
	cases := []struct {
		spec, version string
		want          bool
	}{
		{">=3.11", "3.13.13", true},
		{">=3.11", "3.10.12", false},
		{">=3.11,<3.13", "3.13.0", false},
		{">=3.11,<3.13", "3.12.4", true},
		{"==3.12.*", "3.12.9", true},
		{"==3.12.*", "3.13.0", false},
		{"!=3.12.*", "3.13.0", true},
		{"~=3.11", "3.14.0", true},
		{"~=3.11", "4.0.0", false},
		{"~=3.11.2", "3.11.9", true},
		{"~=3.11.2", "3.12.0", false},
		{"3.12", "3.12.0", true},
		{"", "2.7", true},
	}
	for _, c := range cases {
		spec, err := ParseSpecifier(c.spec)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		if got := spec.Matches(v(c.version)); got != c.want {
			t.Errorf("%q matches %s = %v, want %v", c.spec, c.version, got, c.want)
		}
	}
	for _, bad := range []string{">=3.*", "3.11 || 3.12", ">= three", "~=3"} {
		if _, err := ParseSpecifier(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if got, _ := ParseVersion("Python 3.14.0rc1"); got.String() != "3.14.0" {
		t.Fatalf("pre-release: %s", got)
	}
}

func TestRequirementStatus(t *testing.T) {
	repo := t.TempDir()
	pyDir := filepath.Join(repo, "python")
	os.MkdirAll(pyDir, 0755)
	os.WriteFile(filepath.Join(pyDir, "pyproject.toml"), []byte("[project]\nname = \"lmcc\"\n"), 0644)
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "pyproject.toml"), []byte("[project]\nname = \"lmcc\"\n"), 0644)
	installed := map[string]distribution{
		"websockets": {Name: "websockets", Version: "16.1.1"},
		"numpy":      {Name: "numpy", Version: "2.1.0"},
		"lmcc":       {Name: "lmcc", Version: "0.1.0", URL: "file://" + pyDir, Editable: true},
		"lm15":       {Name: "lm15", Version: "0.3", URL: "https://github.com/x/lm15", VCS: "git", RequestedRevision: "main", CommitID: "abc"},
		"pinned":     {Name: "pinned", Version: "1.0.0"},
	}
	rec := receipt{Satisfied: map[string]string{"-r extra.txt": "t", "websockets>1": "t"}}
	type want struct {
		ok      bool
		problem bool
	}
	cases := map[string]want{
		"websockets":   {ok: true},      // plain, installed
		"rich":         {ok: false},     // plain, not installed
		"numpy==2.1.0": {ok: true},      // exact version matches
		"numpy==2.1":   {ok: true},      // 2.1 == 2.1.0
		"numpy==2.0.0": {ok: false},     // wrong version
		"websockets>1": {ok: true},      // constrained: installed + recorded
		"numpy>=1":     {ok: false},     // constrained: installed, never recorded
		"-e ./python":  {ok: true},      // editable from exactly that folder (no receipt needed)
		"./python":     {ok: true},      // non-editable line satisfied by an editable install too
		"-e .":         {problem: true}, // root has no project file: cannot be installed as written
		"-e ./nope":    {problem: true},
		"-e " + other:  {ok: false}, // same name, installed from a different folder
		"lm15 @ git+https://github.com/x/lm15@main": {ok: true},
		"lm15 @ git+https://github.com/x/lm15.git":  {ok: true},
		"lm15 @ git+https://github.com/x/lm15@dev":  {ok: false},
		"lm15 @ git+https://github.com/y/lm15":      {ok: false},
		"-r extra.txt":                              {ok: true},  // unverifiable, recorded
		"-r other.txt":                              {ok: false}, // unverifiable, not recorded
		"https://x/y/pkg.whl":                       {ok: false},
	}
	for line, w := range cases {
		ok, problem := requirementStatus(line, repo, installed, rec)
		if (problem != "") != w.problem || (!w.problem && ok != w.ok) {
			t.Errorf("requirementStatus(%q) = ok=%v problem=%q, want ok=%v problem=%v", line, ok, problem, w.ok, w.problem)
		}
	}
	// The editable list names the lines a notebook here should declare.
	ed := editableInstalls(installed, repo)
	if len(ed) != 1 || ed[0].Line != "-e ./python" || ed[0].Name != "lmcc" {
		t.Fatalf("editable installs: %+v", ed)
	}
}

func TestRelRequirementPath(t *testing.T) {
	cases := map[string]string{"/p": ".", "/p/python": "./python", "/lib/x": "../lib/x"}
	for dir, want := range cases {
		if got := relRequirementPath("/p", dir); got != want {
			t.Errorf("relRequirementPath(/p, %s) = %s, want %s", dir, got, want)
		}
	}
	if u := fileURLPath("file:///home/u/proj/python"); u != "/home/u/proj/python" {
		t.Fatalf("fileURLPath: %q", u)
	}
	if u, r := splitRevision("https://h/x/y@main"); u != "https://h/x/y" || r != "main" {
		t.Fatalf("splitRevision: %q %q", u, r)
	}
	if u, r := splitRevision("https://h/x/y"); u != "https://h/x/y" || r != "" {
		t.Fatalf("splitRevision no rev: %q %q", u, r)
	}
}

func TestReceiptRoundTrip(t *testing.T) {
	venv := t.TempDir()
	r := readReceipt(venv, "/py", "3.12.0")
	if len(r.unsatisfied([]string{"a", "b"})) != 2 {
		t.Fatal("fresh receipt must satisfy nothing")
	}
	if err := recordReceipt(venv, "/py", "3.12.0", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	r = readReceipt(venv, "/py", "3.12.0")
	if got := r.unsatisfied([]string{"a", "b"}); len(got) != 1 || got[0] != "b" {
		t.Fatalf("got %v", got)
	}
	// A different interpreter invalidates everything.
	r = readReceipt(venv, "/py", "3.13.0")
	if len(r.unsatisfied([]string{"a"})) != 1 {
		t.Fatal("receipt must be bound to the interpreter")
	}
}
