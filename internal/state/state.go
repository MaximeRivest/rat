// Package state manages persistent kernel state.
//
// State is stored in ~/.config/rat/state.yaml. It tracks which kernels
// are running, their ports, PIDs, and configuration. Every CLI command
// that talks to a kernel reads this file to find (or auto-start) it.
//
// The state file is the single source of truth for "what's running."
// On every read, we verify PIDs are actually alive — dead entries get
// cleaned up automatically so users never see stale state.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Kernel represents a single kernel entry in the state file.
type Kernel struct {
	Name    string    `yaml:"name"`           // unique name: "sh", "py", "py-ml"
	Lang    string    `yaml:"lang"`           // canonical language: "sh", "py", "r", "ju", "js"
	Port    int       `yaml:"port"`           // HTTP port the MCP server listens on
	PID     int       `yaml:"pid"`            // OS process ID of the rat serve process
	Cwd     string    `yaml:"cwd"`            // working directory
	Venv    string    `yaml:"venv,omitempty"` // Python venv path (py only)
	Started time.Time `yaml:"started"`        // when the kernel was started
}

// Runtime is a saved named runtime configuration (from `rat add`).
// Unlike Kernel (which tracks a running process), Runtime persists
// across restarts and is used by auto-start to know which venv/cwd
// to use for a given name.
type Runtime struct {
	Name string `yaml:"name"`           // unique name: "py-ml", "py-web"
	Lang string `yaml:"lang"`           // canonical language: "py", "r", ...
	Cwd  string `yaml:"cwd,omitempty"`  // working directory
	Venv string `yaml:"venv,omitempty"` // venv path (py only)
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

// List returns all kernels, removing any with dead PIDs.
// This is the primary read method — always returns clean state.
func (s *Store) List() ([]Kernel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return nil, err
	}

	alive, dead := partitionAlive(f.Kernels)
	if len(dead) > 0 {
		f.Kernels = alive
		_ = s.writeLocked(f) // best-effort cleanup
	}

	return alive, nil
}

// Get returns a kernel by name, or nil if not found.
// Dead PIDs are cleaned up as a side effect.
func (s *Store) Get(name string) (*Kernel, error) {
	kernels, err := s.List()
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

// Put adds or updates a kernel entry. If a kernel with the same name
// exists, it is replaced.
func (s *Store) Put(k Kernel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.readLocked()
	if err != nil {
		return err
	}

	// Remove existing entry with same name
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
		// Corrupted file — start fresh rather than error out
		return &File{}, nil
	}
	return &f, nil
}

func (s *Store) writeLocked(f *File) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	// Write atomically: temp file + rename
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename state: %w", err)
	}

	return nil
}

// partitionAlive splits kernels into alive (PID exists) and dead.
func partitionAlive(kernels []Kernel) (alive, dead []Kernel) {
	for _, k := range kernels {
		if isAlive(k.PID) {
			alive = append(alive, k)
		} else {
			dead = append(dead, k)
		}
	}
	return
}

// isAlive checks if a process with the given PID exists.
// Uses kill(pid, 0) which checks existence without sending a signal.
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// isPortInUse does a quick check by trying to listen on the port.
func isPortInUse(port int) bool {
	// Use a raw socket check — try to bind, close immediately
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return true // can't check, assume in use
	}
	defer syscall.Close(fd)

	// Allow reuse so we don't block the port
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)

	addr := syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
	err = syscall.Bind(fd, &addr)
	return err != nil
}
