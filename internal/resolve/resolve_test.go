package resolve

import (
	"os"
	"path/filepath"
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
		Name: "py-ml", Lang: "py", Cwd: "/ml", Venv: "/ml/.venv",
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
