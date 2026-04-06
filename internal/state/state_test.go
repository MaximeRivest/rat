package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "state.yaml"))
}

func TestEmptyState(t *testing.T) {
	s := tempStore(t)

	kernels, err := s.ListKnown()
	if err != nil {
		t.Fatalf("ListKnown: %v", err)
	}
	if len(kernels) != 0 {
		t.Fatalf("expected 0 kernels, got %d", len(kernels))
	}
}

func TestPutAndGetKnown(t *testing.T) {
	s := tempStore(t)

	k := Kernel{
		Name:    "sh",
		Lang:    "sh",
		Port:    8720,
		PID:     os.Getpid(),
		Cwd:     "/tmp",
		Started: time.Now(),
	}

	if err := s.Put(k); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.GetKnown("sh")
	if err != nil {
		t.Fatalf("GetKnown: %v", err)
	}
	if got == nil {
		t.Fatal("GetKnown returned nil")
	}
	if got.Name != "sh" || got.Port != 8720 || got.PID != os.Getpid() {
		t.Fatalf("unexpected kernel: %+v", got)
	}
	if got.Status != StatusRunning {
		t.Fatalf("expected status %q, got %q", StatusRunning, got.Status)
	}
}

func TestGetKnownNotFound(t *testing.T) {
	s := tempStore(t)

	got, err := s.GetKnown("nonexistent")
	if err != nil {
		t.Fatalf("GetKnown: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestPutReplacesExisting(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	k1 := Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()}
	k2 := Kernel{Name: "sh", Lang: "sh", Port: 8721, PID: pid, Cwd: "/home", Started: time.Now()}

	s.Put(k1)
	s.Put(k2)

	kernels, _ := s.ListKnown()
	if len(kernels) != 1 {
		t.Fatalf("expected 1 kernel after replace, got %d", len(kernels))
	}
	if kernels[0].Port != 8721 {
		t.Fatalf("expected port 8721, got %d", kernels[0].Port)
	}
	if kernels[0].Cwd != "/home" {
		t.Fatalf("expected cwd /home, got %s", kernels[0].Cwd)
	}
}

func TestRemove(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})
	s.Put(Kernel{Name: "py", Lang: "py", Port: 8717, PID: pid, Cwd: "/tmp", Started: time.Now()})

	found, err := s.Remove("sh")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !found {
		t.Fatal("Remove returned false")
	}

	kernels, _ := s.ListKnown()
	if len(kernels) != 1 {
		t.Fatalf("expected 1 kernel after remove, got %d", len(kernels))
	}
	if kernels[0].Name != "py" {
		t.Fatalf("wrong kernel remaining: %s", kernels[0].Name)
	}
}

func TestRemoveNotFound(t *testing.T) {
	s := tempStore(t)

	found, err := s.Remove("nonexistent")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if found {
		t.Fatal("Remove returned true for nonexistent")
	}
}

func TestDeadPIDMarkedStopped(t *testing.T) {
	s := tempStore(t)

	k := Kernel{
		Name:    "dead",
		Lang:    "sh",
		Port:    9999,
		PID:     999999999,
		Status:  StatusRunning,
		Cwd:     "/tmp",
		Started: time.Now(),
	}
	s.Put(k)

	kernels, err := s.ListKnown()
	if err != nil {
		t.Fatalf("ListKnown: %v", err)
	}
	if len(kernels) != 1 {
		t.Fatalf("expected 1 kernel (stopped), got %d", len(kernels))
	}
	if kernels[0].Status != StatusStopped {
		t.Fatalf("expected status %q, got %q", StatusStopped, kernels[0].Status)
	}
	if kernels[0].PID != 0 {
		t.Fatalf("expected PID 0 for stopped kernel, got %d", kernels[0].PID)
	}
	if kernels[0].Port != 0 {
		t.Fatalf("expected Port 0 for stopped kernel, got %d", kernels[0].Port)
	}
}

func TestListRunning(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})
	s.Put(Kernel{Name: "old", Lang: "py", Port: 0, PID: 0, Status: StatusStopped, Cwd: "/old", Started: time.Now()})

	running, err := s.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(running) != 1 {
		t.Fatalf("expected 1 running kernel, got %d", len(running))
	}
	if running[0].Name != "sh" {
		t.Fatalf("expected sh, got %s", running[0].Name)
	}

	all, err := s.ListKnown()
	if err != nil {
		t.Fatalf("ListKnown: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 total kernels, got %d", len(all))
	}
}

func TestGetRunning(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})
	s.Put(Kernel{Name: "old", Lang: "py", Port: 0, PID: 0, Status: StatusStopped, Cwd: "/old", Started: time.Now()})

	got, err := s.GetRunning("sh")
	if err != nil {
		t.Fatalf("GetRunning: %v", err)
	}
	if got == nil || got.Name != "sh" {
		t.Fatalf("expected running kernel sh, got %+v", got)
	}

	stopped, err := s.GetRunning("old")
	if err != nil {
		t.Fatalf("GetRunning(old): %v", err)
	}
	if stopped != nil {
		t.Fatalf("expected nil for stopped kernel, got %+v", stopped)
	}
}

func TestMarkStopped(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})

	found, err := s.MarkStopped("sh")
	if err != nil {
		t.Fatalf("MarkStopped: %v", err)
	}
	if !found {
		t.Fatal("MarkStopped returned false")
	}

	got, _ := s.GetKnown("sh")
	if got == nil {
		t.Fatal("kernel disappeared after MarkStopped")
	}
	if got.Status != StatusStopped {
		t.Fatalf("expected status %q, got %q", StatusStopped, got.Status)
	}
	if got.PID != 0 {
		t.Fatalf("expected PID 0, got %d", got.PID)
	}
	if got.Port != 0 {
		t.Fatalf("expected Port 0, got %d", got.Port)
	}
	if got.Stopped.IsZero() {
		t.Fatal("expected Stopped time to be set")
	}
}

func TestMarkStoppedNotFound(t *testing.T) {
	s := tempStore(t)

	found, err := s.MarkStopped("nonexistent")
	if err != nil {
		t.Fatalf("MarkStopped: %v", err)
	}
	if found {
		t.Fatal("MarkStopped returned true for nonexistent")
	}
}

func TestMultipleKernels(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})
	s.Put(Kernel{Name: "py", Lang: "py", Port: 8717, PID: pid, Cwd: "/home", Started: time.Now()})
	s.Put(Kernel{Name: "py-ml", Lang: "py", Port: 8718, PID: pid, Cwd: "/ml", Venv: "/ml/.venv", Started: time.Now()})

	kernels, _ := s.ListKnown()
	if len(kernels) != 3 {
		t.Fatalf("expected 3 kernels, got %d", len(kernels))
	}

	ml, _ := s.GetKnown("py-ml")
	if ml == nil {
		t.Fatal("py-ml not found")
	}
	if ml.Venv != "/ml/.venv" {
		t.Fatalf("expected venv /ml/.venv, got %s", ml.Venv)
	}
}

func TestNextPort(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "py", Lang: "py", Port: 8717, PID: pid, Cwd: "/tmp", Started: time.Now()})

	port, err := s.NextPort(8717)
	if err != nil {
		t.Fatalf("NextPort: %v", err)
	}
	if port == 8717 {
		t.Fatal("NextPort returned already-used port 8717")
	}
	if port < 8717 || port > 8817 {
		t.Fatalf("NextPort returned out-of-range port: %d", port)
	}
}

func TestCorruptedStateFile(t *testing.T) {
	s := tempStore(t)

	os.MkdirAll(filepath.Dir(s.Path()), 0755)
	os.WriteFile(s.Path(), []byte("{{{{not yaml at all!!!!"), 0644)

	kernels, err := s.ListKnown()
	if err != nil {
		t.Fatalf("ListKnown on corrupted file: %v", err)
	}
	if len(kernels) != 0 {
		t.Fatalf("expected 0 kernels from corrupted file, got %d", len(kernels))
	}
}

func TestStateFilePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")
	pid := os.Getpid()

	s1 := NewStore(path)
	s1.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})

	s2 := NewStore(path)
	got, err := s2.GetKnown("sh")
	if err != nil {
		t.Fatalf("GetKnown from fresh store: %v", err)
	}
	if got == nil {
		t.Fatal("kernel not persisted across store instances")
	}
	if got.Port != 8720 {
		t.Fatalf("expected port 8720, got %d", got.Port)
	}
}

func TestStateFileWritten0600(t *testing.T) {
	s := tempStore(t)

	if err := s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: os.Getpid(), Cwd: "/tmp", Started: time.Now()}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state file mode = %#o, want 0o600", got)
	}
}

func TestPutDefaultsToRunning(t *testing.T) {
	s := tempStore(t)

	k := Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: os.Getpid(), Cwd: "/tmp", Started: time.Now()}
	s.Put(k)

	got, _ := s.GetKnown("sh")
	if got.Status != StatusRunning {
		t.Fatalf("expected default status %q, got %q", StatusRunning, got.Status)
	}
}

func TestStoppedKernelSurvivesListKnown(t *testing.T) {
	s := tempStore(t)

	s.Put(Kernel{
		Name:    "old",
		Lang:    "py",
		Status:  StatusStopped,
		PID:     0,
		Port:    0,
		Cwd:     "/old",
		Started: time.Now(),
		Stopped: time.Now(),
	})

	kernels, err := s.ListKnown()
	if err != nil {
		t.Fatalf("ListKnown: %v", err)
	}
	if len(kernels) != 1 {
		t.Fatalf("expected 1 stopped kernel, got %d", len(kernels))
	}
	if kernels[0].Status != StatusStopped {
		t.Fatalf("expected stopped, got %s", kernels[0].Status)
	}
}
