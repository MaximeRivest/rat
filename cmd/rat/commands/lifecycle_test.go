package commands

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/state"
)

func tempCommandStore(t *testing.T) *state.Store {
	t.Helper()
	return state.NewStore(filepath.Join(t.TempDir(), "state.yaml"))
}

func TestBuildStatusRowsIncludesSavedRuntimes(t *testing.T) {
	s := tempCommandStore(t)
	if err := s.PutRuntime(state.Runtime{
		Name: "py-ml",
		Lang: "py",
		Cwd:  "/ml",
		Venv: "/ml/.venv",
	}); err != nil {
		t.Fatalf("PutRuntime: %v", err)
	}

	rows, err := buildStatusRows(s)
	if err != nil {
		t.Fatalf("buildStatusRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Name != "py-ml" || rows[0].Status != state.StatusStopped {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestBuildStatusRowsKernelOverridesSavedRuntime(t *testing.T) {
	s := tempCommandStore(t)
	if err := s.PutRuntime(state.Runtime{
		Name: "py-ml",
		Lang: "py",
		Cwd:  "/saved",
		Venv: "/saved/.venv",
	}); err != nil {
		t.Fatalf("PutRuntime: %v", err)
	}
	if err := s.Put(state.Kernel{
		Name:    "py-ml",
		Lang:    "py",
		Status:  state.StatusRunning,
		PID:     os.Getpid(),
		Port:    8717,
		Cwd:     "/live",
		Venv:    "/live/.venv",
		Started: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rows, err := buildStatusRows(s)
	if err != nil {
		t.Fatalf("buildStatusRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Cwd != "/live" || rows[0].Venv != "/live/.venv" || rows[0].Status != state.StatusRunning {
		t.Fatalf("kernel row did not win: %+v", rows[0])
	}
}

func TestBuildStatusRowsSortedByName(t *testing.T) {
	s := tempCommandStore(t)
	_ = s.PutRuntime(state.Runtime{Name: "z-last", Lang: "py", Cwd: "/z"})
	_ = s.PutRuntime(state.Runtime{Name: "a-first", Lang: "py", Cwd: "/a"})

	rows, err := buildStatusRows(s)
	if err != nil {
		t.Fatalf("buildStatusRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Name != "a-first" || rows[1].Name != "z-last" {
		t.Fatalf("rows not sorted: %+v", rows)
	}
}
