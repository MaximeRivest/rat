package notebook

// Packages of runtimes other than Python, declared in a notebook's front
// matter under the key a runtime names (rat.r.dependencies,
// rat.julia.dependencies, …).
//
// rat knows no language here. A runtime.yaml that declares
//
//	packages:
//	  key: r                  # rat.<key>.dependencies
//	  script: packages.R      # next to runtime.yaml
//	  args: [--no-save]       # before the script
//
// gets its packages checked and installed by that script, run by the
// runtime's own binary in the kernel's directory (the project):
//
//	<binary> <args> <script> check|install <project> [--force] [--update] -- <dependency>...
//
// check answers on stdout, one fact per line, tab-separated (no JSON: a
// runtime without a JSON library — base R, Julia — must still be able to
// say what it lacks):
//
//	version      <runtime version>
//	environment  <where packages go>
//	lock         <the lock file, committed with the project>
//	requirement  <line>          every effective line, in order (repeated)
//	missing      <line>          what install must install (repeated)
//	problem      <line> <why>    a line that cannot be installed as written
//	restart      true            installing changes a package a running kernel may have loaded
//	relock       true            nothing to install, but the lock is missing or out of date
//	detail       <one line>      what the check found
//	hint         <text>          how a person fixes a problem
//	summary      <command>       the install, as a person would type it
//	info         <key> <value>   facts for clients (library, renv, …)
//
// install and lock get the same arguments (the whole declaration: the
// script works out again what is missing). install installs and writes
// the lock; lock only writes it. What they print is the step's log, and
// exit status 0 means done.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/generic"
	resolver "github.com/maximerivest/rat/internal/resolve"
	"github.com/maximerivest/rat/internal/runtimes"
	"github.com/maximerivest/rat/internal/state"
)

// DepsSpec is rat.<key> for a runtime other than Python.
type DepsSpec struct {
	Dependencies []string `yaml:"dependencies"`
}

// EnvState is what a runtime's packages script found.
type EnvState struct {
	Key           string   `json:"key"`
	Lang          string   `json:"lang"`
	Kernel        string   `json:"kernel"`
	KernelRunning bool     `json:"kernel_running"`
	Version       string   `json:"version,omitempty"`
	Environment   string   `json:"environment,omitempty"`
	Lock          string   `json:"lock,omitempty"`
	Requirements  []string `json:"requirements"`
	Missing       []string `json:"missing,omitempty"`
	Restart       bool     `json:"restart,omitempty"`
	Relock        bool     `json:"relock,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	// Info carries the script's own facts (library, renv, …), flattened
	// into the JSON beside the fields above.
	Info map[string]string `json:"-"`

	binary string
	args   []string
	script string
	cwd    string
	deps   []string
}

// MarshalJSON flattens Info into the object.
func (e *EnvState) MarshalJSON() ([]byte, error) {
	type plain EnvState
	raw, err := json.Marshal((*plain)(e))
	if err != nil {
		return nil, err
	}
	if len(e.Info) == 0 {
		return raw, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, v := range e.Info {
		if _, taken := m[k]; taken {
			continue
		}
		switch v {
		case "true":
			m[k] = true
		case "false":
			m[k] = false
		default:
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// packagedRuntime is a runtime with a packages script.
type packagedRuntime struct {
	lang string
	cfg  *generic.RuntimeConfig
	dir  string
}

// runtimeConfig finds a runtime.yaml as `rat` does: the user's
// (~/.config/rat/runtimes/<lang>) first, then the built-in one.
func runtimeConfig(lang string) (*generic.RuntimeConfig, string) {
	if dir, err := os.UserConfigDir(); err == nil {
		p := filepath.Join(dir, "rat", "runtimes", lang, "runtime.yaml")
		if cfg, err := generic.LoadConfig(p); err == nil {
			return cfg, filepath.Dir(p)
		}
	}
	if !runtimes.IsBuiltin(lang) {
		return nil, ""
	}
	p, err := runtimes.Extract(lang)
	if err != nil {
		return nil, ""
	}
	cfg, err := generic.LoadConfig(p)
	if err != nil {
		return nil, ""
	}
	return cfg, filepath.Dir(p)
}

// packagedRuntimes lists the runtimes (built-in, and the user's) whose
// packages a notebook may declare, by front-matter key.
func packagedRuntimes() map[string]packagedRuntime {
	langs := map[string]bool{}
	for _, l := range runtimes.List() {
		langs[l] = true
	}
	if dir, err := os.UserConfigDir(); err == nil {
		entries, _ := os.ReadDir(filepath.Join(dir, "rat", "runtimes"))
		for _, e := range entries {
			if e.IsDir() {
				langs[e.Name()] = true
			}
		}
	}
	out := map[string]packagedRuntime{}
	for lang := range langs {
		cfg, dir := runtimeConfig(lang)
		if cfg == nil || cfg.Packages.Key == "" || cfg.Packages.Script == "" {
			continue
		}
		out[cfg.Packages.Key] = packagedRuntime{lang: lang, cfg: cfg, dir: dir}
	}
	return out
}

// PackageKeys returns the front-matter keys runtimes declare (sorted).
func PackageKeys() []string {
	var keys []string
	for k := range packagedRuntimes() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mergeDeps(a, b *DepsSpec) *DepsSpec {
	if a == nil && b == nil {
		return nil
	}
	out := &DepsSpec{}
	for _, spec := range []*DepsSpec{a, b} {
		if spec == nil {
			continue
		}
		for _, d := range spec.Dependencies {
			if d = strings.TrimSpace(d); !containsString(out.Dependencies, d) {
				out.Dependencies = append(out.Dependencies, d)
			}
		}
	}
	return out
}

// validateDependency checks the shape of one line; what it means is the
// runtime's script's business.
func validateDependency(dep string) error {
	dep = strings.TrimSpace(dep)
	if dep == "" {
		return fmt.Errorf("empty dependency")
	}
	if strings.ContainsAny(dep, "\n\r\x00") {
		return fmt.Errorf("a dependency is one line")
	}
	if strings.HasPrefix(dep, "-") {
		return fmt.Errorf("%q: a dependency does not start with -", dep)
	}
	return nil
}

func doctorEnv(store *state.Store, nb *Notebook, r *Report, opts *Options, key string, rt packagedRuntime, spec *DepsSpec) error {
	es := &EnvState{Key: key, Lang: rt.lang, Requirements: []string{}, Info: map[string]string{}}
	r.Envs[key] = es
	binary := rt.cfg.Detect.Env
	if binary != "" {
		binary = opts.getenv(binary)
	}
	if binary == "" {
		for _, c := range rt.cfg.Detect.Commands {
			if p, err := opts.lookPath(c); err == nil {
				binary = p
				break
			}
		}
	}
	if binary == "" && opts.LookPath == nil {
		binary = rt.cfg.KnownPath()
	}
	if binary == "" {
		return nil // the tool check says the runtime is missing
	}
	res, err := resolver.ResolveWith(store, rt.lang, resolver.Options{Cwd: nb.Dir, ProjectRoot: r.Project})
	if err != nil {
		return err
	}
	es.Kernel = res.Name
	es.cwd = res.Cwd
	if es.cwd == "" {
		es.cwd = r.Project
	}
	if k, _ := store.GetRunning(res.Name); k != nil {
		es.KernelRunning = true
	}
	es.binary = binary
	es.args = rt.cfg.Packages.Args
	es.script = rt.cfg.Packages.Script
	if !filepath.IsAbs(es.script) {
		es.script = filepath.Join(rt.dir, es.script)
	}
	if spec != nil {
		es.deps = spec.Dependencies
	}

	label := rt.cfg.Display + " packages"
	out, err := runCommand(es.command("check", r.Project, opts, es.deps), es.cwd, 3*time.Minute)
	if err != nil {
		r.Checks = append(r.Checks, Check{ID: "packages-" + key, Label: label,
			Detail: "could not check: " + lastLines(out+"\n"+err.Error(), 6),
			Hint:   "check that " + filepath.Base(binary) + " runs in " + es.cwd})
		r.Blocked = true
		return nil
	}
	var problems []string
	detail, hint := "", ""
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "version":
			es.Version = f[1]
		case "environment":
			es.Environment = f[1]
		case "lock":
			es.Lock = f[1]
		case "requirement":
			es.Requirements = append(es.Requirements, f[1])
		case "missing":
			es.Missing = append(es.Missing, f[1])
		case "problem":
			why := ""
			if len(f) > 2 {
				why = f[2]
			}
			problems = append(problems, f[1]+": "+why)
		case "restart":
			es.Restart = f[1] == "true"
		case "relock":
			es.Relock = f[1] == "true"
		case "detail":
			detail = f[1]
		case "hint":
			hint = f[1]
		case "summary":
			es.Summary = f[1]
		case "info":
			if len(f) > 2 {
				es.Info[f[1]] = f[2]
			}
		}
	}
	if len(problems) > 0 {
		if hint == "" {
			hint = "fix the line in the notebook's front matter (rat." + key + ".dependencies)"
		}
		r.Checks = append(r.Checks, Check{ID: "packages-" + key, Label: label, Detail: strings.Join(problems, "; "), Hint: hint})
		r.Blocked = true
		return nil
	}
	if len(es.Missing) == 0 {
		if detail == "" {
			detail = fmt.Sprintf("%d satisfied", len(es.Requirements))
		}
		r.Checks = append(r.Checks, Check{ID: "packages-" + key, Label: label, OK: true, Detail: detail})
		return nil
	}
	if detail == "" {
		detail = fmt.Sprintf("%d to install: %s", len(es.Missing), strings.Join(es.Missing, ", "))
	}
	r.Checks = append(r.Checks, Check{ID: "packages-" + key, Label: label, Detail: detail, Hint: hint})
	effect := ""
	if es.Restart && es.KernelRunning {
		effect = "the " + rt.cfg.Display + " kernel restarts — a package it may have loaded changes version; variables reset"
	}
	summary := []string{filepath.Base(binary), es.script, "install"}
	if es.Summary != "" {
		summary = []string{es.Summary}
	}
	r.Actions = append(r.Actions, Action{ID: "install:" + key, Label: "install " + label,
		Command: summary, Dir: es.cwd, Reason: "declared by the notebook", Effect: effect})
	return nil
}

func (es *EnvState) command(verb, project string, opts *Options, deps []string) []string {
	argv := append([]string{es.binary}, es.args...)
	argv = append(argv, es.script, verb, project)
	if opts.Force {
		argv = append(argv, "--force")
	}
	if opts.Update {
		argv = append(argv, "--update")
	}
	argv = append(argv, "--")
	return append(argv, deps...)
}

// ensureEnv runs the install for one runtime; it reports whether the
// runtime's kernel must restart.
func ensureEnv(es *EnvState, project string, opts *Options) (string, bool, error) {
	if err := writeRatGitignore(project); err != nil {
		return "", false, err
	}
	out, err := runCommand(es.command("install", project, opts, es.deps), es.cwd, 60*time.Minute)
	if err != nil {
		return out, false, err
	}
	return out, es.Restart && es.KernelRunning, nil
}

// lockEnv writes a runtime's lock when nothing needed installing (every
// package was already there) but the lock is missing or out of date.
func lockEnv(es *EnvState, project string, opts *Options) (string, error) {
	if err := writeRatGitignore(project); err != nil {
		return "", err
	}
	return runCommand(es.command("lock", project, opts, es.deps), es.cwd, 10*time.Minute)
}

// ratGitignore keeps rat's state out of Git but its lock files in:
// libraries are rebuilt from the locks on another machine.
const ratGitignore = `# Created by rat. Libraries and environments are rebuilt from the
# lock files, which belong in Git with the notebooks.
*
!.gitignore
!r.lock
!python.lock
!julia/
julia/*
!julia/Project.toml
!julia/Manifest.toml
`

// writeRatGitignore writes <project>/.rat/.gitignore, replacing one an
// older rat wrote (which kept everything out, locks included).
func writeRatGitignore(project string) error {
	dir := filepath.Join(project, ".rat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, ".gitignore")
	old, err := os.ReadFile(p)
	if err == nil && !strings.HasPrefix(string(old), "# Created by rat") {
		return nil // the person's own
	}
	if string(old) == ratGitignore {
		return nil
	}
	return os.WriteFile(p, []byte(ratGitignore), 0o644)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func sortedEnvKeys(m map[string]*EnvState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HasGuide reports whether rat has a setup guide for lang (`rat guide`).
func HasGuide(lang string) bool { return runtimes.HasGuide(lang) }
