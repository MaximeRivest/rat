package python

import (
	"bufio"
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/procutil"
)

// activityEntry is a JSON line written to the activity log so that
// frontends can display what other MCP clients executed.
type activityEntry struct {
	N      int    `json:"n"`
	Code   string `json:"code"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
	Time   int64  `json:"t"`
	Client string `json:"client,omitempty"`
}

//go:embed kernel.py
var kernelScript string

type request struct {
	Op         string `json:"op"`
	Code       string `json:"code,omitempty"`
	At         string `json:"at,omitempty"`
	Cursor     int    `json:"cursor,omitempty"`
	Text       string `json:"text,omitempty"`
	AllowStdin bool   `json:"allow_stdin,omitempty"`
	Full       bool   `json:"full,omitempty"`
}

type response struct {
	Op      string `json:"op,omitempty"`
	Success bool   `json:"success,omitempty"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Text    string `json:"text,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Vars    int    `json:"vars,omitempty"`
}

// partialBuf accumulates live output during execution so Ctl("output") can
// return partial output to streaming clients.
type partialBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *partialBuf) Append(s string) {
	b.mu.Lock()
	b.buf.WriteString(s)
	b.mu.Unlock()
}

func (b *partialBuf) Get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *partialBuf) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *partialBuf) Reset() {
	b.mu.Lock()
	b.buf.Reset()
	b.mu.Unlock()
}

// Python implements kernel.Kernel for a persistent Python subprocess.
type Python struct {
	name       string
	cwd        string
	cmdPath    string
	cmdArgs    []string
	scriptPath string
	version    string // e.g. "3.12.1", detected at startup

	mu              sync.Mutex
	cmd             *exec.Cmd
	protocolConn    net.Conn       // private protocol connection (used for read deadlines)
	stdin           io.WriteCloser // private protocol writer
	stdout          *bufio.Reader  // private protocol reader
	stderrMu        sync.Mutex
	stderrBuf       bytes.Buffer
	executionCount  int
	executing       atomic.Bool
	waitingForInput atomic.Bool
	writeMu         sync.Mutex
	partial         partialBuf // live output during execution
	externalOutput  partialBuf // process stdout/stderr captured for final output

	interruptMu   sync.Mutex
	interruptProc *os.Process
}

// New creates a new Python kernel. If venv is non-empty, it is used
// as the virtual environment (its python binary is preferred).
// If runtimePath is non-empty, it overrides all auto-detection and
// uses that exact binary.
func New(name, cwd, venv, runtimePath string) (*Python, error) {
	var cmdPath string
	var cmdArgs []string
	var err error
	if runtimePath != "" {
		cmdPath = runtimePath
	} else {
		cmdPath, cmdArgs, err = detectPythonCommand(venv)
	}
	if err != nil {
		return nil, err
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, _ = filepath.Abs(cwd)

	scriptPath, err := writeKernelScript(name)
	if err != nil {
		return nil, err
	}

	_ = os.Remove(filepath.Join(filepath.Dir(scriptPath), "activity.jsonl"))

	p := &Python{
		name:       name,
		cwd:        cwd,
		cmdPath:    cmdPath,
		cmdArgs:    cmdArgs,
		scriptPath: scriptPath,
		version:    detectPythonVersion(cmdPath, cmdArgs),
	}
	if err := p.ensureStartedLocked(); err != nil {
		return nil, err
	}
	return p, nil
}

// Run executes Python code in the persistent kernel.
func (p *Python) Run(code string) kernel.RunResult {
	start := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureStartedLocked(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: p.executionCount, Duration: int(time.Since(start).Milliseconds())}
	}

	p.executionCount++
	execCount := p.executionCount
	p.executing.Store(true)
	defer p.executing.Store(false)

	p.partial.Reset()
	p.externalOutput.Reset()

	if err := p.sendLocked(request{Op: "run", Code: code, AllowStdin: true}); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}

	// Read responses in a loop: output_chunk messages stream partial
	// output that Ctl("output") can return to polling clients.
	var resp response
	for {
		var err error
		resp, err = p.readLocked()
		if err != nil {
			return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
		}
		switch resp.Op {
		case "output_chunk":
			p.partial.Append(resp.Text)
			continue
		case "input_request":
			p.waitingForInput.Store(true)
			continue
		case "input_delivered":
			p.waitingForInput.Store(false)
			continue
		}
		// Any other message is the final result.
		p.waitingForInput.Store(false)
		break
	}

	p.waitForExternalOutputQuiet()
	output := strings.TrimSpace(joinOutput(resp.Output, p.externalOutput.Get()))
	if !resp.Success {
		errText := strings.TrimSpace(resp.Error)
		if output != "" {
			errText = strings.TrimSpace(output + "\n" + errText)
		}
		if errText == "" {
			errText = "execution failed"
		}
		errResult := kernel.RunResult{
			Success:   false,
			Output:    output,
			Error:     errText,
			ExecCount: execCount,
			Duration:  int(time.Since(start).Milliseconds()),
			Vars:      resp.Vars,
		}
		p.logActivity(code, errResult)
		return errResult
	}

	result := kernel.RunResult{
		Success:   true,
		Output:    output,
		ExecCount: execCount,
		Duration:  int(time.Since(start).Milliseconds()),
		Vars:      resp.Vars,
	}
	p.logActivity(code, result)
	return result
}

// SendInput responds to a waiting Python input() call.
func (p *Python) SendInput(text string) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return fmt.Errorf("python kernel not started")
	}
	data, err := json.Marshal(request{Op: "input", Text: text})
	if err != nil {
		return err
	}
	if _, err := p.stdin.Write(append(data, '\n')); err != nil {
		p.killLocked()
		return fmt.Errorf("write input to python kernel: %w", err)
	}
	return nil
}

// IsWaitingForInput returns true when the running code is blocked on input().
func (p *Python) IsWaitingForInput() bool {
	return p.waitingForInput.Load()
}

// Look inspects the Python runtime.
func (p *Python) Look(req kernel.LookRequest) kernel.LookResult {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureStartedLocked(); err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	var err error
	switch {
	case req.Code != "":
		err = p.sendLocked(request{Op: "complete", Code: req.Code, Cursor: req.Cursor})
	case req.At != "":
		err = p.sendLocked(request{Op: "look_at", At: req.At, Full: req.Full})
	default:
		err = p.sendLocked(request{Op: "look_overview"})
	}
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	resp, err := p.readLockedWithTimeout(10 * time.Second)
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	if resp.Text != "" {
		return kernel.LookResult{Text: resp.Text}
	}
	if resp.Error != "" {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %s", strings.TrimSpace(resp.Error))}
	}
	return kernel.LookResult{Text: ""}
}

// Ctl controls the Python runtime.
func (p *Python) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset", "restart":
		p.mu.Lock()
		defer p.mu.Unlock()
		p.killLocked()
		p.executionCount = 0
		// Clear activity log so frontends don't show stale entries.
		if path := p.ActivityLogPath(); path != "" {
			_ = os.Truncate(path, 0)
		}
		if err := p.ensureStartedLocked(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		if op == "reset" {
			return kernel.CtlResult{Text: "RESET | namespace cleared | 0 vars"}
		}
		return kernel.CtlResult{Text: "RESTARTED | fresh python session"}
	case "cancel":
		if err := p.interrupt(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		return kernel.CtlResult{Text: "CANCELLED"}
	case "status":
		state := "idle"
		if p.executing.Load() {
			state = "busy"
			if p.waitingForInput.Load() {
				state = "waiting_for_input"
			}
		}
		if p.version != "" {
			state += "\nruntime_version: Python " + p.version
		}
		return kernel.CtlResult{Text: state}
	case "output":
		// Return partial stdout accumulated during the current execution.
		// Lock-free relative to p.mu so it doesn't block Run().
		return kernel.CtlResult{Text: p.partial.Get()}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'. Use reset, cancel, restart, or status.", op)}
	}
}

// Shutdown tears down the Python kernel process.
func (p *Python) Shutdown() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killLocked()
	if p.scriptPath != "" {
		_ = os.Remove(p.scriptPath)
	}
	return nil
}

func (p *Python) ensureStartedLocked() error {
	if p.cmd != nil && p.cmd.Process != nil && p.cmd.ProcessState == nil {
		return nil
	}

	args := append(append([]string{}, p.cmdArgs...), p.scriptPath)
	cmd := exec.Command(p.cmdPath, args...)
	cmd.Dir = p.cwd
	cmd.Env = os.Environ()
	procutil.HideWindow(cmd)

	listener, token, err := newProtocolListener()
	if err != nil {
		return err
	}
	defer listener.Close()
	cmd.Env = append(cmd.Env,
		"RAT_PROTOCOL_TCP_ADDR="+listener.Addr().String(),
		"RAT_PROTOCOL_TOKEN="+token,
	)

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open devnull for python stdin: %w", err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull

	userStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("python stdout: %w", err)
	}
	userStderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("python stderr: %w", err)
	}

	p.resetStderrBuf()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start python kernel: %w", err)
	}
	go p.consumeUserOutput(userStdout)
	go p.consumeUserOutput(userStderr)

	conn, reader, err := acceptProtocol(listener, token)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	p.cmd = cmd
	p.protocolConn = conn
	p.stdin = conn
	p.stdout = reader
	p.interruptMu.Lock()
	p.interruptProc = cmd.Process
	p.interruptMu.Unlock()

	if err := p.sendLocked(request{Op: "ping"}); err != nil {
		p.killLocked()
		return err
	}
	resp, err := p.readLocked()
	if err != nil {
		p.killLocked()
		return err
	}
	if !resp.OK {
		p.killLocked()
		return fmt.Errorf("python kernel failed to initialize: %s", strings.TrimSpace(resp.Error))
	}

	return nil
}

func newProtocolListener() (net.Listener, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("python protocol listener: %w", err)
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		_ = listener.Close()
		return nil, "", fmt.Errorf("python protocol token: %w", err)
	}
	return listener, hex.EncodeToString(buf), nil
}

func acceptProtocol(
	listener net.Listener,
	token string,
) (net.Conn, *bufio.Reader, error) {
	tcp, ok := listener.(*net.TCPListener)
	if ok {
		_ = tcp.SetDeadline(time.Now().Add(10 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		return nil, nil, fmt.Errorf("accept python protocol connection: %w", err)
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("read python protocol hello: %w", err)
	}
	var hello struct {
		Op    string `json:"op"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &hello); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("decode python protocol hello: %w", err)
	}
	if hello.Op != "protocol_hello" || hello.Token != token {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("invalid python protocol hello")
	}
	return conn, reader, nil
}

func (p *Python) consumeUserOutput(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			text := string(buf[:n])
			if p.executing.Load() {
				p.partial.Append(text)
				p.externalOutput.Append(text)
			} else {
				p.appendStderrBuf(text)
			}
		}
		if err != nil {
			return
		}
	}
}

func (p *Python) resetStderrBuf() {
	p.stderrMu.Lock()
	p.stderrBuf.Reset()
	p.stderrMu.Unlock()
}

func (p *Python) appendStderrBuf(text string) {
	p.stderrMu.Lock()
	_, _ = p.stderrBuf.WriteString(text)
	p.stderrMu.Unlock()
}

func (p *Python) getStderrBuf() string {
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	return p.stderrBuf.String()
}

func (p *Python) waitForExternalOutputQuiet() {
	deadline := time.Now().Add(150 * time.Millisecond)
	quietSince := time.Now()
	lastLen := p.externalOutput.Len()
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		n := p.externalOutput.Len()
		if n != lastLen {
			lastLen = n
			quietSince = time.Now()
			continue
		}
		if time.Since(quietSince) >= 20*time.Millisecond {
			return
		}
	}
}

func joinOutput(primary, external string) string {
	if primary == "" {
		return external
	}
	if external == "" {
		return primary
	}
	if strings.HasSuffix(primary, "\n") || strings.HasPrefix(external, "\n") {
		return primary + external
	}
	return primary + "\n" + external
}

func (p *Python) sendLocked(req request) error {
	if p.stdin == nil {
		return fmt.Errorf("python kernel not started")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(append(data, '\n')); err != nil {
		p.killLocked()
		return fmt.Errorf("write to python kernel: %w", err)
	}
	return nil
}

func (p *Python) readLocked() (response, error) {
	return p.readLockedWithTimeout(0)
}

func (p *Python) readLockedWithTimeout(timeout time.Duration) (response, error) {
	if p.stdout == nil {
		return response{}, fmt.Errorf("python kernel not started")
	}
	if timeout > 0 && p.protocolConn != nil {
		conn := p.protocolConn
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		defer conn.SetReadDeadline(time.Time{})
	}
	line, err := p.stdout.ReadBytes('\n')
	if err != nil {
		stderr := strings.TrimSpace(p.getStderrBuf())
		p.killLocked()
		if stderr != "" {
			return response{}, fmt.Errorf("read from python kernel: %w: %s", err, stderr)
		}
		return response{}, fmt.Errorf("read from python kernel: %w", err)
	}
	var resp response
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return response{}, fmt.Errorf("decode python kernel response: %w", err)
	}
	return resp, nil
}

func (p *Python) interrupt() error {
	p.interruptMu.Lock()
	proc := p.interruptProc
	p.interruptMu.Unlock()
	if proc == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		return proc.Kill()
	}
	return proc.Signal(syscall.SIGINT)
}

func (p *Python) killLocked() {
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	p.protocolConn = nil
	if p.cmd != nil {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.cmd.Wait()
		p.cmd = nil
	}
	p.interruptMu.Lock()
	p.interruptProc = nil
	p.interruptMu.Unlock()
	p.stdout = nil
}

// logActivity appends an execution record to the activity log so
// frontends can see what other MCP clients executed.
func (p *Python) logActivity(code string, r kernel.RunResult) {
	if p.scriptPath == "" {
		return
	}
	path := filepath.Join(filepath.Dir(p.scriptPath), "activity.jsonl")
	e := activityEntry{
		N:      r.ExecCount,
		Code:   truncateLog(code, 500),
		Output: truncateLog(r.Output, 500),
		OK:     r.Success,
		Time:   time.Now().Unix(),
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// ActivityLogPath returns the path to the activity log for this kernel.
func (p *Python) ActivityLogPath() string {
	if p.scriptPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p.scriptPath), "activity.jsonl")
}

func truncateLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func detectPythonVersion(cmdPath string, cmdArgs []string) string {
	args := append(append([]string{}, cmdArgs...), "-c", "import platform; print(platform.python_version())")
	cmd := exec.Command(cmdPath, args...)
	procutil.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectPythonCommand(venv string) (string, []string, error) {
	if v := os.Getenv("RAT_PYTHON"); v != "" {
		return v, nil, nil
	}
	// Explicit venv parameter (from --venv or auto-detected).
	if venv != "" {
		path := filepath.Join(venv, "bin", "python")
		if runtime.GOOS == "windows" {
			path = filepath.Join(venv, "Scripts", "python.exe")
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil, nil
		}
		// venv specified but python not found — fall through
	}
	// VIRTUAL_ENV environment variable.
	if envVenv := os.Getenv("VIRTUAL_ENV"); envVenv != "" {
		path := filepath.Join(envVenv, "bin", "python")
		if runtime.GOOS == "windows" {
			path = filepath.Join(envVenv, "Scripts", "python.exe")
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil, nil
		}
	}
	for _, candidate := range []string{"python3", "python"} {
		if path, err := exec.LookPath(candidate); err == nil {
			if !IsWindowsStoreAlias(path) {
				return path, nil, nil
			}
		}
	}
	if path, err := exec.LookPath("py"); err == nil {
		return path, []string{"-3"}, nil
	}
	// On Windows, probe well-known install locations as a last resort.
	if runtime.GOOS == "windows" {
		if p := FindWindowsPython(); p != "" {
			return p, nil, nil
		}
	}
	return "", nil, fmt.Errorf("python not found (tried RAT_PYTHON, active venv, python3, python, py -3)")
}

// IsWindowsStoreAlias returns true if path points to the Windows Store
// python.exe stub (inside WindowsApps) which is not a real interpreter.
func IsWindowsStoreAlias(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	// Resolve symlinks / reparse points to get the real location.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	norm := strings.ToLower(filepath.ToSlash(resolved))
	return strings.Contains(norm, "windowsapps")
}

// FindWindowsPython searches common Windows install directories for python.exe.
func FindWindowsPython() string {
	// Ordered from most-specific to least.
	var roots []string
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "Python"))
	}
	roots = append(roots,
		`C:\Program Files\Python`,
		`C:\Program Files (x86)\Python`,
	)
	for _, root := range roots {
		// Look for e.g. Python312\python.exe, Python311\python.exe, etc.
		matches, _ := filepath.Glob(filepath.Join(root, "*", "python.exe"))
		if len(matches) == 0 {
			// Also try root itself (some installers put python.exe directly).
			matches, _ = filepath.Glob(filepath.Join(root+"*", "python.exe"))
		}
		if len(matches) > 0 {
			// Pick the last match (highest version by lexicographic sort).
			return matches[len(matches)-1]
		}
	}
	return ""
}

func writeKernelScript(name string) (string, error) {
	kdir, err := cachedir.Kernels(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(kdir, "python-kernel.py")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(kernelScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
