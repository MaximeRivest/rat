package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/state"
)

func tempStore(t *testing.T) *state.Store {
	t.Helper()
	return state.NewStore(filepath.Join(t.TempDir(), "state.yaml"))
}

func TestStopStoppedKernelReturnsNotRunning(t *testing.T) {
	s := tempStore(t)
	if err := s.Put(state.Kernel{
		Name:    "py@proj",
		Lang:    "py",
		Status:  state.StatusStopped,
		PID:     0,
		Port:    0,
		Cwd:     "/tmp/proj",
		Started: time.Now(),
		Stopped: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := Stop(s, "py@proj")
	if err == nil {
		t.Fatal("expected error for stopped kernel")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStopAllIgnoresStoppedKernels(t *testing.T) {
	s := tempStore(t)
	if err := s.Put(state.Kernel{
		Name:    "py@proj",
		Lang:    "py",
		Status:  state.StatusStopped,
		PID:     0,
		Port:    0,
		Cwd:     "/tmp/proj",
		Started: time.Now(),
		Stopped: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	n, err := StopAll(s)
	if err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 stopped kernels to be signaled, got %d", n)
	}
}
