// Package state manages persistent kernel state.
//
// State is stored in ~/.config/rat/state.yaml. It tracks known runtimes,
// whether they are currently running, their ports, PIDs, and configuration.
// Every CLI command that talks to a kernel reads this file to resolve names
// and locate live processes.
package state

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/maximerivest/rat/internal/procutil"
	"github.com/maximerivest/rat/internal/runtimeid"
	"github.com/maximerivest/rat/internal/securefs"
	"gopkg.in/yaml.v3"
)

// Status constants for kernel state.
const (
	StatusRunning = "running"
	StatusStopped = "stopped"
)

// Kernel represents a single kernel entry in the state file.
type Kernel struct {
	Name    string    `yaml:"name"`              // unique name: "sh@proj", "py@proj", "py-ml"
	Lang    string    `yaml:"lang"`              // canonical language: "sh", "py", "r", "jl", "js"
	Port    int       `yaml:"port"`              // HTTP port the MCP server listens on
	PID     int       `yaml:"pid"`               // OS process ID of the rat serve process
	Cwd     string    `yaml:"cwd"`               // working directory
	Venv    string    `yaml:"venv,omitempty"`    // Python venv path (py only)
	Status  string    `yaml:"status"`            // "running" or "stopped"
	Started time.Time `yaml:"started"`           // when the kernel was started
	Stopped time.Time `yaml:"stopped,omitempty"` // when the kernel was stopped (zero if running)
}

// Runtime is a saved named runtime configuration (from `rat add`).
// Unlike Kernel (which tracks a running process), Runtime persists
// across restarts and is used by resolution/auto-start to know which
// cwd/venv to use for a given name.
type Runtime struct {
	Name        string            `yaml:"name"`                   // unique name: "py-ml", "py-web"
	Lang        string            `yaml:"lang"`                   // canonical language: "py", "r", ...
	Cwd         string            `yaml:"cwd,omitempty"`          // working directory
	Venv        string            `yaml:"venv,omitempty"`         // venv path (py only)
	RuntimePath string            `yaml:"runtime_path,omitempty"` // explicit binary path (e.g. /opt/python3.11/bin/python3)
	Options     map[string]string `yaml:"options,omitempty"`      // typed runtime options (e.g. model=claude-sonnet-4-5)
	Env         map[string]string `yaml:"env,omitempty"`          // extra env vars / secrets
}

// File is the top-level state file structure.
type File struct {
	Kernels  []Kernel  `yaml:"kernels"`
	Runtimes []Runtime `yaml:"runtimes,omitempty"`
}

// Store manages reading and writing the state file.
// All operations are mutex-protected for concurrent safety.
type Store struct {
	path string
	mu   sync.Mutex
}

// DefaultPath returns ~/.config/rat/state.yaml
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rat: cannot determine config directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(dir, "rat", "state.yaml")
}

// NewStore creates a Store at the given path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultStore creates a Store at the default path.
func DefaultStore() *Store {
	return NewStore(DefaultPath())
}

// Path returns the state file path.
func (s *Store) Path() string {
	return s.path
}

// ListKnown returns all kernels (running + stopped), normalizing stale state
// as a side effect. Dead running PIDs are marked stopped, and legacy entries
// with missing status are normalized.
func (s *Store) ListKnown() ([]Kernel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return nil, err
	}

	changed := normalizeKernels(f.Kernels)
	if changed {
		_ = s.writeLocked(f) // best-effort cleanup
	}

	return f.Kernels, nil
}

// ListRunning returns only kernels with status "running" and a live PID.
func (s *Store) ListRunning() ([]Kernel, error) {
	all, err := s.ListKnown()
	if err != nil {
		return nil, err
	}
	var running []Kernel
	for _, k := range all {
		if k.Status == StatusRunning {
			running = append(running, k)
		}
	}
	return running, nil
}

// GetKnown returns a known kernel by name, whether running or stopped.
// State is normalized as a side effect.
func (s *Store) GetKnown(name string) (*Kernel, error) {
	kernels, err := s.ListKnown()
	if err != nil {
		return nil, err
	}
	for i := range kernels {
		if kernels[i].Name == name {
			return &kernels[i], nil
		}
	}
	return nil, nil
}

// GetRunning returns a running kernel by name, or nil if it is absent or stopped.
func (s *Store) GetRunning(name string) (*Kernel, error) {
	kernels, err := s.ListRunning()
	if err != nil {
		return nil, err
	}
	for i := range kernels {
		if kernels[i].Name == name {
			return &kernels[i], nil
		}
	}
	return nil, nil
}

// MarkStopped changes a kernel's status to stopped. Returns true if found.
func (s *Store) MarkStopped(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return false, err
	}

	for i := range f.Kernels {
		if f.Kernels[i].Name == name {
			f.Kernels[i].Status = StatusStopped
			f.Kernels[i].PID = 0
			f.Kernels[i].Port = 0
			f.Kernels[i].Stopped = time.Now()
			return true, s.writeLocked(f)
		}
	}
	return false, nil
}

// Put adds or updates a kernel entry. If a kernel with the same name
// exists, it is replaced. Status defaults to "running" if not set.
func (s *Store) Put(k Kernel) error {
	if err := runtimeid.ValidateName(k.Name); err != nil {
		return err
	}
	if k.Status == "" {
		k.Status = StatusRunning
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return err
	}

	filtered := make([]Kernel, 0, len(f.Kernels))
	for _, existing := range f.Kernels {
		if existing.Name != k.Name {
			filtered = append(filtered, existing)
		}
	}
	filtered = append(filtered, k)
	f.Kernels = filtered

	return s.writeLocked(f)
}

// Remove deletes a kernel entry by name. Returns true if it existed.
func (s *Store) Remove(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return false, err
	}

	filtered := make([]Kernel, 0, len(f.Kernels))
	found := false
	for _, k := range f.Kernels {
		if k.Name == name {
			found = true
		} else {
			filtered = append(filtered, k)
		}
	}

	if !found {
		return false, nil
	}

	f.Kernels = filtered
	return true, s.writeLocked(f)
}

// NextPort finds the next available port starting from base.
// It checks both the state file and whether the port is actually in use.
func (s *Store) NextPort(base int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return 0, err
	}

	used := make(map[int]bool, len(f.Kernels))
	for _, k := range f.Kernels {
		used[k.Port] = true
	}

	for port := base; port < base+100; port++ {
		if !used[port] && !isPortInUse(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available port in range %d-%d", base, base+99)
}

// ── Runtimes (saved configurations) ─────────────────────────

// GetRuntime returns a saved runtime config by name, or nil.
func (s *Store) GetRuntime(name string) (*Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	for i := range f.Runtimes {
		if f.Runtimes[i].Name == name {
			return &f.Runtimes[i], nil
		}
	}
	return nil, nil
}

// PutRuntime adds or updates a runtime config.
func (s *Store) PutRuntime(r Runtime) error {
	if err := runtimeid.ValidateName(r.Name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.readLocked()
	if err != nil {
		return err
	}
	filtered := make([]Runtime, 0, len(f.Runtimes))
	for _, existing := range f.Runtimes {
		if existing.Name != r.Name {
			filtered = append(filtered, existing)
		}
	}
	filtered = append(filtered, r)
	f.Runtimes = filtered
	return s.writeLocked(f)
}

// RemoveRuntime deletes a saved runtime config. Returns true if found.
func (s *Store) RemoveRuntime(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.readLocked()
	if err != nil {
		return false, err
	}
	filtered := make([]Runtime, 0, len(f.Runtimes))
	found := false
	for _, r := range f.Runtimes {
		if r.Name == name {
			found = true
		} else {
			filtered = append(filtered, r)
		}
	}
	if !found {
		return false, nil
	}
	f.Runtimes = filtered
	return true, s.writeLocked(f)
}

// ListRuntimes returns all saved runtime configs.
func (s *Store) ListRuntimes() ([]Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	return f.Runtimes, nil
}

// ── internal ────────────────────────────────────────────────

func (s *Store) readLocked() (*File, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}

	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		// Corrupted file — start fresh rather than error out.
		return &File{}, nil
	}
	return &f, nil
}

func (s *Store) writeLocked(f *File) error {
	dir := filepath.Dir(s.path)
	if err := securefs.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := securefs.MakePrivateFile(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("secure temp state file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename state: %w", err)
	}
	if err := securefs.MakePrivateFile(s.path); err != nil {
		return fmt.Errorf("secure state file: %w", err)
	}

	return nil
}

// normalizeKernels updates legacy/invalid kernel entries in-place.
// It fills in missing status, marks dead running PIDs as stopped, and
// ensures stopped kernels do not keep stale PID/port values.
func normalizeKernels(kernels []Kernel) bool {
	changed := false
	for i := range kernels {
		k := &kernels[i]

		if k.Status == "" {
			if k.PID > 0 {
				k.Status = StatusRunning
			} else {
				k.Status = StatusStopped
				if k.Stopped.IsZero() {
					k.Stopped = time.Now()
				}
			}
			changed = true
		}

		if k.Status == StatusRunning && !procutil.IsAlive(k.PID) {
			k.Status = StatusStopped
			k.PID = 0
			k.Port = 0
			k.Stopped = time.Now()
			changed = true
		}

		if k.Status == StatusStopped {
			if k.PID != 0 {
				k.PID = 0
				changed = true
			}
			if k.Port != 0 {
				k.Port = 0
				changed = true
			}
			if k.Stopped.IsZero() {
				k.Stopped = time.Now()
				changed = true
			}
		}
	}
	return changed
}

// isPortInUse does a quick check by trying to listen on the port.
func isPortInUse(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}
