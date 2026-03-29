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

	kernels, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(kernels) != 0 {
		t.Fatalf("expected 0 kernels, got %d", len(kernels))
	}
}

func TestPutAndGet(t *testing.T) {
	s := tempStore(t)

	k := Kernel{
		Name:    "sh",
		Lang:    "sh",
		Port:    8720,
		PID:     os.Getpid(), // use our own PID so it passes alive check
		Cwd:     "/tmp",
		Started: time.Now(),
	}

	if err := s.Put(k); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get("sh")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Name != "sh" || got.Port != 8720 || got.PID != os.Getpid() {
		t.Fatalf("unexpected kernel: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	s := tempStore(t)

	got, err := s.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
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

	kernels, _ := s.List()
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

	kernels, _ := s.List()
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

func TestDeadPIDCleanup(t *testing.T) {
	s := tempStore(t)

	// Write a kernel with a PID that definitely doesn't exist
	k := Kernel{
		Name:    "dead",
		Lang:    "sh",
		Port:    9999,
		PID:     999999999, // hopefully doesn't exist
		Cwd:     "/tmp",
		Started: time.Now(),
	}
	s.Put(k)

	// Verify it was written
	data, _ := os.ReadFile(s.Path())
	if len(data) == 0 {
		t.Fatal("state file is empty after Put")
	}

	// List should clean it up
	kernels, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(kernels) != 0 {
		t.Fatalf("expected dead kernel to be cleaned up, got %d", len(kernels))
	}
}

func TestMultipleKernels(t *testing.T) {
	s := tempStore(t)
	pid := os.Getpid()

	s.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})
	s.Put(Kernel{Name: "py", Lang: "py", Port: 8717, PID: pid, Cwd: "/home", Started: time.Now()})
	s.Put(Kernel{Name: "py-ml", Lang: "py", Port: 8718, PID: pid, Cwd: "/ml", Venv: "/ml/.venv", Started: time.Now()})

	kernels, _ := s.List()
	if len(kernels) != 3 {
		t.Fatalf("expected 3 kernels, got %d", len(kernels))
	}

	// Check Get for named runtime
	ml, _ := s.Get("py-ml")
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

	// Claim port 8717 in state
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

	// Write garbage
	os.MkdirAll(filepath.Dir(s.Path()), 0755)
	os.WriteFile(s.Path(), []byte("{{{{not yaml at all!!!!"), 0644)

	// Should recover gracefully
	kernels, err := s.List()
	if err != nil {
		t.Fatalf("List on corrupted file: %v", err)
	}
	if len(kernels) != 0 {
		t.Fatalf("expected 0 kernels from corrupted file, got %d", len(kernels))
	}
}

func TestStateFilePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")
	pid := os.Getpid()

	// Write with one store instance
	s1 := NewStore(path)
	s1.Put(Kernel{Name: "sh", Lang: "sh", Port: 8720, PID: pid, Cwd: "/tmp", Started: time.Now()})

	// Read with a fresh store instance (simulates separate process)
	s2 := NewStore(path)
	got, err := s2.Get("sh")
	if err != nil {
		t.Fatalf("Get from fresh store: %v", err)
	}
	if got == nil {
		t.Fatal("kernel not persisted across store instances")
	}
	if got.Port != 8720 {
		t.Fatalf("expected port 8720, got %d", got.Port)
	}
}
