// Package daemon manages background kernel processes.
//
// "rat start sh" launches "rat serve sh --http --port <auto>" as a
// detached background process and records it in the state file.
// "rat stop sh" sends SIGTERM and cleans up state.
//
// The daemon is just a regular "rat serve" process running in the
// background — no custom daemon protocol, no PID files beyond state.yaml.
package daemon

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/maximerivest/rat/internal/state"
)

const (
	BasePort     = 8717
	StartupMax   = 5 * time.Second
	PollInterval = 100 * time.Millisecond
)

// StartOpts configures a kernel start.
type StartOpts struct {
	Name string // kernel name ("sh", "py", "py-ml")
	Lang string // canonical language ("sh", "py", ...)
	Cwd  string // working directory
	Venv string // venv path (py only)
}

// Start launches a kernel in the background and records it in state.
// Returns the state entry on success.
func Start(store *state.Store, opts StartOpts) (*state.Kernel, error) {
	// Check if already running
	existing, err := store.Get(opts.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil // already running
	}

	// Find a free port
	port, err := store.NextPort(BasePort)
	if err != nil {
		return nil, err
	}

	// Find our own binary path
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find executable: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	// Build the command: rat serve <name> --lang <lang> --http --port <port> --cwd <cwd>
	args := []string{"serve", opts.Name, "--lang", opts.Lang, "--kernel-name", opts.Name, "--http", "--port", strconv.Itoa(port)}
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}

	cmd := exec.Command(self, args...)
	cmd.Dir = opts.Cwd

	// Detach: new session, no stdin, logs to file
	logDir := filepath.Join(filepath.Dir(store.Path()), "logs")
	os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(
		filepath.Join(logDir, opts.Name+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644,
	)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}

	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start kernel: %w", err)
	}
	logFile.Close()

	// Don't wait for the child — it's detached
	go cmd.Wait()

	// Record in state
	k := state.Kernel{
		Name:    opts.Name,
		Lang:    opts.Lang,
		Port:    port,
		PID:     cmd.Process.Pid,
		Cwd:     opts.Cwd,
		Venv:    opts.Venv,
		Started: time.Now(),
	}
	if err := store.Put(k); err != nil {
		// Kill the process we just started — state is the source of truth
		syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		return nil, fmt.Errorf("save state: %w", err)
	}

	// Wait for the HTTP endpoint to become ready
	if err := waitReady(port, StartupMax); err != nil {
		// Kernel started but isn't responding — leave it running,
		// user can check logs. Don't remove from state.
		return &k, fmt.Errorf("kernel started (PID %d) but not responding on :%d: %w", k.PID, port, err)
	}

	return &k, nil
}

// Stop terminates a kernel by name. Returns an error if not found.
func Stop(store *state.Store, name string) error {
	k, err := store.Get(name)
	if err != nil {
		return err
	}
	if k == nil {
		return fmt.Errorf("kernel %q is not running", name)
	}

	return stopKernel(store, k)
}

// StopAll terminates all running kernels.
func StopAll(store *state.Store) (int, error) {
	kernels, err := store.List()
	if err != nil {
		return 0, err
	}
	if len(kernels) == 0 {
		return 0, nil
	}

	count := 0
	for _, k := range kernels {
		if err := stopKernel(store, &k); err == nil {
			count++
		}
	}
	return count, nil
}

// stopKernel sends SIGTERM, waits briefly, escalates to SIGKILL, cleans state.
func stopKernel(store *state.Store, k *state.Kernel) error {
	// Send SIGTERM
	if err := syscall.Kill(k.PID, syscall.SIGTERM); err != nil {
		// Process already gone — just clean up state
		store.Remove(k.Name)
		return nil
	}

	// Wait up to 3 seconds for graceful shutdown
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !isAlive(k.PID) {
			store.Remove(k.Name)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Escalate to SIGKILL
	syscall.Kill(k.PID, syscall.SIGKILL)
	time.Sleep(100 * time.Millisecond)
	store.Remove(k.Name)
	return nil
}

// waitReady polls the MCP endpoint until it responds or timeout.
func waitReady(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := client.Post(url, "application/json", nil)
		if err == nil {
			resp.Body.Close()
			// Any response (even 4xx) means the server is up
			return nil
		}
		time.Sleep(PollInterval)
	}

	return fmt.Errorf("timeout after %s", timeout)
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
