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
	Kernel        string   `json:"kernel"`
	Venv          string   `json:"venv"`
	VenvExists    bool     `json:"venv_exists"`
	Python        string   `json:"python,omitempty"`
	Version       string   `json:"version,omitempty"`
	Requires      string   `json:"requires,omitempty"`
	RequiresOK    bool     `json:"requires_ok"`
	Requirements  []string `json:"requirements"` // effective requirement lines
	Missing       []string `json:"missing,omitempty"`
	UV            string   `json:"uv,omitempty"`
	KernelRunning bool     `json:"kernel_running"`
	KernelVenv    string   `json:"kernel_venv,omitempty"` // live binding when running
	PEP723        bool     `json:"pep723,omitempty"`
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
		Checks: []Check{}, Actions: []Action{}, Steps: []Step{}}
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

	// Tools for non-Python cells. Rat does not install these yet: report
	// them, but do not let a missing R or tmux stop the Python work.
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
		} else {
			missing = append(missing, b)
		}
	}
	c := Check{ID: "tool-" + lang, Label: lang + " cells", OK: len(missing) == 0}
	if c.OK {
		c.Detail = strings.Join(found, ", ")
	} else {
		c.Detail = "missing: " + strings.Join(missing, ", ")
		c.Hint = "install " + strings.Join(missing, " and ") + " (rat does not install runtimes for " + lang + " yet)"
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
	for _, rq := range RatRequirements {
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
				Hint:   "install uv (https://docs.astral.sh/uv/) or a python3 on PATH"})
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
		switch {
		case opts.Force, !ps.VenvExists, hasAction(r.Actions, "recreate-venv"):
			missing = append(missing, ps.Requirements...)
		default:
			receipt := readReceipt(ps.Venv, ps.Python, ps.Version)
			installed, err := installedDistributions(ps.Python)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", ps.Python, err)
			}
			for _, line := range ps.Requirements {
				if !requirementSatisfied(line, r.Project, installed, receipt) {
					missing = append(missing, line)
				}
			}
		}
		ps.Missing = missing
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
	if plan.Blocked || len(plan.Actions) == 0 {
		return plan, nil
	}
	ps := plan.Python
	var steps []Step
	restartKernel := false
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
				if anyEditable(ps.Missing) && ps.KernelRunning {
					restartKernel = true
				}
			}
		case "restart-kernel":
			restartKernel = true
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
	if restartKernel && ps.KernelRunning {
		t0 := time.Now()
		res, err := resolver.ResolveWith(store, "py", resolver.Options{Cwd: nb.Dir, ProjectRoot: plan.Project})
		step := Step{Action: Action{ID: "restart-kernel", Label: "restart " + ps.Kernel, Effect: "variables reset"}}
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
		steps = append(steps, step)
		if opts.Progress != nil {
			opts.Progress(step)
		}
	}
	return finish(store, nb, opts, steps)
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

// installedDistributions lists the normalized names of every distribution
// the interpreter can see. One subprocess, ~50 ms; the receipt alone would
// happily report a package the user has since uninstalled.
func installedDistributions(python string) (map[string]bool, error) {
	const script = `import importlib.metadata as m
for d in m.distributions():
    n = d.metadata['Name']
    if n: print(n)`
	out, err := exec.Command(python, "-c", script).Output()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names[normalizeName(l)] = true
		}
	}
	return names, nil
}

var (
	nameRe      = regexp.MustCompile(`^([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
	normalizeRe = regexp.MustCompile(`[-_.]+`)
)

// normalizeName applies PEP 503 normalization.
func normalizeName(n string) string {
	return strings.ToLower(normalizeRe.ReplaceAllString(strings.TrimSpace(n), "-"))
}

// requirementSatisfied decides whether one requirement line needs
// installing. Two sources of truth: the interpreter (what is installed)
// and the receipt (which exact lines rat installed before).
//
//   - A plain name ("websockets") is satisfied when installed: the
//     interpreter alone answers, so a venv rat never touched is not
//     reinstalled needlessly.
//   - A constrained line ("x==1.2", "x @ git+…", "-e .") must be installed
//     AND recorded verbatim in the receipt: only the receipt knows whether
//     the installed copy came from this exact line. Change the line and it
//     installs again.
//   - A line whose distribution cannot be derived ("-r file", bare URL) is
//     satisfied by the receipt alone.
func requirementSatisfied(line, projectDir string, installed map[string]bool, rec receipt) bool {
	name := distributionName(line, projectDir)
	_, recorded := rec.Satisfied[strings.TrimSpace(line)]
	if name == "" {
		return recorded
	}
	if !installed[normalizeName(name)] {
		return false
	}
	if isPlainName(line) {
		return true
	}
	return recorded
}

var plainNameRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

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
