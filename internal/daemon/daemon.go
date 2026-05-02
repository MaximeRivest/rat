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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/procutil"
	"github.com/maximerivest/rat/internal/runtimeid"
	"github.com/maximerivest/rat/internal/securefs"
	"github.com/maximerivest/rat/internal/state"
)

const (
	BasePort     = 8717
	StartupMax   = 5 * time.Second
	PollInterval = 100 * time.Millisecond

	// ServeOptionsEnv carries structured runtime options to the background
	// `rat serve` process without exposing option values in process arguments.
	ServeOptionsEnv = "RAT_SERVE_OPTIONS_JSON"
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

func buildServeArgs(opts StartOpts, port int) []string {
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
	return args
}

func buildServeEnv(base []string, opts StartOpts) []string {
	env := removeEnv(base, ServeOptionsEnv)
	env = mergeEnv(env, opts.Env)
	if len(opts.Options) > 0 {
		data, _ := json.Marshal(opts.Options)
		env = mergeEnv(env, map[string]string{ServeOptionsEnv: string(data)})
	}
	return env
}

func mergeEnv(base []string, values map[string]string) []string {
	out := append([]string{}, base...)
	if len(values) == 0 {
		return out
	}

	index := make(map[string]int, len(out))
	for i, item := range out {
		key, _, ok := strings.Cut(item, "=")
		if ok {
			index[key] = i
		}
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry := key + "=" + values[key]
		if i, ok := index[key]; ok {
			out[i] = entry
			continue
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	return out
}

func removeEnv(base []string, keys ...string) []string {
	if len(keys) == 0 {
		return append([]string{}, base...)
	}
	remove := make(map[string]bool, len(keys))
	for _, key := range keys {
		remove[key] = true
	}
	out := make([]string, 0, len(base))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok && remove[key] {
			continue
		}
		out = append(out, item)
	}
	return out
}

// Start launches a kernel in the background and records it in state.
// Returns the state entry on success.
func Start(store *state.Store, opts StartOpts) (*state.Kernel, error) {
	if err := runtimeid.ValidateName(opts.Name); err != nil {
		return nil, err
	}

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

	// Find our own binary path
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find executable: %w", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	logDir := filepath.Join(filepath.Dir(store.Path()), "logs")

	// Select the port and record the started process while holding the state
	// file lock, so concurrent `rat start` calls don't reserve the same port.
	k, err := store.AllocatePortAndPut(BasePort, func(port int) (state.Kernel, error) {
		// Build the command: rat serve <name> --lang <lang> --http --port <port> --cwd <cwd> [--venv <venv>]
		// Runtime env/options are delivered through the child environment, not argv,
		// so secrets don't show up in process listings.
		args := buildServeArgs(opts, port)

		cmd := exec.Command(self, args...)
		cmd.Dir = opts.Cwd
		cmd.Env = buildServeEnv(os.Environ(), opts)

		// Detach: new session, no stdin, logs to file
		logFile, err := openKernelLog(logDir, opts.Name)
		if err != nil {
			return state.Kernel{}, fmt.Errorf("open log: %w", err)
		}

		cmd.Stdin = nil
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		procutil.ConfigureBackgroundProcess(cmd)

		if err := cmd.Start(); err != nil {
			logFile.Close()
			return state.Kernel{}, fmt.Errorf("start kernel: %w", err)
		}
		logFile.Close()

		// Don't wait for the child — it's detached
		go cmd.Wait()

		return state.Kernel{
			Name:    opts.Name,
			Lang:    opts.Lang,
			Port:    port,
			PID:     cmd.Process.Pid,
			Cwd:     opts.Cwd,
			Venv:    opts.Venv,
			Started: time.Now(),
		}, nil
	})
	if err != nil {
		if k != nil && k.PID > 0 {
			_ = procutil.Terminate(k.PID)
			return nil, fmt.Errorf("save state: %w", err)
		}
		return nil, err
	}

	// Wait for the HTTP endpoint to become ready
	logPath := filepath.Join(logDir, opts.Name+".log")
	if err := waitReady(k.Port, StartupMax); err != nil {
		// Surface the real error from the kernel log if available.
		tail := readLogTail(logPath, 512)
		if tail != "" {
			return k, fmt.Errorf("%s", strings.TrimSpace(tail))
		}
		return k, fmt.Errorf("kernel started (PID %d) but not responding on :%d: %w", k.PID, k.Port, err)
	}

	return k, nil
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
// This verifies the HTTP server, MCP protocol handling, and the runtime's
// status path. A simple HTTP probe only tests that something is listening.
func healthCheck(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	client := &http.Client{Timeout: timeout}

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"rat-health","version":"0.1.0"}}}`
	initReq, _ := http.NewRequest("POST", url, strings.NewReader(initBody))
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(initReq)
	if err != nil {
		return err
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if err := validateMCPResponse(resp, 1, "initialize", nil); err != nil {
		return err
	}

	notifyBody := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	notifyReq, _ := http.NewRequest("POST", url, strings.NewReader(notifyBody))
	notifyReq.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		notifyReq.Header.Set("Mcp-Session-Id", sessionID)
	}
	nr, err := client.Do(notifyReq)
	if err != nil {
		return err
	}
	if err := validateHTTPStatus(nr, "initialized notification"); err != nil {
		return err
	}

	statusBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ctl","arguments":{"op":"status"}}}`
	statusReq, _ := http.NewRequest("POST", url, strings.NewReader(statusBody))
	statusReq.Header.Set("Content-Type", "application/json")
	statusReq.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		statusReq.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp2, err := client.Do(statusReq)
	if err != nil {
		return err
	}
	return validateMCPResponse(resp2, 2, "ctl(status)", validateToolStatusResult)
}

type mcpEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *mcpError       `json:"error"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func validateHTTPStatus(resp *http.Response, label string) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d: %s", label, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func validateMCPResponse(resp *http.Response, id int, label string, validateResult func(json.RawMessage) error) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s: read response: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d: %s", label, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	env, err := findMCPEnvelope(body, id)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%s: JSON-RPC error %d: %s", label, env.Error.Code, env.Error.Message)
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return fmt.Errorf("%s: missing JSON-RPC result", label)
	}
	if validateResult != nil {
		if err := validateResult(env.Result); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func findMCPEnvelope(body []byte, id int) (mcpEnvelope, error) {
	for _, payload := range mcpPayloads(body) {
		if strings.TrimSpace(payload) == "" {
			continue
		}
		var env mcpEnvelope
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			return mcpEnvelope{}, fmt.Errorf("decode JSON-RPC response: %w", err)
		}
		if jsonIDMatches(env.ID, id) {
			return env, nil
		}
	}
	return mcpEnvelope{}, fmt.Errorf("no JSON-RPC response with id %d", id)
}

func mcpPayloads(body []byte) []string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil
	}
	if !strings.Contains(text, "data:") {
		return []string{text}
	}

	var payloads []string
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		payloads = append(payloads, strings.Join(dataLines, "\n"))
		dataLines = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if len(payloads) == 0 {
		return []string{text}
	}
	return payloads
}

func jsonIDMatches(raw json.RawMessage, want int) bool {
	if len(raw) == 0 {
		return false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n == want
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int(f) == want
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == strconv.Itoa(want)
	}
	return false
}

func validateToolStatusResult(raw json.RawMessage) error {
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	text := strings.TrimSpace(toolResultText(result.Content))
	if result.IsError {
		return fmt.Errorf("tool returned error: %s", text)
	}
	if text == "" {
		return fmt.Errorf("tool result has no status text")
	}
	firstLine := strings.ToLower(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]))
	if strings.HasPrefix(firstLine, "error") {
		return fmt.Errorf("unhealthy status: %s", text)
	}
	return nil
}

func toolResultText(content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var parts []string
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func openKernelLog(logDir, name string) (*os.File, error) {
	if err := runtimeid.ValidateName(name); err != nil {
		return nil, err
	}
	return securefs.OpenPrivateAppend(filepath.Join(logDir, name+".log"))
}

// readLogTail reads the last n bytes of a log file.
func readLogTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	size := info.Size()
	readN := int64(n)
	if readN > size {
		readN = size
	}
	buf := make([]byte, readN)
	_, err = f.ReadAt(buf, size-readN)
	if err != nil {
		return ""
	}
	return string(buf)
}
