package resolve

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/state"
)

func tempStore(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	return state.NewStore(filepath.Join(dir, "state.yaml"))
}

func TestExactMatchRunningKernel(t *testing.T) {
	s := tempStore(t)
	s.Put(state.Kernel{
		Name: "py@myproject", Lang: "py", Port: 8717,
		PID: os.Getpid(), Cwd: "/proj", Started: time.Now(),
	})

	r, err := Resolve(s, "py@myproject", "/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "py@myproject" {
		t.Fatalf("expected py@myproject, got %s", r.Name)
	}
	if r.IsNew {
		t.Fatal("should not be new")
	}
}

func TestExactMatchSavedRuntime(t *testing.T) {
	s := tempStore(t)
	s.PutRuntime(state.Runtime{
		Name:    "py-ml",
		Lang:    "py",
		Cwd:     "/ml",
		Venv:    "/ml/.venv",
		Options: map[string]string{"model": "claude-sonnet-4-5"},
		Env:     map[string]string{"TOKEN": "secret"},
	})

	r, err := Resolve(s, "py-ml", "/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "py-ml" {
		t.Fatalf("expected py-ml, got %s", r.Name)
	}
	if r.Venv != "/ml/.venv" {
		t.Fatalf("expected venv /ml/.venv, got %s", r.Venv)
	}
	if r.Options["model"] != "claude-sonnet-4-5" {
		t.Fatalf("expected options to round-trip, got %#v", r.Options)
	}
	if r.Env["TOKEN"] != "secret" {
		t.Fatalf("expected env to round-trip, got %#v", r.Env)
	}
}

func TestLanguageAliasNewKernel(t *testing.T) {
	s := tempStore(t)

	// Create a temp project dir with a .git marker
	projDir := t.TempDir()
	os.MkdirAll(filepath.Join(projDir, ".git"), 0755)

	r, err := Resolve(s, "py", projDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Lang != "py" {
		t.Fatalf("expected lang py, got %s", r.Lang)
	}
	if !r.IsNew {
		t.Fatal("expected IsNew=true for new kernel")
	}
	// Name should be py@<dirname>
	expectedName := "py@" + filepath.Base(projDir)
	if r.Name != expectedName {
		t.Fatalf("expected name %q, got %q", expectedName, r.Name)
	}
}

func TestLanguageAliasSlugsProjectName(t *testing.T) {
	s := tempStore(t)

	parent := t.TempDir()
	projDir := filepath.Join(parent, "my project!")
	os.MkdirAll(filepath.Join(projDir, ".git"), 0755)

	r, err := Resolve(s, "py", projDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "py@my-project" {
		t.Fatalf("expected safe project slug, got %q", r.Name)
	}
}

func TestLanguageAliasExistingKernel(t *testing.T) {
	s := tempStore(t)

	projDir := t.TempDir()
	os.MkdirAll(filepath.Join(projDir, ".git"), 0755)
	projName := filepath.Base(projDir)
	kernelName := "py@" + projName

	s.Put(state.Kernel{
		Name: kernelName, Lang: "py", Port: 8717,
		PID: os.Getpid(), Cwd: projDir, Started: time.Now(),
	})

	r, err := Resolve(s, "py", projDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != kernelName {
		t.Fatalf("expected %s, got %s", kernelName, r.Name)
	}
	if r.IsNew {
		t.Fatal("should not be new — kernel exists")
	}
}

func TestLanguageAliasFindsStoppedKernel(t *testing.T) {
	s := tempStore(t)

	projDir := t.TempDir()
	os.MkdirAll(filepath.Join(projDir, ".git"), 0755)
	projName := filepath.Base(projDir)
	kernelName := "py@" + projName

	// Stopped kernel — should still be found
	s.Put(state.Kernel{
		Name: kernelName, Lang: "py", Port: 0, PID: 0,
		Status: state.StatusStopped, Cwd: projDir,
		Started: time.Now(), Stopped: time.Now(),
	})

	r, err := Resolve(s, "py", projDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != kernelName {
		t.Fatalf("expected %s, got %s", kernelName, r.Name)
	}
	if r.IsNew {
		t.Fatal("should not be new — stopped kernel exists")
	}
}

func TestPrefixMatchSingle(t *testing.T) {
	s := tempStore(t)
	s.PutRuntime(state.Runtime{
		Name: "py-ml", Lang: "py", Cwd: "/ml",
	})

	r, err := Resolve(s, "py-", "/somewhere")
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "py-ml" {
		t.Fatalf("expected py-ml, got %s", r.Name)
	}
}

func TestPrefixMatchAmbiguous(t *testing.T) {
	s := tempStore(t)
	s.Put(state.Kernel{
		Name: "py@proj1", Lang: "py", Port: 8717,
		PID: os.Getpid(), Cwd: "/proj1", Started: time.Now(),
	})
	s.Put(state.Kernel{
		Name: "py@proj2", Lang: "py", Port: 8718,
		PID: os.Getpid(), Cwd: "/proj2", Started: time.Now(),
	})

	_, err := Resolve(s, "py@", "/somewhere")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !contains(err.Error(), "multiple runtimes match") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNoMatch(t *testing.T) {
	s := tempStore(t)

	_, err := Resolve(s, "xyz", "/somewhere")
	if err == nil {
		t.Fatal("expected error for unknown name")
	}
	if !contains(err.Error(), "no runtime matching") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLanguageAliasVariants(t *testing.T) {
	s := tempStore(t)
	projDir := t.TempDir()
	os.MkdirAll(filepath.Join(projDir, ".git"), 0755)

	for _, alias := range []string{"py", "python", "sh", "bash", "r", "jl", "ju", "julia", "js", "node", "javascript"} {
		r, err := Resolve(s, alias, projDir)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", alias, err)
		}
		if r.Lang == "" {
			t.Fatalf("Resolve(%q): empty lang", alias)
		}
		if !r.IsNew {
			t.Fatalf("Resolve(%q): expected IsNew", alias)
		}
	}
}

func TestCollisionTiebreaker(t *testing.T) {
	s := tempStore(t)

	// Create two dirs with the same basename
	parent1 := t.TempDir()
	parent2 := t.TempDir()
	dir1 := filepath.Join(parent1, "backend")
	dir2 := filepath.Join(parent2, "backend")
	os.MkdirAll(filepath.Join(dir1, ".git"), 0755)
	os.MkdirAll(filepath.Join(dir2, ".git"), 0755)

	// First project claims py@backend
	s.Put(state.Kernel{
		Name: "py@backend", Lang: "py", Port: 8717,
		PID: os.Getpid(), Cwd: dir1, Started: time.Now(),
	})

	// Second project should get a qualified name
	r, err := Resolve(s, "py", dir2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name == "py@backend" {
		t.Fatal("collision: second project got same name as first")
	}
	if r.Lang != "py" {
		t.Fatalf("expected lang py, got %s", r.Lang)
	}
	if !r.IsNew {
		t.Fatal("expected IsNew for second project")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func makeVenv(t *testing.T, dir string) string {
	t.Helper()
	venv := filepath.Join(dir, ".venv")
	binDir := filepath.Join(venv, "bin")
	if runtime.GOOS == "windows" {
		binDir = filepath.Join(venv, "Scripts")
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(binDir, "python")
	if runtime.GOOS == "windows" {
		py = filepath.Join(binDir, "python.exe")
	}
	if err := os.WriteFile(py, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}
	return venv
}

func TestStoppedKernelPicksUpNewVenv(t *testing.T) {
	// A kernel started before the project had a venv is stopped; the
	// project now has one. Resolving must bind to the new venv, not the
	// stale (empty) binding in state.
	s := tempStore(t)
	proj := t.TempDir()
	os.MkdirAll(filepath.Join(proj, ".git"), 0755)
	name := "py@" + filepath.Base(proj)
	s.Put(state.Kernel{Name: name, Lang: "py", Port: 8717, PID: 999999, Cwd: proj, Started: time.Now()})
	s.MarkStopped(name)
	venv := makeVenv(t, proj)

	r, err := Resolve(s, "py", proj)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != name || r.Venv != venv {
		t.Fatalf("expected %s bound to %s, got %s bound to %q", name, venv, r.Name, r.Venv)
	}
	r, err = Resolve(s, name, "/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if r.Venv != venv {
		t.Fatalf("exact match: expected %s, got %q", venv, r.Venv)
	}
}

func TestRunningKernelKeepsItsBinding(t *testing.T) {
	// A running kernel is bound to what it started with even if the
	// project gained a venv since — restarting is a separate, visible act.
	s := tempStore(t)
	proj := t.TempDir()
	os.MkdirAll(filepath.Join(proj, ".git"), 0755)
	name := "py@" + filepath.Base(proj)
	s.Put(state.Kernel{Name: name, Lang: "py", Port: 8717, PID: os.Getpid(), Cwd: proj, Started: time.Now()})
	makeVenv(t, proj)

	r, err := Resolve(s, "py", proj)
	if err != nil {
		t.Fatal(err)
	}
	if r.Venv != "" {
		t.Fatalf("running kernel must keep its live binding, got %q", r.Venv)
	}
}

func TestRegisteredRuntimeKeepsExplicitVenv(t *testing.T) {
	s := tempStore(t)
	proj := t.TempDir()
	explicit := filepath.Join(proj, "custom-env")
	s.PutRuntime(state.Runtime{Name: "py-ml", Lang: "py", Cwd: proj, Venv: explicit})
	s.Put(state.Kernel{Name: "py-ml", Lang: "py", Port: 8717, PID: 999999, Cwd: proj, Venv: explicit, Started: time.Now()})
	s.MarkStopped("py-ml")
	makeVenv(t, proj)

	r, err := Resolve(s, "py-ml", proj)
	if err != nil {
		t.Fatal(err)
	}
	if r.Venv != explicit {
		t.Fatalf("explicit registration must keep %s, got %q", explicit, r.Venv)
	}
}

func TestExplicitProjectRoot(t *testing.T) {
	// A notebook deep inside repo/docs pins its project to the repo:
	// the kernel is py@repo and the venv is the repo's, even though
	// docs/ carries a requirements.txt.
	s := tempStore(t)
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	venv := makeVenv(t, repo)
	docs := filepath.Join(repo, "docs")
	deep := filepath.Join(docs, "tutorials")
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(docs, "requirements.txt"), []byte(""), 0644)

	r, err := ResolveWith(s, "py", Options{Cwd: deep, ProjectRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	want := "py@" + filepath.Base(repo)
	if r.Name != want || r.Cwd != repo || r.Venv != venv {
		t.Fatalf("got name=%s cwd=%s venv=%q, want %s %s %s", r.Name, r.Cwd, r.Venv, want, repo, venv)
	}
}
