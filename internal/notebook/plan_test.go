package notebook

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/maximerivest/rat/internal/state"
)

func tempStore(t *testing.T) *state.Store {
	t.Helper()
	return state.NewStore(filepath.Join(t.TempDir(), "state.yaml"))
}

func writeNotebook(t *testing.T, dir, rel, text string) *Notebook {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0755)
	if err := os.WriteFile(p, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	nb, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return nb
}

func offlineRatRequirements(t *testing.T) {
	t.Helper()
	saved := RatRequirements
	RatRequirements = nil
	t.Cleanup(func() { RatRequirements = saved })
}

func requireVenvTooling(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("uv"); err != nil {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("neither uv nor python3 on PATH")
		}
	}
}

func TestDoctorDetectsProjectThroughDocsFolder(t *testing.T) {
	offlineRatRequirements(t)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	os.WriteFile(filepath.Join(repo, "pyproject.toml"), []byte("[project]\nname = \"thing\"\n"), 0644)
	os.WriteFile(filepath.Join(repo, "docs", "requirements.txt"), []byte("mkdocs"), 0644)
	nb := writeNotebook(t, repo, "docs/tutorials/x/index.md", "# x\n\n```python\nimport thing\n```\n")

	r, err := Doctor(tempStore(t), nb, Options{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	if err != nil {
		t.Fatal(err)
	}
	if r.Project != repo || r.ProjectSource != "detected" || r.ProjectPackage != "thing" {
		t.Fatalf("project: %s (%s) pkg=%s", r.Project, r.ProjectSource, r.ProjectPackage)
	}
	if r.Python == nil || r.Python.Kernel != "py@"+filepath.Base(repo) {
		t.Fatalf("kernel: %+v", r.Python)
	}
	if r.OK {
		t.Fatal("no venv: must not be ok")
	}
}

func TestDoctorPinnedProjectMissing(t *testing.T) {
	dir := t.TempDir()
	nb := writeNotebook(t, dir, "a.md", "---\nrat:\n  project: ./nope\n---\n```python\n1\n```\n")
	r, err := Doctor(tempStore(t), nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Blocked || r.Checks[0].OK || r.Checks[0].Hint == "" {
		t.Fatalf("expected a blocking project check with a hint: %+v", r.Checks)
	}
}

func TestDoctorMissingToolForShellCells(t *testing.T) {
	dir := t.TempDir()
	nb := writeNotebook(t, dir, "a.md", "```bash\nls\n```\n")
	r, err := Doctor(tempStore(t), nb, Options{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || r.Blocked {
		t.Fatalf("missing tmux/bash must fail the check without blocking python work: ok=%v blocked=%v %+v", r.OK, r.Blocked, r.Checks)
	}
	if r.Python != nil {
		t.Fatal("a shell-only notebook needs no python plan")
	}
}

func TestEnsureCreatesVenvThenIsIdempotent(t *testing.T) {
	offlineRatRequirements(t)
	requireVenvTooling(t)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	nb := writeNotebook(t, repo, "nb/run.md", "---\nrat:\n  python:\n    requires: \">=3.8\"\n---\n```python\nprint(1)\n```\n")
	store := tempStore(t)

	plan, err := Doctor(store, nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].ID != "create-venv" {
		t.Fatalf("plan: %+v", plan.Actions)
	}

	var seen []string
	r, err := Ensure(store, nb, Options{Progress: func(s Step) { seen = append(seen, s.Action.ID) }})
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK {
		t.Fatalf("ensure not ok: checks=%+v steps=%+v", r.Checks, r.Steps)
	}
	if len(seen) != 1 || seen[0] != "create-venv" {
		t.Fatalf("steps: %v", seen)
	}
	if r.Python.Venv != filepath.Join(repo, ".venv") || !r.Python.VenvExists || r.Python.Version == "" {
		t.Fatalf("python state: %+v", r.Python)
	}

	again, err := Ensure(store, nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !again.OK || len(again.Steps) != 0 || len(again.Actions) != 0 {
		t.Fatalf("second ensure must be a no-op: actions=%+v steps=%+v", again.Actions, again.Steps)
	}
}

func TestDoctorVersionMismatchNeedsRecreate(t *testing.T) {
	offlineRatRequirements(t)
	requireVenvTooling(t)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	store := tempStore(t)
	// Build a venv first with no constraint...
	first := writeNotebook(t, repo, "a.md", "```python\n1\n```\n")
	if r, err := Ensure(store, first, Options{}); err != nil || !r.OK {
		t.Fatalf("setup: %v %+v", err, r)
	}
	// ...then require a version it cannot satisfy.
	nb := writeNotebook(t, repo, "b.md", "---\nrat:\n  python:\n    requires: \"<2.0\"\n---\n```python\n1\n```\n")
	r, err := Doctor(store, nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Blocked || len(r.Actions) != 0 {
		t.Fatalf("mismatch without --recreate must block, got actions=%+v", r.Actions)
	}
	r, err = Doctor(store, nb, Options{Recreate: true, LookPath: func(n string) (string, error) {
		if n == "uv" {
			return "/fake/uv", nil
		}
		return exec.LookPath(n)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Blocked || len(r.Actions) == 0 || r.Actions[0].ID != "recreate-venv" || r.Actions[0].Effect == "" {
		t.Fatalf("with --recreate expected a recreate action stating its effect, got %+v (blocked=%v)", r.Actions, r.Blocked)
	}
}

func TestDoctorReportsMissingRequirement(t *testing.T) {
	offlineRatRequirements(t)
	requireVenvTooling(t)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	store := tempStore(t)
	base := writeNotebook(t, repo, "a.md", "```python\n1\n```\n")
	if r, err := Ensure(store, base, Options{}); err != nil || !r.OK {
		t.Fatalf("setup: %v", err)
	}
	nb := writeNotebook(t, repo, "b.md", "---\nrat:\n  python:\n    dependencies: [rat-test-package-that-does-not-exist]\n---\n```python\n1\n```\n")
	r, err := Doctor(store, nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || len(r.Actions) != 1 || r.Actions[0].ID != "install" || len(r.Python.Missing) != 1 {
		t.Fatalf("expected one install action: %+v missing=%v", r.Actions, r.Python.Missing)
	}
}

func TestChainOrderAndCycle(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	writeNotebook(t, repo, "nb/00-base.md", "---\nrat:\n  python:\n    dependencies: [rich]\n---\n```python\nbase = 1\n```\n")
	writeNotebook(t, repo, "nb/01-load.md", "---\nrat:\n  after: [./00-base.md]\n  python:\n    dependencies: [httpx]\n---\n```python\nloaded = base + 1\n```\n")
	nb := writeNotebook(t, repo, "nb/02-analyse.md", "---\nrat:\n  after:\n    - ./01-load.md\n    - ./00-base.md\n---\n```python\nprint(loaded)\n```\n")
	chain, err := nb.Chain()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range chain {
		names = append(names, filepath.Base(c.Path))
	}
	if len(names) != 2 || names[0] != "00-base.md" || names[1] != "01-load.md" {
		t.Fatalf("chain order: %v", names)
	}
	offlineRatRequirements(t)
	r, err := Doctor(tempStore(t), nb, Options{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.After) != 2 || r.After[0].Played {
		t.Fatalf("after state: %+v", r.After)
	}
	// The chain's requirements are merged in, dependencies first.
	if got := r.Python.Requirements; len(got) != 2 || got[0] != "rich" || got[1] != "httpx" {
		t.Fatalf("merged requirements: %v", got)
	}

	// A cycle is refused with a clear message.
	writeNotebook(t, repo, "nb/00-base.md", "---\nrat:\n  after: [./02-analyse.md]\n---\n```python\n1\n```\n")
	if _, err := nb.Chain(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
	rc, err := Doctor(tempStore(t), nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !rc.Blocked || rc.Checks[len(rc.Checks)-1].ID != "after" {
		t.Fatalf("cycle must block with an `after` check: %+v", rc.Checks)
	}
}

func TestChainAcrossProjectsIsRefused(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	os.MkdirAll(filepath.Join(a, ".git"), 0755)
	os.MkdirAll(filepath.Join(b, ".git"), 0755)
	writeNotebook(t, a, "setup.md", "```python\nx = 1\n```\n")
	nb := writeNotebook(t, b, "use.md", "---\nrat:\n  after: [../a/setup.md]\n---\n```python\nprint(x)\n```\n")
	r, err := Doctor(tempStore(t), nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Blocked || !strings.Contains(r.Checks[len(r.Checks)-1].Detail, "belongs to project") {
		t.Fatalf("cross-project chain must block: %+v", r.Checks)
	}
}

// buildRat compiles the CLI once per test binary: Play starts kernels
// through os.Executable(), which inside `go test` is the test binary.
var ratBinary struct {
	once sync.Once
	path string
	err  error
}

func buildRat(t *testing.T) string {
	t.Helper()
	ratBinary.once.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			ratBinary.err = err
			return
		}
		dir, err := os.MkdirTemp("", "rat-bin-")
		if err != nil {
			ratBinary.err = err
			return
		}
		out := filepath.Join(dir, "rat")
		cmd := exec.Command(goBin, "build", "-o", out, "./cmd/rat")
		cmd.Dir = moduleRoot(t)
		if b, err := cmd.CombinedOutput(); err != nil {
			ratBinary.err = fmt.Errorf("go build: %v\n%s", err, b)
			return
		}
		ratBinary.path = out
	})
	if ratBinary.err != nil {
		t.Skip("cannot build rat: " + ratBinary.err.Error())
	}
	return ratBinary.path
}

func moduleRoot(t *testing.T) string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// isolatedEnv points rat's state, cache and home into a temp dir so the
// test never touches the developer's kernels.
func isolatedEnv(t *testing.T) []string {
	home := t.TempDir()
	return append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
	)
}

func TestPlayRunsChainOncePerKernel(t *testing.T) {
	offlineRatRequirements(t)
	requireVenvTooling(t)
	rat := buildRat(t)
	env := isolatedEnv(t)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	writeNotebook(t, repo, "nb/00-base.md", "```python\nbase = 40\n```\n")
	nb := writeNotebook(t, repo, "nb/01-use.md", "---\nrat:\n  after: [./00-base.md]\n---\n```python\nprint(base + 2)\n```\n")
	bad := writeNotebook(t, repo, "nb/02-bad.md", "---\nrat:\n  after: [./00-base.md]\n---\n```python\nraise ValueError('boom')\n```\n\n```python\nprint('never')\n```\n")
	t.Cleanup(func() {
		stop := exec.Command(rat, "stop", "--all")
		stop.Env = env
		_ = stop.Run()
	})
	// ipython/jedi are rat's own requirements; this test stays offline.
	play := func(path string) *PlayReport {
		t.Helper()
		cmd := exec.Command(rat, "play", path, "--json", "--timeout", "2m")
		cmd.Env = append(env, "RAT_NOTEBOOK_REQUIREMENTS=")
		out, _ := cmd.Output()
		var r PlayReport
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("rat play %s: %v\n%s", path, err, out)
		}
		return &r
	}
	report := play(nb.Path)
	if !report.OK {
		t.Fatalf("play failed: %+v ensure=%+v", report.Runs, report.Ensure.Checks)
	}
	if len(report.Runs) != 2 || report.Runs[0].Skipped || report.Runs[0].Role != "prerequisite" {
		t.Fatalf("first play must run the prerequisite: %+v", report.Runs)
	}
	if out := report.Runs[1].Cells[0].Output; !strings.Contains(out, "42") {
		t.Fatalf("state did not carry over: %q", out)
	}
	again := play(nb.Path)
	if !again.OK || !again.Runs[0].Skipped {
		t.Fatalf("second play must skip the prerequisite: %+v", again.Runs)
	}
	if !again.Ensure.After[0].Played {
		t.Fatalf("doctor must see the prerequisite as played: %+v", again.Ensure.After)
	}
	br := play(bad.Path)
	if br.OK || len(br.Runs[1].Cells) != 1 || br.Runs[1].Cells[0].OK || !strings.Contains(br.Runs[1].Cells[0].Output, "boom") {
		t.Fatalf("failure reporting: %+v", br.Runs[1])
	}
}
