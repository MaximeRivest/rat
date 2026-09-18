package notebook

import (
	"os"
	"os/exec"
	"path/filepath"
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
