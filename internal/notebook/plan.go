package notebook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/project"
	resolver "github.com/maximerivest/rat/internal/resolve"
	"github.com/maximerivest/rat/internal/state"
)

// RatRequirements are installed into every notebook environment on top of
// what the notebook declares: the kernel completes with jedi and the human
// REPL (`rat py`) is IPython. They are rat's contract with the venv, the
// same one `rat install py` establishes.
var RatRequirements = []string{"ipython", "jedi"}

// ratRequirements honours RAT_NOTEBOOK_REQUIREMENTS (comma separated; empty
// string for none) so offline tests and air-gapped machines can opt out.
func ratRequirements(opts *Options) []string {
	v, set := lookupEnv(opts, "RAT_NOTEBOOK_REQUIREMENTS")
	if !set {
		return RatRequirements
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func lookupEnv(opts *Options, key string) (string, bool) {
	if opts.Getenv != nil {
		v := opts.Getenv(key)
		return v, v != ""
	}
	return os.LookupEnv(key)
}

// Report is the result of Doctor (a plan) or Ensure (a plan and what was
// done about it). It is the JSON integrations consume.
type Report struct {
	Notebook       string   `json:"notebook"`
	Project        string   `json:"project"`
	ProjectSource  string   `json:"project_source"` // "front matter" | "detected"
	ProjectPackage string   `json:"project_package,omitempty"`
	Declared       bool     `json:"declared"` // front matter has a rat: key
	Languages      []string `json:"languages"`

	Python *PythonState `json:"python,omitempty"`
	// Envs: the packages of runtimes other than Python, by front-matter
	// key; in JSON each is a top-level field of its key ("r", "julia").
	Envs map[string]*EnvState `json:"-"`
	// After lists the notebooks this one declares as prerequisites, in run
	// order, with whether each has already run in the current kernels.
	After []AfterState `json:"after"`

	Checks  []Check  `json:"checks"`
	Actions []Action `json:"actions"`
	Steps   []Step   `json:"steps"`

	// OK: every check passes and nothing remains to do.
	OK bool `json:"ok"`
	// Blocked: ensure cannot carry out its Python plan until a person
	// fixes something (no way to create a venv, no installer, a version
	// mismatch that needs --recreate, a pinned project that is missing).
	Blocked bool `json:"blocked"`
}

// PythonState describes the Python environment the notebook resolves to.
type PythonState struct {
	Kernel       string   `json:"kernel"`
	Venv         string   `json:"venv"`
	VenvExists   bool     `json:"venv_exists"`
	Python       string   `json:"python,omitempty"`
	Version      string   `json:"version,omitempty"`
	Requires     string   `json:"requires,omitempty"`
	RequiresOK   bool     `json:"requires_ok"`
	Requirements []string `json:"requirements"` // effective requirement lines
	Missing      []string `json:"missing,omitempty"`
	// Editable lists the packages installed editable (from a local
	// checkout) in the environment, as the requirement lines that would
	// reproduce them relative to the project root — what a notebook in
	// this project should declare for the project's own code.
	Editable      []EditableInstall `json:"editable,omitempty"`
	UV            string            `json:"uv,omitempty"`
	KernelRunning bool              `json:"kernel_running"`
	KernelVenv    string            `json:"kernel_venv,omitempty"` // live binding when running
	PEP723        bool              `json:"pep723,omitempty"`
	// Lock: .rat/python.lock, the environment's versions as `pip freeze`
	// wrote them after the last ensure (editable installs aside: they are
	// declared). Its pins are wanted like declared lines.
	Lock   string `json:"lock,omitempty"`
	Relock bool   `json:"relock,omitempty"`
}

// EditableInstall is one local package installed editable in the venv.
type EditableInstall struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute source directory
	Line string `json:"line"` // "-e ./python" relative to the project root
}

// AfterState is one prerequisite notebook of the chain.
type AfterState struct {
	Path   string `json:"path"`
	Played bool   `json:"played"` // its cells ran to completion in the kernels it needs, which are still running
}

// Check is one verified fact about the environment.
type Check struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"` // how a person fixes it when ensure cannot
}

// Action is one thing Ensure will do.
type Action struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	// Effect names the user-visible consequence ("kernel restarts —
	// variables reset"), so callers can say it before it happens.
	Effect string `json:"effect,omitempty"`
}

// Step is one executed Action.
type Step struct {
	Action  Action  `json:"action"`
	OK      bool    `json:"ok"`
	Output  string  `json:"output,omitempty"`
	Seconds float64 `json:"seconds"`
}

// Options control Doctor and Ensure.
type Options struct {
	// Force reinstalls every requirement even when the receipt says it is
	// satisfied.
	Force bool
	// Recreate allows Ensure to delete and rebuild a venv whose interpreter
	// does not satisfy `requires`. Destructive; never implied.
	Recreate bool
	// Update resolves the declarations again instead of reproducing the
	// lock files, and writes new locks.
	Update bool
	// Progress receives each step as it completes (Ensure only).
	Progress func(Step)
	// LookPath and Getenv are injectable for tests.
	LookPath func(string) (string, error)
	Getenv   func(string) string
}

func (o *Options) lookPath(name string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath(name)
	}
	return exec.LookPath(name)
}

func (o *Options) getenv(k string) string {
	if o.Getenv != nil {
		return o.Getenv(k)
	}
	return os.Getenv(k)
}

// Doctor inspects the notebook's environment and returns the plan Ensure
// would execute. It changes nothing.
func Doctor(store *state.Store, nb *Notebook, opts Options) (*Report, error) {
	r := &Report{Notebook: nb.Path, Declared: nb.Declared, Languages: nb.Languages(),
		After: []AfterState{}, Checks: []Check{}, Actions: []Action{}, Steps: []Step{}}
	if r.Languages == nil {
		r.Languages = []string{}
	}

	// Project root.
	if root, pinned := nb.ProjectRoot(); pinned {
		r.Project = root
		r.ProjectSource = "front matter"
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			r.Checks = append(r.Checks, Check{ID: "project", Label: "project",
				Detail: fmt.Sprintf("rat.project points to %s, which is not a directory", root),
				Hint:   "fix the `rat.project` path in the notebook's front matter"})
			r.Blocked = true
			return r, nil
		}
	} else {
		r.Project, _ = project.FindRoot(nb.Dir)
		r.ProjectSource = "detected"
	}
	r.ProjectPackage = ProjectPackage(r.Project)
	r.Checks = append(r.Checks, Check{ID: "project", Label: "project", OK: true,
		Detail: fmt.Sprintf("%s (%s)", r.Project, r.ProjectSource)})

	// Prerequisite chain (rat.after): must load, must not cycle, and must
	// live in this project — a chain across projects would span kernels,
	// and state does not flow between kernels.
	chain, err := nb.Chain()
	if err != nil {
		r.Checks = append(r.Checks, Check{ID: "after", Label: "after", Detail: err.Error(),
			Hint: "fix the `rat.after` paths in the notebook's front matter"})
		r.Blocked = true
		return r, nil
	}
	var chainPython []*PythonSpec
	var chainDeps []map[string]*DepsSpec
	for _, dep := range chain {
		depRoot, pinned := dep.ProjectRoot()
		if !pinned {
			depRoot, _ = project.FindRoot(dep.Dir)
		}
		if depRoot != r.Project {
			r.Checks = append(r.Checks, Check{ID: "after", Label: "after",
				Detail: fmt.Sprintf("%s belongs to project %s, not %s", rel(nb.Dir, dep.Path), depRoot, r.Project),
				Hint:   "a chain must stay inside one project (one kernel); pin `rat.project` in both notebooks"})
			r.Blocked = true
			return r, nil
		}
		r.After = append(r.After, AfterState{Path: dep.Path, Played: playedIn(store, dep, depRoot)})
		if dep.Python != nil {
			chainPython = append(chainPython, dep.Python)
		}
		if dep.Deps != nil {
			chainDeps = append(chainDeps, dep.Deps)
		}
		for _, l := range dep.Languages() {
			if !containsString(r.Languages, l) {
				r.Languages = append(r.Languages, l)
			}
		}
	}
	sort.Strings(r.Languages)
	if len(chain) > 0 {
		var names []string
		pending := 0
		for _, a := range r.After {
			n := rel(nb.Dir, a.Path)
			if !a.Played {
				pending++
				n += " (not yet run)"
			}
			names = append(names, n)
		}
		r.Checks = append(r.Checks, Check{ID: "after", Label: "after", OK: true,
			Detail: strings.Join(names, ", ") + map[bool]string{true: " — rat play runs them first", false: ""}[pending > 0]})
	}
	// The chain's requirements are this notebook's too: they share a kernel.
	effective := nb.Python
	for i := len(chainPython) - 1; i >= 0; i-- {
		effective = mergePython(chainPython[i], effective)
	}
	effectiveDeps := map[string]*DepsSpec{}
	for k, v := range nb.Deps {
		effectiveDeps[k] = v
	}
	for i := len(chainDeps) - 1; i >= 0; i-- {
		for k, v := range chainDeps[i] {
			effectiveDeps[k] = mergeDeps(v, effectiveDeps[k])
		}
	}
	nb = &Notebook{Path: nb.Path, Dir: nb.Dir, Manifest: nb.Manifest, Declared: nb.Declared, Cells: nb.Cells, Python: effective, PEP723: nb.PEP723, Deps: effectiveDeps}

	// Tools for non-Python cells. Rat does not install the runtimes
	// themselves: report them, but do not let a missing R or tmux stop the
	// Python work.
	for _, l := range r.Languages {
		if c, ok := toolCheck(l, &opts); ok {
			r.Checks = append(r.Checks, c)
		}
	}

	needsPython := nb.Python != nil || containsString(r.Languages, "py")
	if needsPython {
		if err := doctorPython(store, nb, r, &opts); err != nil {
			return nil, err
		}
	}
	// Every other runtime's packages, through its own script.
	r.Envs = map[string]*EnvState{}
	runtimesByKey := packagedRuntimes()
	var keys []string
	for key := range nb.Deps {
		keys = append(keys, key)
	}
	for key, rt := range runtimesByKey {
		if nb.Deps[key] == nil && containsString(r.Languages, rt.lang) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		rt, ok := runtimesByKey[key]
		if !ok {
			r.Checks = append(r.Checks, Check{ID: "packages-" + key, Label: "rat." + key,
				Detail: "no runtime declares packages under rat." + key,
				Hint:   "known keys: python, " + strings.Join(PackageKeys(), ", ")})
			r.Blocked = true
			continue
		}
		if err := doctorEnv(store, nb, r, &opts, key, rt, nb.Deps[key]); err != nil {
			return nil, err
		}
	}

	r.OK = !r.Blocked && len(r.Actions) == 0
	for _, c := range r.Checks {
		if !c.OK {
			r.OK = false
		}
	}
	return r, nil
}

func toolCheck(lang string, opts *Options) (Check, bool) {
	var bins []string
	switch lang {
	case "sh":
		if runtime.GOOS == "windows" {
			bins = []string{"pwsh", "powershell"}
		} else {
			bins = []string{"tmux", "bash"}
		}
	case "r":
		bins = []string{"Rscript"}
	case "jl":
		bins = []string{"julia"}
	case "js":
		bins = []string{"node"}
	case "pi":
		bins = []string{"pi"}
	default:
		return Check{}, false
	}
	var missing, found []string
	for _, b := range bins {
		if p, err := opts.lookPath(b); err == nil {
			found = append(found, p)
		} else if cfg, _ := runtimeConfig(lang); cfg != nil && opts.LookPath == nil && cfg.KnownPath() != "" {
			found = append(found, cfg.KnownPath()) // where an installer put it
		} else {
			missing = append(missing, b)
		}
	}
	c := Check{ID: "tool-" + lang, Label: lang + " cells", OK: len(missing) == 0}
	if c.OK {
		c.Detail = strings.Join(found, ", ")
	} else {
		c.Detail = "missing: " + strings.Join(missing, ", ")
		c.Hint = "install " + strings.Join(missing, " and ")
		if HasGuide(lang) {
			c.Hint += " — `rat guide " + lang + "` shows how, for a person or their AI"
		}
	}
	return c, true
}

func doctorPython(store *state.Store, nb *Notebook, r *Report, opts *Options) error {
	ps := &PythonState{PEP723: nb.PEP723}
	r.Python = ps

	res, err := resolver.ResolveWith(store, "py", resolver.Options{Cwd: nb.Dir, ProjectRoot: r.Project})
	if err != nil {
		return err
	}
	ps.Kernel = res.Name
	if k, _ := store.GetRunning(res.Name); k != nil {
		ps.KernelRunning = true
		ps.KernelVenv = k.Venv
	}

	// The environment is the project's, deterministically: an activated
	// shell venv would make the terminal and a background service disagree.
	if v := opts.getenv("VIRTUAL_ENV"); v != "" {
		if abs, _ := filepath.Abs(v); abs != res.Venv && res.Venv != "" {
			r.Checks = append(r.Checks, Check{ID: "virtual-env", Label: "VIRTUAL_ENV", OK: true,
				Detail: fmt.Sprintf("ignored (%s); notebooks use the project's environment", v)})
		}
	}

	ps.Venv = res.Venv
	if ps.Venv == "" {
		ps.Venv = filepath.Join(r.Project, ".venv")
	}
	ps.Python = project.VenvPython(ps.Venv)
	ps.VenvExists = ps.Python != ""
	if !ps.VenvExists {
		ps.Python = expectedVenvPython(ps.Venv)
	}

	spec := Specifier{}
	if nb.Python != nil && nb.Python.Requires != "" {
		spec, _ = ParseSpecifier(nb.Python.Requires)
		ps.Requires = spec.String()
	}

	uv := findUV(opts)
	ps.UV = uv

	// Requirements: what the notebook declares plus rat's own.
	if nb.Python != nil {
		ps.Requirements = append(ps.Requirements, nb.Python.Dependencies...)
	}
	for _, rq := range ratRequirements(opts) {
		if !containsString(ps.Requirements, rq) {
			ps.Requirements = append(ps.Requirements, rq)
		}
	}

	// ── venv ──
	if !ps.VenvExists {
		cmd := venvCreateCommand(uv, ps.Venv, spec, opts)
		if cmd == nil {
			r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: false,
				Detail: "no venv and no way to create one",
				Hint:   "install uv — `rat guide py` shows how, for a person or their AI"})
			r.Blocked = true
			return nil
		}
		r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: false,
			Detail: fmt.Sprintf("%s does not exist", ps.Venv)})
		r.Actions = append(r.Actions, Action{ID: "create-venv", Label: "create " + rel(r.Project, ps.Venv),
			Command: cmd, Dir: r.Project,
			Reason: "the notebook's project has no environment yet",
			Effect: kernelEffect(ps, "binds to the new environment")})
		ps.RequiresOK = true // uv picks a matching interpreter
	} else {
		ver, err := pythonVersion(ps.Python)
		if err != nil {
			r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: false,
				Detail: fmt.Sprintf("%s: %v", ps.Python, err),
				Hint:   "the venv looks broken; remove it and run `rat ensure` again"})
			r.Blocked = true
			return nil
		}
		ps.Version = ver.String()
		ps.RequiresOK = spec.IsZero() || spec.Matches(ver)
		if ps.RequiresOK {
			r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: true,
				Detail: fmt.Sprintf("%s · Python %s", ps.Venv, ps.Version)})
		} else {
			detail := fmt.Sprintf("%s has Python %s; the notebook requires %s", ps.Venv, ps.Version, ps.Requires)
			if opts.Recreate {
				cmd := venvCreateCommand(uv, ps.Venv, spec, opts)
				if cmd == nil {
					r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: false, Detail: detail,
						Hint: "install uv to get a matching interpreter"})
					r.Blocked = true
					return nil
				}
				r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: false, Detail: detail})
				r.Actions = append(r.Actions, Action{ID: "recreate-venv", Label: "recreate " + rel(r.Project, ps.Venv),
					Command: cmd, Dir: r.Project,
					Reason: "the interpreter does not satisfy `requires` (--recreate)",
					Effect: "the existing environment is deleted; " + kernelEffect(ps, "restarts")})
				ps.RequiresOK = true
			} else {
				r.Checks = append(r.Checks, Check{ID: "venv", Label: "environment", OK: false, Detail: detail,
					Hint: "run `rat ensure --recreate` to rebuild the environment (its installed packages are lost)"})
				r.Blocked = true
				return nil
			}
		}
	}

	// ── requirements ──
	if len(ps.Requirements) > 0 {
		var missing []string
		ps.Lock = filepath.Join(r.Project, ".rat", "python.lock")
		var pins []string
		if !opts.Update {
			pins = readPythonLock(ps.Lock)
		}
		switch {
		case opts.Force, !ps.VenvExists, hasAction(r.Actions, "recreate-venv"):
			missing = append(missing, ps.Requirements...)
			missing = append(missing, pins...)
		default:
			receipt := readReceipt(ps.Venv, ps.Python, ps.Version)
			installed, err := installedDistributions(ps.Python)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", ps.Python, err)
			}
			ps.Editable = editableInstalls(installed, r.Project)
			var problems []string
			for _, line := range ps.Requirements {
				ok, problem := requirementStatus(line, r.Project, installed, receipt)
				if problem != "" {
					problems = append(problems, problem)
					continue
				}
				if !ok {
					missing = append(missing, line)
				}
			}
			for _, pin := range pins {
				name, version, _ := strings.Cut(pin, "==")
				if d, ok := installed[normalizeName(strings.TrimSpace(name))]; !ok || d.Version != strings.TrimSpace(version) {
					missing = append(missing, pin)
				}
			}
			if len(problems) > 0 {
				// A line that cannot be installed as written: report it, do not
				// try (uv would fail, and the kernel would restart for nothing).
				hint := "fix the line in the notebook's front matter"
				if len(ps.Editable) > 0 {
					var lines []string
					for _, e := range ps.Editable {
						lines = append(lines, e.Line+" ("+e.Name+")")
					}
					hint += "; this project's own packages are installed from: " + strings.Join(lines, ", ")
				}
				r.Checks = append(r.Checks, Check{ID: "requirements", Label: "requirements", OK: false,
					Detail: strings.Join(problems, "; "), Hint: hint})
				r.Blocked = true
				return nil
			}
		}
		ps.Missing = missing
		ps.Relock = len(missing) == 0 && (opts.Update || !fileExists(ps.Lock))
		if len(missing) == 0 {
			r.Checks = append(r.Checks, Check{ID: "requirements", Label: "requirements", OK: true,
				Detail: fmt.Sprintf("%d satisfied", len(ps.Requirements))})
		} else {
			cmd := installCommand(uv, ps.Python, opts)
			if cmd == nil {
				r.Checks = append(r.Checks, Check{ID: "requirements", Label: "requirements", OK: false,
					Detail: "cannot install: neither uv nor pip is available",
					Hint:   "install uv (https://docs.astral.sh/uv/)"})
				r.Blocked = true
				return nil
			}
			r.Checks = append(r.Checks, Check{ID: "requirements", Label: "requirements", OK: false,
				Detail: fmt.Sprintf("%d to install: %s", len(missing), strings.Join(missing, ", "))})
			editable := anyEditable(missing)
			effect := ""
			if editable {
				effect = kernelEffect(ps, "restarts — editable installs need a fresh interpreter")
			}
			r.Actions = append(r.Actions, Action{ID: "install", Label: "install requirements",
				Command: append(cmd, "-r", "<requirements>"), Dir: r.Project,
				Reason: "declared by the notebook (and rat's REPL needs)",
				Effect: effect})
		}
	}

	// ── kernel binding ──
	if ps.KernelRunning && ps.KernelVenv != ps.Venv && !hasAction(r.Actions, "create-venv") && !hasAction(r.Actions, "recreate-venv") {
		r.Checks = append(r.Checks, Check{ID: "kernel", Label: "kernel", OK: false,
			Detail: fmt.Sprintf("%s is running on %s, not %s", ps.Kernel, orNone(ps.KernelVenv), ps.Venv)})
		r.Actions = append(r.Actions, Action{ID: "restart-kernel", Label: "restart " + ps.Kernel,
			Command: []string{"rat", "restart", ps.Kernel}, Dir: r.Project,
			Reason: "the kernel is bound to a different environment",
			Effect: "kernel restarts — variables reset"})
	} else if ps.KernelRunning {
		r.Checks = append(r.Checks, Check{ID: "kernel", Label: "kernel", OK: true,
			Detail: ps.Kernel + " running on " + orNone(ps.KernelVenv)})
	} else {
		r.Checks = append(r.Checks, Check{ID: "kernel", Label: "kernel", OK: true,
			Detail: ps.Kernel + " (starts on first run)"})
	}
	return nil
}

// Ensure executes the plan Doctor produces, then re-inspects so the
// returned report describes the environment as it is now.
func Ensure(store *state.Store, nb *Notebook, opts Options) (*Report, error) {
	plan, err := Doctor(store, nb, opts)
	if err != nil {
		return nil, err
	}
	if plan.Blocked {
		return plan, nil
	}
	// Locks come with ensure: what runs is recorded, also when every
	// package was installed already (by Nix, by hand, by an older rat).
	var lockSteps []Step
	if ps := plan.Python; ps != nil && ps.Relock && ps.VenvExists && !hasAction(plan.Actions, "install") {
		t0 := time.Now()
		out, err := writePythonLock(ps, plan.Project, &opts)
		step := Step{Action: Action{ID: "lock:python", Label: "write " + rel(plan.Project, ps.Lock)}, OK: err == nil, Output: out, Seconds: time.Since(t0).Seconds()}
		if err != nil {
			step.Output = err.Error()
		}
		lockSteps = append(lockSteps, step)
		if opts.Progress != nil {
			opts.Progress(step)
		}
	}
	for _, key := range sortedEnvKeys(plan.Envs) {
		es := plan.Envs[key]
		if !es.Relock || hasAction(plan.Actions, "install:"+key) {
			continue
		}
		t0 := time.Now()
		out, err := lockEnv(es, plan.Project, &opts)
		step := Step{Action: Action{ID: "lock:" + key, Label: "write " + rel(plan.Project, es.Lock)}, OK: err == nil,
			Output: strings.TrimSpace(out), Seconds: time.Since(t0).Seconds()}
		if err != nil {
			step.Output = strings.TrimSpace(step.Output + "\n" + err.Error())
		}
		lockSteps = append(lockSteps, step)
		if opts.Progress != nil {
			opts.Progress(step)
		}
	}
	if len(plan.Actions) == 0 {
		if len(lockSteps) == 0 {
			return plan, nil
		}
		return finish(store, nb, opts, lockSteps)
	}
	ps := plan.Python
	steps := lockSteps
	restartKernel := false
	var restartLangs []*EnvState
	for _, a := range plan.Actions {
		t0 := time.Now()
		var out string
		var runErr error
		switch a.ID {
		case "recreate-venv":
			if err := os.RemoveAll(ps.Venv); err != nil {
				runErr = fmt.Errorf("remove %s: %w", ps.Venv, err)
				break
			}
			fallthrough
		case "create-venv":
			out, runErr = runCommand(a.Command, a.Dir, 5*time.Minute)
			if runErr == nil {
				if project.VenvPython(ps.Venv) == "" {
					runErr = fmt.Errorf("%s was not created", ps.Venv)
				} else {
					restartKernel = ps.KernelRunning // it was bound elsewhere
				}
			}
		case "install":
			reqFile, err := writeRequirements(ps.Missing)
			if err != nil {
				runErr = err
				break
			}
			defer os.Remove(reqFile)
			cmd := installCommand(ps.UV, ps.Python, &opts)
			out, runErr = runCommand(append(cmd, "-r", reqFile), a.Dir, 30*time.Minute)
			if runErr == nil {
				ver, _ := pythonVersion(ps.Python)
				if err := recordReceipt(ps.Venv, ps.Python, ver.String(), ps.Missing); err != nil {
					out += "\n(could not write receipt: " + err.Error() + ")"
				}
				if lockOut, err := writePythonLock(ps, plan.Project, &opts); err != nil {
					out += "\n(could not write " + rel(plan.Project, ps.Lock) + ": " + err.Error() + ")"
				} else {
					out += "\n" + lockOut
				}
				if anyEditable(ps.Missing) && ps.KernelRunning {
					restartKernel = true
				}
			}
		case "restart-kernel":
			restartKernel = true
		default:
			if key, ok := strings.CutPrefix(a.ID, "install:"); ok && plan.Envs[key] != nil {
				var restart bool
				out, restart, runErr = ensureEnv(plan.Envs[key], plan.Project, &opts)
				if restart {
					restartLangs = append(restartLangs, plan.Envs[key])
				}
			}
		}
		step := Step{Action: a, OK: runErr == nil, Output: strings.TrimSpace(out), Seconds: time.Since(t0).Seconds()}
		if runErr != nil {
			step.Output = strings.TrimSpace(step.Output + "\n" + runErr.Error())
		}
		steps = append(steps, step)
		if opts.Progress != nil && a.ID != "restart-kernel" {
			opts.Progress(step)
		}
		if runErr != nil {
			return finish(store, nb, opts, steps)
		}
	}
	if restartKernel && ps != nil && ps.KernelRunning {
		steps = append(steps, restartNotebookKernel(store, "py", ps.Kernel, nb, plan, opts))
	}
	for _, es := range restartLangs {
		steps = append(steps, restartNotebookKernel(store, es.Lang, es.Kernel, nb, plan, opts))
	}
	return finish(store, nb, opts, steps)
}

// restartNotebookKernel restarts the notebook's kernel for lang.
func restartNotebookKernel(store *state.Store, lang, kernelName string, nb *Notebook, plan *Report, opts Options) Step {
	t0 := time.Now()
	res, err := resolver.ResolveWith(store, lang, resolver.Options{Cwd: nb.Dir, ProjectRoot: plan.Project})
	step := Step{Action: Action{ID: "restart-kernel", Label: "restart " + kernelName, Effect: "variables reset"}}
	if err == nil {
		err = daemon.Stop(store, res.Name)
	}
	if err == nil {
		_, err = daemon.Start(store, daemon.StartOpts{Name: res.Name, Lang: res.Lang, Cwd: res.Cwd, Venv: res.Venv,
			RuntimePath: res.RuntimePath, Options: res.Options, Env: res.Env})
	}
	step.OK = err == nil
	if err != nil {
		step.Output = err.Error()
	}
	step.Seconds = time.Since(t0).Seconds()
	if opts.Progress != nil {
		opts.Progress(step)
	}
	return step
}

func finish(store *state.Store, nb *Notebook, opts Options, steps []Step) (*Report, error) {
	after := opts
	after.Force = false
	after.Recreate = false
	report, err := Doctor(store, nb, after)
	if err != nil {
		return nil, err
	}
	report.Steps = steps
	if report.Steps == nil {
		report.Steps = []Step{}
	}
	for _, s := range steps {
		if !s.OK {
			report.OK = false
		}
	}
	return report, nil
}

// playedIn reports whether dep's cells ran to completion in every kernel
// they need, and those kernels are still the same running processes.
func playedIn(store *state.Store, dep *Notebook, root string) bool {
	langs := dep.Languages()
	if len(langs) == 0 {
		return true
	}
	for _, l := range langs {
		res, err := resolver.ResolveWith(store, l, resolver.Options{Cwd: dep.Dir, ProjectRoot: root})
		if err != nil {
			return false
		}
		k, _ := store.GetRunning(res.Name)
		if k == nil || !containsString(k.Played, dep.Path) {
			return false
		}
	}
	return true
}

// ── helpers ──

func kernelEffect(ps *PythonState, what string) string {
	if ps.KernelRunning {
		return "kernel " + what + " — variables reset"
	}
	return ""
}

func hasAction(actions []Action, id string) bool {
	for _, a := range actions {
		if a.ID == id {
			return true
		}
	}
	return false
}

func orNone(s string) string {
	if s == "" {
		return "the system interpreter"
	}
	return s
}

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// expectedVenvPython is where the interpreter will be once venv exists.
func expectedVenvPython(venv string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts", "python.exe")
	}
	return filepath.Join(venv, "bin", "python")
}

func findUV(opts *Options) string {
	if p, err := opts.lookPath("uv"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range []string{
		filepath.Join(home, ".local", "bin", "uv"),
		filepath.Join(home, ".cargo", "bin", "uv"),
		filepath.Join(home, ".nix-profile", "bin", "uv"),
	} {
		if runtime.GOOS == "windows" {
			c += ".exe"
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func venvCreateCommand(uv, venv string, spec Specifier, opts *Options) []string {
	if uv != "" {
		cmd := []string{uv, "venv", venv}
		if !spec.IsZero() {
			cmd = append(cmd, "--python", spec.UVRequest())
		}
		return cmd
	}
	if !spec.IsZero() {
		return nil // only uv can fetch a specific interpreter
	}
	for _, c := range []string{"python3", "python"} {
		if p, err := opts.lookPath(c); err == nil {
			return []string{p, "-m", "venv", venv}
		}
	}
	return nil
}

func installCommand(uv, python string, opts *Options) []string {
	if uv != "" {
		return []string{uv, "pip", "install", "--python", python}
	}
	if python != "" {
		return []string{python, "-m", "pip", "install"}
	}
	return nil
}

func anyEditable(lines []string) bool {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "-e ") || strings.HasPrefix(t, "--editable ") {
			return true
		}
	}
	return false
}

func writeRequirements(lines []string) (string, error) {
	f, err := os.CreateTemp("", "rat-requirements-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			return "", err
		}
	}
	return f.Name(), nil
}

func runCommand(argv []string, dir string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		return buf.String(), fmt.Errorf("%s: %w", filepath.Base(argv[0]), err)
	}
	return buf.String(), nil
}

func pythonVersion(python string) (Version, error) {
	out, err := exec.Command(python, "-c", "import platform; print(platform.python_version())").Output()
	if err != nil {
		return Version{}, fmt.Errorf("cannot run interpreter: %w", err)
	}
	return ParseVersion(string(out))
}

// distribution is what the interpreter knows about one installed package,
// including where it came from (PEP 610 direct_url.json: a local path and
// whether it is editable, or a VCS URL and the requested revision).
type distribution struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	URL               string `json:"url"`
	Editable          bool   `json:"editable"`
	VCS               string `json:"vcs"`
	RequestedRevision string `json:"requested_revision"`
	CommitID          string `json:"commit_id"`
}

// installedDistributions lists every distribution the interpreter can see,
// keyed by normalized name. One subprocess, ~50 ms. This is the authority
// on what is installed and from where; a receipt rat wrote is not.
func installedDistributions(python string) (map[string]distribution, error) {
	const script = `import importlib.metadata as m, json
out = []
for d in m.distributions():
    n = d.metadata['Name']
    if not n: continue
    rec = {"name": n, "version": d.version or ""}
    try:
        raw = d.read_text("direct_url.json")
        if raw:
            du = json.loads(raw)
            rec["url"] = du.get("url", "")
            rec["editable"] = bool(du.get("dir_info", {}).get("editable"))
            vcs = du.get("vcs_info") or {}
            rec["vcs"] = vcs.get("vcs", "")
            rec["requested_revision"] = vcs.get("requested_revision", "")
            rec["commit_id"] = vcs.get("commit_id", "")
    except Exception:
        pass
    out.append(rec)
print(json.dumps(out))`
	out, err := exec.Command(python, "-c", script).Output()
	if err != nil {
		return nil, err
	}
	var list []distribution
	if err := json.Unmarshal(bytes.TrimSpace(out), &list); err != nil {
		return nil, fmt.Errorf("parse distributions: %w", err)
	}
	dists := map[string]distribution{}
	for _, d := range list {
		dists[normalizeName(d.Name)] = d
	}
	return dists, nil
}

// editableInstalls returns the venv's editable local packages as the
// requirement lines a notebook in projectDir should use.
func editableInstalls(installed map[string]distribution, projectDir string) []EditableInstall {
	var out []EditableInstall
	for _, d := range installed {
		if !d.Editable {
			continue
		}
		dir := fileURLPath(d.URL)
		if dir == "" {
			continue
		}
		line := "-e " + relRequirementPath(projectDir, dir)
		out = append(out, EditableInstall{Name: d.Name, Path: dir, Line: line})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// relRequirementPath writes dir relative to base in requirements syntax:
// ".", "./python", "../lib"; absolute when it is not under a sensible
// relative form.
func relRequirementPath(base, dir string) string {
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return dir
	}
	rel = filepath.ToSlash(rel)
	switch {
	case rel == ".":
		return "."
	case strings.HasPrefix(rel, "../"):
		return rel
	default:
		return "./" + rel
	}
}

// fileURLPath turns "file:///home/u/proj/python" into a clean path, or "".
func fileURLPath(u string) string {
	if !strings.HasPrefix(u, "file://") {
		return ""
	}
	p := strings.TrimPrefix(u, "file://")
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.Clean(p)
}

// requirementStatus decides whether one requirement line needs installing
// (ok=false), is satisfied (ok=true), or cannot be installed as written
// (problem != ""). The interpreter's own install records are the truth:
//
//   - "name": satisfied when installed.
//   - "name==1.2": satisfied when that version is installed.
//   - "-e path" / "path": the folder must hold a project file; satisfied
//     when a distribution from exactly that folder is installed (editable
//     when -e). No receipt needed: a venv the person set up by hand counts.
//   - "name @ git+url[@rev]": satisfied when installed from that URL (and
//     that revision, when one is written).
//   - other constrained lines ("x>=1"): installed AND recorded verbatim in
//     the receipt (only the receipt knows the constraint was honoured).
//   - unverifiable lines ("-r file", bare URLs): the receipt alone.
func requirementStatus(line, projectDir string, installed map[string]distribution, rec receipt) (ok bool, problem string) {
	t := strings.TrimSpace(line)
	_, recorded := rec.Satisfied[t]
	editable := false
	if strings.HasPrefix(t, "-e ") || strings.HasPrefix(t, "--editable ") {
		editable = true
		t = strings.TrimSpace(t[strings.Index(t, " ")+1:])
	} else if strings.HasPrefix(t, "-") {
		return recorded, ""
	}

	// Local path.
	if t == "." || strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") || filepath.IsAbs(t) {
		dir := t
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(projectDir, dir)
		}
		dir = filepath.Clean(dir)
		if !hasProjectFile(dir) {
			return false, fmt.Sprintf("%q points to %s, which has no pyproject.toml or setup.py", line, dir)
		}
		name := ProjectPackage(dir)
		if name == "" {
			return recorded, "" // setup.py-only project: name unknown without running it
		}
		d, found := installed[normalizeName(name)]
		if !found {
			return false, ""
		}
		if src := fileURLPath(d.URL); src != "" {
			if !samePath(src, dir) {
				return false, "" // installed from somewhere else
			}
			return !editable || d.Editable, ""
		}
		return false, "" // installed, but not from a local path
	}

	// name @ url
	if i := strings.Index(t, " @ "); i > 0 {
		name := strings.TrimSpace(t[:i])
		url := strings.TrimSpace(t[i+3:])
		if m := nameRe.FindStringSubmatch(name); m != nil {
			name = m[1]
		}
		d, found := installed[normalizeName(name)]
		if !found {
			return false, ""
		}
		if d.URL == "" {
			return recorded, ""
		}
		wantURL, wantRev := splitRevision(strings.TrimPrefix(url, "git+"))
		if strings.TrimSuffix(d.URL, ".git") != strings.TrimSuffix(wantURL, ".git") {
			return false, ""
		}
		if wantRev != "" && d.RequestedRevision != wantRev && d.CommitID != wantRev {
			return false, ""
		}
		return true, ""
	}

	if strings.Contains(t, "://") {
		return recorded, ""
	}

	m := nameRe.FindStringSubmatch(t)
	if m == nil {
		return recorded, ""
	}
	d, found := installed[normalizeName(m[1])]
	if !found {
		return false, ""
	}
	if isPlainName(t) {
		return true, ""
	}
	if v := exactVersion(t); v != "" {
		return normalizeVersion(d.Version) == normalizeVersion(v), ""
	}
	return recorded, ""
}

func hasProjectFile(dir string) bool {
	for _, f := range []string{"pyproject.toml", "setup.py", "setup.cfg"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// splitRevision splits "https://host/repo@branch" into URL and revision.
// The scheme's "://" is not an "@"; only a trailing @rev counts.
func splitRevision(url string) (string, string) {
	if i := strings.LastIndex(url, "@"); i > strings.Index(url, "://")+3 {
		return url[:i], url[i+1:]
	}
	return url, ""
}

var exactVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:\[[^\]]*\])?\s*==\s*([A-Za-z0-9.!+*-]+)\s*$`)

// exactVersion returns V for "name==V" (no wildcard), else "".
func exactVersion(line string) string {
	m := exactVersionRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil || strings.Contains(m[1], "*") {
		return ""
	}
	return m[1]
}

// normalizeVersion trims trailing ".0" segments so 1.2 == 1.2.0.
func normalizeVersion(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for strings.HasSuffix(v, ".0") && strings.Count(v, ".") > 0 {
		v = strings.TrimSuffix(v, ".0")
	}
	return v
}

var (
	nameRe      = regexp.MustCompile(`^([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
	normalizeRe = regexp.MustCompile(`[-_.]+`)
	plainNameRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
)

// normalizeName applies PEP 503 normalization.
func normalizeName(n string) string {
	return strings.ToLower(normalizeRe.ReplaceAllString(strings.TrimSpace(n), "-"))
}

func isPlainName(line string) bool {
	return plainNameRe.MatchString(strings.TrimSpace(line))
}

// distributionName derives the distribution a requirement line installs,
// when that is knowable without asking an index: "name==1", "name[x]>=2",
// "name @ url", "-e ." / "." (from the target's pyproject.toml). Returns
// "" for lines that cannot be verified (-r files, bare URLs).
func distributionName(line, projectDir string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "-") {
		if strings.HasPrefix(t, "-e ") || strings.HasPrefix(t, "--editable ") {
			t = strings.TrimSpace(t[strings.Index(t, " ")+1:])
		} else {
			return ""
		}
	}
	if strings.Contains(t, "://") && !strings.Contains(t, " @ ") {
		return ""
	}
	if t == "." || strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") || filepath.IsAbs(t) {
		p := t
		if !filepath.IsAbs(p) {
			p = filepath.Join(projectDir, p)
		}
		return ProjectPackage(p)
	}
	if m := nameRe.FindStringSubmatch(t); m != nil {
		return m[1]
	}
	return ""
}

// ── receipt ──

const receiptFile = "rat-notebook.json"

type receipt struct {
	Python    string            `json:"python"`
	Version   string            `json:"version"`
	Satisfied map[string]string `json:"satisfied"` // requirement line → RFC 3339 time
}

func readReceipt(venv, python, version string) receipt {
	r := receipt{Python: python, Version: version, Satisfied: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(venv, receiptFile))
	if err != nil {
		return r
	}
	var on receipt
	if json.Unmarshal(data, &on) != nil || on.Python != python || on.Version != version {
		return r // a different interpreter: nothing carries over
	}
	if on.Satisfied != nil {
		r.Satisfied = on.Satisfied
	}
	return r
}

func (r receipt) unsatisfied(lines []string) []string {
	var out []string
	for _, l := range lines {
		if _, ok := r.Satisfied[strings.TrimSpace(l)]; !ok {
			out = append(out, l)
		}
	}
	return out
}

func recordReceipt(venv, python, version string, lines []string) error {
	r := readReceipt(venv, python, version)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, l := range lines {
		r.Satisfied[strings.TrimSpace(l)] = now
	}
	keys := make([]string, 0, len(r.Satisfied))
	for k := range r.Satisfied {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, k := range keys {
		ordered[k] = r.Satisfied[k]
	}
	r.Satisfied = ordered
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(venv, receiptFile), append(data, '\n'), 0644)
}

// MarshalJSON writes each runtime's packages as a top-level field of its
// key ("r", "julia"), beside "python".
func (r *Report) MarshalJSON() ([]byte, error) {
	type plain Report
	raw, err := json.Marshal((*plain)(r))
	if err != nil {
		return nil, err
	}
	if len(r.Envs) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for key, es := range r.Envs {
		if _, taken := m[key]; taken {
			continue
		}
		b, err := json.Marshal(es)
		if err != nil {
			return nil, err
		}
		m[key] = b
	}
	return json.Marshal(m)
}

// readPythonLock returns the pinned lines of .rat/python.lock (nil when
// there is none).
func readPythonLock(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pins []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		pins = append(pins, line)
	}
	return pins
}

// writePythonLock records the environment's versions: `pip freeze`
// without the editable installs (the notebook declares those, with paths
// that mean something on another machine).
func writePythonLock(ps *PythonState, project string, opts *Options) (string, error) {
	var argv []string
	if ps.UV != "" {
		argv = []string{ps.UV, "pip", "freeze", "--python", ps.Python, "--exclude-editable"}
	} else {
		argv = []string{ps.Python, "-m", "pip", "freeze", "--exclude-editable"}
	}
	out, err := runCommand(argv, project, 2*time.Minute)
	if err != nil {
		return out, err
	}
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "-e ") && !strings.HasPrefix(line, "#") {
			kept = append(kept, line)
		}
	}
	if err := writeRatGitignore(project); err != nil {
		return "", err
	}
	body := "# Written by `rat ensure`: the versions this project's notebooks run with.\n# `rat ensure` reproduces them; `rat ensure --update` resolves again.\n" + strings.Join(kept, "\n") + "\n"
	if err := os.WriteFile(ps.Lock, []byte(body), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d packages)", rel(project, ps.Lock), len(kept)), nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
