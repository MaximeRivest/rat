// Package notebook makes a Markdown notebook self-describing.
//
// A notebook declares what it needs in its front matter, under one key:
//
//	---
//	rat:
//	  project: ../..            # optional: the project this notebook runs in,
//	                            # relative to the notebook (or absolute)
//	  python:
//	    requires: ">=3.11"      # optional: interpreter version (PEP 440)
//	    dependencies:           # requirements.txt lines, verbatim
//	      - -e .                # this project, editable
//	      - websockets
//	      - lm15 @ git+https://github.com/example/lm15@main
//	  r:                        # any runtime that declares packages.key (runtimeenv.go)
//	    dependencies:
//	      - dplyr
//	---
//
// Python cells may also carry a PEP 723 block (`# /// script` ...
// `# ///`), the same inline-metadata standard `uv run` understands; its
// dependencies and requires-python merge with the front matter.
//
// Nothing here executes anything. Load parses; the plan package decides
// what to do with the result.
package notebook

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/maximerivest/rat/internal/lang"
)

// Manifest is the `rat:` key of a notebook's front matter.
type Manifest struct {
	Project string      `yaml:"project"`
	Python  *PythonSpec `yaml:"python"`
	// Deps holds rat.<key>.dependencies for the runtimes that declare a
	// packages key (r, julia, …); filled by Parse, not by YAML.
	Deps map[string]*DepsSpec `yaml:"-"`
	// After lists notebooks (paths relative to this one) whose cells must
	// have run in the same kernel before this notebook's cells make sense.
	// A declared dependency, never an implicit one: `rat play` runs them
	// first, once per kernel lifetime.
	After []string `yaml:"after"`
}

// PythonSpec declares the Python environment a notebook needs.
type PythonSpec struct {
	Requires     string   `yaml:"requires"`
	Dependencies []string `yaml:"dependencies"`
}

// Cell is a fenced code block with a language rat can run.
type Cell struct {
	Lang string // canonical rat language ("py", "sh", "r", ...)
	Line int    // 1-based line of the opening fence
	Code string
}

// Notebook is a parsed notebook.
type Notebook struct {
	Path     string
	Dir      string
	Manifest Manifest // zero value when the front matter has no `rat:` key
	Declared bool     // true when the front matter declares `rat:`
	Cells    []Cell

	// Python is the effective Python specification: front matter merged
	// with any PEP 723 block found in a python cell. Nil when the notebook
	// declares nothing about Python (cells may still run on the project's
	// default environment).
	Python *PythonSpec
	// PEP723 is true when at least one python cell carries inline metadata.
	PEP723 bool
	// Deps is rat.<key>.dependencies by key, for runtimes other than
	// Python (the front matter's; chains merge them in Doctor).
	Deps map[string]*DepsSpec
}

// Load reads and parses a notebook file.
func Load(path string) (*Notebook, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	nb, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	nb.Path = abs
	nb.Dir = filepath.Dir(abs)
	return nb, nil
}

// Parse parses notebook text. Path and Dir are left empty.
func Parse(data []byte) (*Notebook, error) {
	nb := &Notebook{}
	front, body := splitFrontMatter(data)
	if front != nil {
		var doc struct {
			Rat *Manifest `yaml:"rat"`
		}
		if err := yaml.Unmarshal(front, &doc); err != nil {
			return nil, fmt.Errorf("front matter: %w", err)
		}
		if doc.Rat != nil {
			nb.Declared = true
			nb.Manifest = *doc.Rat
			// Every other key is a runtime's package declaration.
			var raw struct {
				Rat map[string]yaml.Node `yaml:"rat"`
			}
			if err := yaml.Unmarshal(front, &raw); err == nil {
				for key, node := range raw.Rat {
					switch key {
					case "project", "python", "after":
						continue
					}
					var spec DepsSpec
					if err := node.Decode(&spec); err != nil {
						return nil, fmt.Errorf("rat.%s: %w", key, err)
					}
					for i, d := range spec.Dependencies {
						if err := validateDependency(d); err != nil {
							return nil, fmt.Errorf("rat.%s.dependencies[%d]: %w", key, i, err)
						}
					}
					if nb.Manifest.Deps == nil {
						nb.Manifest.Deps = map[string]*DepsSpec{}
					}
					nb.Manifest.Deps[key] = mergeDeps(&spec, nil)
				}
			}
		}
	}
	nb.Cells = parseCells(body, bytes.Count(data, []byte("\n"))-bytes.Count(body, []byte("\n")))

	if err := nb.Manifest.validate(); err != nil {
		return nil, err
	}

	var pep *PythonSpec
	for _, c := range nb.Cells {
		if c.Lang != "py" {
			continue
		}
		spec, found, err := parsePEP723(c.Code)
		if err != nil {
			return nil, fmt.Errorf("python cell at line %d: %w", c.Line, err)
		}
		if found {
			nb.PEP723 = true
			pep = mergePython(pep, spec)
		}
	}
	nb.Python = mergePython(nb.Manifest.Python, pep)
	nb.Deps = nb.Manifest.Deps
	return nb, nil
}

// ProjectRoot returns the pinned project root (absolute) and true, or
// "" and false when the notebook does not pin one.
func (nb *Notebook) ProjectRoot() (string, bool) {
	if nb.Manifest.Project == "" {
		return "", false
	}
	p := nb.Manifest.Project
	if !filepath.IsAbs(p) {
		p = filepath.Join(nb.Dir, p)
	}
	return filepath.Clean(p), true
}

// AfterPaths returns the absolute paths of the notebooks this one
// declares in `rat.after`, in declaration order.
func (nb *Notebook) AfterPaths() []string {
	out := make([]string, 0, len(nb.Manifest.After))
	for _, a := range nb.Manifest.After {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !filepath.IsAbs(a) {
			a = filepath.Join(nb.Dir, a)
		}
		out = append(out, filepath.Clean(a))
	}
	return out
}

// Chain returns the notebooks that must run before nb, in run order
// (dependencies of dependencies first), each once. nb itself is not
// included. An unreadable or cyclic dependency is an error: a notebook
// that cannot state its prerequisites cannot claim to be runnable.
func (nb *Notebook) Chain() ([]*Notebook, error) {
	var order []*Notebook
	seen := map[string]bool{nb.Path: true}
	onPath := map[string]bool{nb.Path: true}
	var visit func(n *Notebook) error
	visit = func(n *Notebook) error {
		for _, p := range n.AfterPaths() {
			if onPath[p] {
				return fmt.Errorf("rat.after cycle: %s ← %s", p, n.Path)
			}
			if seen[p] {
				continue
			}
			dep, err := Load(p)
			if err != nil {
				return fmt.Errorf("rat.after of %s: %w", n.Path, err)
			}
			onPath[p] = true
			if err := visit(dep); err != nil {
				return err
			}
			delete(onPath, p)
			seen[p] = true
			order = append(order, dep)
		}
		return nil
	}
	if err := visit(nb); err != nil {
		return nil, err
	}
	return order, nil
}

// Languages returns the canonical languages used by the notebook's cells,
// sorted, without duplicates.
func (nb *Notebook) Languages() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range nb.Cells {
		if !seen[c.Lang] {
			seen[c.Lang] = true
			out = append(out, c.Lang)
		}
	}
	sort.Strings(out)
	return out
}

func (m Manifest) validate() error {
	if strings.ContainsAny(m.Project, "\n\r") {
		return fmt.Errorf("rat.project must be a path")
	}
	for i, a := range m.After {
		if strings.TrimSpace(a) == "" || strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("rat.after[%d] must be a notebook path", i)
		}
	}
	if m.Python != nil {
		if m.Python.Requires != "" {
			if _, err := ParseSpecifier(m.Python.Requires); err != nil {
				return fmt.Errorf("rat.python.requires: %w", err)
			}
		}
		for i, dep := range m.Python.Dependencies {
			if err := validateRequirement(dep); err != nil {
				return fmt.Errorf("rat.python.dependencies[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// validateRequirement accepts one requirements.txt line. It is
// deliberately permissive about syntax (uv is the authority) and strict
// about shape: one line, non-empty, no shell metacharacters that could
// only be an injection attempt.
func validateRequirement(dep string) error {
	dep = strings.TrimSpace(dep)
	if dep == "" {
		return fmt.Errorf("empty requirement")
	}
	if strings.ContainsAny(dep, "\n\r\x00") {
		return fmt.Errorf("requirement must be a single line")
	}
	if strings.HasPrefix(dep, "#") {
		return fmt.Errorf("comments are not requirements")
	}
	return nil
}

func mergePython(a, b *PythonSpec) *PythonSpec {
	if a == nil && b == nil {
		return nil
	}
	out := &PythonSpec{}
	if a != nil {
		out.Requires = a.Requires
		out.Dependencies = append(out.Dependencies, a.Dependencies...)
	}
	if b != nil {
		if out.Requires == "" {
			out.Requires = b.Requires
		}
		for _, d := range b.Dependencies {
			if !containsString(out.Dependencies, d) {
				out.Dependencies = append(out.Dependencies, d)
			}
		}
	}
	for i := range out.Dependencies {
		out.Dependencies[i] = strings.TrimSpace(out.Dependencies[i])
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if strings.TrimSpace(x) == strings.TrimSpace(s) {
			return true
		}
	}
	return false
}

// splitFrontMatter returns the YAML between leading `---` lines and the
// remaining body. front is nil when there is no front matter.
func splitFrontMatter(data []byte) (front, body []byte) {
	// Tolerate a UTF-8 BOM.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(data, []byte("---")) {
		return nil, data
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 || strings.TrimRight(string(data[:nl]), " \t\r") != "---" {
		return nil, data
	}
	rest := data[nl+1:]
	sc := bufio.NewScanner(bytes.NewReader(rest))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	offset := 0
	for sc.Scan() {
		line := sc.Text()
		lineLen := len(sc.Bytes()) + 1
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "---" || trimmed == "..." {
			return rest[:offset], rest[offset+lineLen:]
		}
		offset += lineLen
	}
	return nil, data // never closed: not front matter
}

var fenceOpen = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})[ \t]*([^`\n]*)$")

// parseCells finds fenced code blocks whose info string names a rat
// language. lineOffset is the number of lines consumed by front matter,
// so reported lines refer to the whole file.
func parseCells(body []byte, lineOffset int) []Cell {
	var cells []Cell
	lines := strings.Split(string(body), "\n")
	for i := 0; i < len(lines); i++ {
		m := fenceOpen.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		fence := m[2]
		info := strings.TrimSpace(m[3])
		if strings.HasPrefix(fence, "`") && strings.Contains(info, "`") {
			continue // not a fence per CommonMark
		}
		langWord := cellLanguage(info)
		var code []string
		closed := false
		j := i + 1
		for ; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, fence[:1]) && strings.Count(t, fence[:1]) == len(t) && len(t) >= len(fence) {
				closed = true
				break
			}
			code = append(code, lines[j])
		}
		if closed {
			if canonical, ok := lang.Resolve(langWord); ok == nil && langWord != "" {
				cells = append(cells, Cell{Lang: canonical, Line: lineOffset + i + 1, Code: strings.Join(code, "\n")})
			}
			i = j
		}
	}
	return cells
}

// cellLanguage extracts the language word from a fence info string:
// "python", "python title=x", "{python}", "{.python}", "python3" → "python".
func cellLanguage(info string) string {
	info = strings.TrimSpace(info)
	if info == "" {
		return ""
	}
	if strings.HasPrefix(info, "{") {
		info = strings.TrimPrefix(info, "{")
		info = strings.TrimPrefix(info, ".")
	}
	word := info
	if i := strings.IndexAny(info, " \t,}"); i >= 0 {
		word = info[:i]
	}
	return strings.ToLower(word)
}

// PEP 723: https://peps.python.org/pep-0723/
var pep723Block = regexp.MustCompile(`(?m)^# /// (?P<type>[a-zA-Z0-9-]+)$\s(?P<content>(^#(| .*)$\s)+)^# ///$`)

func parsePEP723(code string) (*PythonSpec, bool, error) {
	var found *PythonSpec
	for _, m := range pep723Block.FindAllStringSubmatch(code, -1) {
		if m[1] != "script" {
			continue
		}
		if found != nil {
			return nil, false, fmt.Errorf("multiple `# /// script` blocks in one cell")
		}
		var content strings.Builder
		for _, line := range strings.Split(m[2], "\n") {
			if line == "#" {
				content.WriteString("\n")
				continue
			}
			content.WriteString(strings.TrimPrefix(line, "# "))
			content.WriteString("\n")
		}
		var meta struct {
			RequiresPython string   `toml:"requires-python"`
			Dependencies   []string `toml:"dependencies"`
		}
		if _, err := toml.Decode(content.String(), &meta); err != nil {
			return nil, false, fmt.Errorf("`# /// script` block: %w", err)
		}
		found = &PythonSpec{Requires: meta.RequiresPython, Dependencies: meta.Dependencies}
	}
	return found, found != nil, nil
}

// ProjectPackage returns the name declared in <root>/pyproject.toml, or "".
func ProjectPackage(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return ""
	}
	var py struct {
		Project struct {
			Name string `toml:"name"`
		} `toml:"project"`
		Tool struct {
			Poetry struct {
				Name string `toml:"name"`
			} `toml:"poetry"`
		} `toml:"tool"`
	}
	if _, err := toml.Decode(string(data), &py); err != nil {
		return ""
	}
	if py.Project.Name != "" {
		return py.Project.Name
	}
	return py.Tool.Poetry.Name
}
