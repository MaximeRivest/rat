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
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/procutil"
	"github.com/maximerivest/rat/internal/state"
)

const (
	BasePort     = 8717
	StartupMax   = 5 * time.Second
	PollInterval = 100 * time.Millisecond
)

// StartOpts configures a kernel start.
type StartOpts struct {
	Name        string            // kernel name ("sh", "py", "py-ml")
	Lang        string            // canonical language ("sh", "py", ...)
	Cwd         string            // working directory
	Venv        string            // venv path (py only)
	RuntimePath string            // explicit binary path (e.g. /opt/python3.11/bin/python3)
	Options     map[string]string // structured runtime options (e.g. model=claude-sonnet-4-5)
	Env         map[string]string // extra env vars / secrets
}

// Start launches a kernel in the background and records it in state.
// Returns the state entry on success.
func Start(store *state.Store, opts StartOpts) (*state.Kernel, error) {
	// Check if already running and actually responding.
	existing, err := store.GetKnown(opts.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Status == state.StatusRunning {
		// PID is alive — but is the kernel actually functional?
		// A stopped (Ctrl-Z) or hung process still has a live PID,
		// and the Go HTTP server may respond even if the Python
		// subprocess is wedged. Do a real health check.
		if err := healthCheck(existing.Port, 3*time.Second); err == nil {
			return existing, nil // truly running and healthy
		}
		// Not responding — kill the stale process and start fresh.
		forceStopAndRemove(store, existing)
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

	// Build the command: rat serve <name> --lang <lang> --http --port <port> --cwd <cwd> [--venv <venv>]
	args := []string{"serve", opts.Name, "--lang", opts.Lang, "--kernel-name", opts.Name, "--http", "--port", strconv.Itoa(port)}
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	if opts.Venv != "" {
		args = append(args, "--venv", opts.Venv)
	}
	if opts.RuntimePath != "" {
		args = append(args, "--runtime", opts.RuntimePath)
	}
	for k, v := range opts.Options {
		args = append(args, "--opt", k+"="+v)
	}
	for k, v := range opts.Env {
		args = append(args, "--env", k+"="+v)
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
	procutil.ConfigureBackgroundProcess(cmd)

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
		_ = procutil.Terminate(cmd.Process.Pid)
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
	k, err := store.GetRunning(name)
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
	kernels, err := store.ListRunning()
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

// forceStopAndRemove kills a kernel and removes its state entry entirely.
// Used when daemon.Start finds a stale/unhealthy kernel it needs to replace.
func forceStopAndRemove(store *state.Store, k *state.Kernel) {
	if k.PID > 0 {
		_ = procutil.Terminate(k.PID)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if !procutil.IsAlive(k.PID) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if procutil.IsAlive(k.PID) {
			_ = procutil.Kill(k.PID)
			time.Sleep(100 * time.Millisecond)
		}
	}
	store.Remove(k.Name)
}

// stopKernel sends SIGTERM, waits briefly, escalates to SIGKILL,
// then marks the kernel as stopped in state (preserves the entry).
func stopKernel(store *state.Store, k *state.Kernel) error {
	if k == nil {
		return fmt.Errorf("kernel is not running")
	}
	if k.Status != state.StatusRunning || k.PID <= 0 {
		return fmt.Errorf("kernel %q is not running", k.Name)
	}

	// Send SIGTERM
	if err := procutil.Terminate(k.PID); err != nil {
		// Process already gone — just mark stopped
		store.MarkStopped(k.Name)
		return nil
	}

	// Wait up to 3 seconds for graceful shutdown
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !procutil.IsAlive(k.PID) {
			store.MarkStopped(k.Name)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Escalate to SIGKILL
	_ = procutil.Kill(k.PID)
	time.Sleep(100 * time.Millisecond)
	store.MarkStopped(k.Name)
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

// healthCheck does a full MCP round-trip: initialize → ctl(status).
// This verifies the Go server AND the language subprocess are both
// functional. A simple HTTP probe only tests the Go server.
func healthCheck(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	client := &http.Client{Timeout: timeout}

	// 1. Initialize MCP session
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"rat-health","version":"0.1.0"}}}`
	resp, err := client.Post(url, "application/json", strings.NewReader(initBody))
	if err != nil {
		return err
	}
	body, _ := readBody(resp)
	resp.Body.Close()

	// Extract session ID
	sessionID := resp.Header.Get("Mcp-Session-Id")

	// Send initialized notification
	notifyBody := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	req, _ := http.NewRequest("POST", url, strings.NewReader(notifyBody))
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	nr, err := client.Do(req)
	if err != nil {
		return err
	}
	nr.Body.Close()

	// 2. Call ctl(status) — this exercises the language subprocess
	statusBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ctl","arguments":{"op":"status"}}}`
	req2, _ := http.NewRequest("POST", url, strings.NewReader(statusBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req2.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		return err
	}
	resp2.Body.Close()

	_ = body // used for init check
	return nil
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return buf[:n], nil
}
