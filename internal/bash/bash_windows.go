//go:build windows

package bash

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/procutil"
)

//go:embed kernel_windows.ps1
var kernelScript string

type activityEntry struct {
	N      int    `json:"n"`
	Code   string `json:"code"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
	Time   int64  `json:"t"`
	Client string `json:"client,omitempty"`
}

type request struct {
	Op     string `json:"op"`
	Code   string `json:"code,omitempty"`
	At     string `json:"at,omitempty"`
	Cursor int    `json:"cursor,omitempty"`
	Text   string `json:"text,omitempty"`
}

type response struct {
	Op      string `json:"op,omitempty"`
	Success bool   `json:"success,omitempty"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Text    string `json:"text,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Vars    int    `json:"vars,omitempty"`
}

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

func (b *partialBuf) Reset() {
	b.mu.Lock()
	b.buf.Reset()
	b.mu.Unlock()
}

// Bash is a persistent PowerShell-backed shared shell kernel on Windows.
type Bash struct {
	name       string
	cwd        string
	cmdPath    string
	cmdArgs    []string
	scriptPath string
	version    string

	mu              sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdout          *bufio.Reader
	stderrBuf       bytes.Buffer
	executionCount  int
	executing       atomic.Bool
	waitingForInput atomic.Bool
	writeMu         sync.Mutex
	partial         partialBuf
	interruptProc   atomic.Pointer[os.Process]
}

func New(name, cwd string) (*Bash, error) {
	cmdPath, cmdArgs, err := detectPowerShellCommand()
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

	b := &Bash{
		name:       name,
		cwd:        cwd,
		cmdPath:    cmdPath,
		cmdArgs:    cmdArgs,
		scriptPath: scriptPath,
		version:    detectPowerShellVersion(cmdPath, cmdArgs),
	}
	if err := b.ensureStartedLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

func SessionName(name string) string { return name }

func Attach(name string) error {
	return fmt.Errorf("shared native shell attach is not available on Windows; use the MCP frontend")
}

func (b *Bash) Run(code string) kernel.RunResult {
	start := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.ensureStartedLocked(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: b.executionCount, Duration: int(time.Since(start).Milliseconds())}
	}

	b.executionCount++
	execCount := b.executionCount
	b.executing.Store(true)
	defer b.executing.Store(false)
	b.partial.Reset()

	if err := b.sendLocked(request{Op: "run", Code: code}); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}

	for {
		resp, err := b.readLocked()
		if err != nil {
			return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
		}
		switch resp.Op {
		case "output_chunk":
			b.partial.Append(resp.Text)
			continue
		case "input_request":
			b.waitingForInput.Store(true)
			continue
		}
		b.waitingForInput.Store(false)
		output := strings.TrimSpace(resp.Output)
		if !resp.Success {
			errText := strings.TrimSpace(resp.Error)
			if errText == "" {
				errText = output
			}
			result := kernel.RunResult{Success: false, Output: output, Error: errText, ExecCount: execCount, Duration: int(time.Since(start).Milliseconds()), Vars: resp.Vars}
			b.logActivity(code, result)
			return result
		}
		result := kernel.RunResult{Success: true, Output: output, ExecCount: execCount, Duration: int(time.Since(start).Milliseconds()), Vars: resp.Vars}
		b.logActivity(code, result)
		return result
	}
}

func (b *Bash) SendInput(text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureStartedLocked(); err != nil {
		return err
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := b.sendLocked(request{Op: "input", Text: text}); err != nil {
		return err
	}
	resp, err := b.readLocked()
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("input rejected")
	}
	b.waitingForInput.Store(false)
	return nil
}

func (b *Bash) IsWaitingForInput() bool {
	return b.waitingForInput.Load()
}

func (b *Bash) Look(req kernel.LookRequest) kernel.LookResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureStartedLocked(); err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	var q request
	switch {
	case req.At != "":
		q = request{Op: "look_at", At: req.At}
	case req.Code != "":
		q = request{Op: "complete", Code: req.Code, Cursor: req.Cursor}
	default:
		q = request{Op: "look_overview"}
	}
	if err := b.sendLocked(q); err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	resp, err := b.readLocked()
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	return kernel.LookResult{Text: strings.TrimSpace(resp.Text)}
}

func (b *Bash) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset", "restart", "cancel":
		b.mu.Lock()
		defer b.mu.Unlock()
		b.killLocked()
		b.executionCount = 0
		if path := b.ActivityLogPath(); path != "" {
			_ = os.Truncate(path, 0)
		}
		if op == "cancel" {
			return kernel.CtlResult{Text: "CANCELLED"}
		}
		if err := b.ensureStartedLocked(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		if op == "reset" {
			return kernel.CtlResult{Text: "RESET | namespace cleared | 0 vars"}
		}
		return kernel.CtlResult{Text: "RESTARTED | fresh powershell session"}
	case "status":
		state := "idle"
		if b.executing.Load() {
			state = "busy"
			if b.waitingForInput.Load() {
				state = "waiting_for_input"
			}
		}
		if b.version != "" {
			state += "\nruntime_version: PowerShell " + b.version
		}
		return kernel.CtlResult{Text: state}
	case "output":
		return kernel.CtlResult{Text: b.partial.Get()}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'. Use reset, cancel, restart, or status.", op)}
	}
}

func (b *Bash) Shutdown() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.killLocked()
	if b.scriptPath != "" {
		_ = os.Remove(b.scriptPath)
	}
	return nil
}

func (b *Bash) ensureStartedLocked() error {
	if b.cmd != nil && b.cmd.Process != nil && b.cmd.ProcessState == nil {
		return nil
	}
	args := append(append([]string{}, b.cmdArgs...), b.scriptPath)
	cmd := exec.Command(b.cmdPath, args...)
	cmd.Dir = b.cwd
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("powershell stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("powershell stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("powershell stderr: %w", err)
	}

	b.stderrBuf.Reset()
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start powershell kernel: %w", err)
	}
	go func() { _, _ = io.Copy(&b.stderrBuf, stderr) }()

	b.cmd = cmd
	b.stdin = stdin
	b.stdout = bufio.NewReader(stdout)
	b.interruptProc.Store(cmd.Process)

	if err := b.sendLocked(request{Op: "ping"}); err != nil {
		b.killLocked()
		return err
	}
	resp, err := b.readLocked()
	if err != nil {
		b.killLocked()
		return err
	}
	if !resp.OK {
		b.killLocked()
		return fmt.Errorf("powershell kernel failed to initialize: %s", strings.TrimSpace(resp.Error))
	}
	return nil
}

func (b *Bash) sendLocked(req request) error {
	if b.stdin == nil {
		return fmt.Errorf("powershell kernel not started")
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if _, err := b.stdin.Write(append(data, '\n')); err != nil {
		b.killLocked()
		return fmt.Errorf("write to powershell kernel: %w", err)
	}
	return nil
}

func (b *Bash) readLocked() (response, error) {
	if b.stdout == nil {
		return response{}, fmt.Errorf("powershell kernel not started")
	}
	line, err := b.stdout.ReadBytes('\n')
	if err != nil {
		stderr := strings.TrimSpace(b.stderrBuf.String())
		b.killLocked()
		if stderr != "" {
			return response{}, fmt.Errorf("read from powershell kernel: %w: %s", err, stderr)
		}
		return response{}, fmt.Errorf("read from powershell kernel: %w", err)
	}
	var resp response
	if err := json.Unmarshal(bytes.TrimSpace(line), &resp); err != nil {
		return response{}, fmt.Errorf("decode powershell kernel response: %w", err)
	}
	return resp, nil
}

func (b *Bash) killLocked() {
	if b.stdin != nil {
		_ = b.stdin.Close()
		b.stdin = nil
	}
	if proc := b.interruptProc.Load(); proc != nil {
		_ = procutil.Interrupt(proc)
	}
	if b.cmd != nil {
		if b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
		_ = b.cmd.Wait()
		b.cmd = nil
	}
	b.interruptProc.Store(nil)
	b.stdout = nil
	b.waitingForInput.Store(false)
	b.executing.Store(false)
}

func detectPowerShellCommand() (string, []string, error) {
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{name: "pwsh", args: []string{"-NoLogo", "-NoProfile", "-File"}},
		{name: "powershell", args: []string{"-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File"}},
	} {
		path, err := exec.LookPath(candidate.name)
		if err == nil {
			return path, candidate.args, nil
		}
	}
	return "", nil, fmt.Errorf("powershell not found")
}

func detectPowerShellVersion(cmdPath string, cmdArgs []string) string {
	args := append(append([]string{}, cmdArgs[:len(cmdArgs)-1]...), "-Command", "$PSVersionTable.PSVersion.ToString()")
	out, err := exec.Command(cmdPath, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func writeKernelScript(name string) (string, error) {
	kdir, err := cachedir.Kernels(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(kdir, "powershell-kernel.ps1")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(kernelScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (b *Bash) logActivity(code string, r kernel.RunResult) {
	path := b.ActivityLogPath()
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	e := activityEntry{N: r.ExecCount, Code: code, Output: nonEmpty(r.Output, r.Error), OK: r.Success, Time: time.Now().Unix()}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
}

func (b *Bash) ActivityLogPath() string {
	kdir, err := cachedir.Kernels(b.name)
	if err != nil {
		return ""
	}
	return filepath.Join(kdir, "activity.jsonl")
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
