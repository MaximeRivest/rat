// Package pi implements the Kernel interface for a shared pi session.
//
// Like bash, pi runs inside a tmux session. Humans attach with `rat pi`
// and see the full TUI. MCP clients inject prompts into that same session
// via tmux send-keys. A pi extension (rat-bridge) signals completion back
// to this Go code via a control file, so `rat run pi` gets structured results.
//
// This means everything is shared: the human and Claude see the same
// conversation. If Claude runs a prompt while you're attached, you see it
// live. If you type while Claude is waiting, you see it too.
package pi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximerivest/rat/internal/kernel"
)

//go:embed extension.ts
var bridgeExtension string

// Pi is a tmux-backed shared pi kernel.
type Pi struct {
	name        string
	cwd         string
	tmuxPath    string
	piPath      string
	sessionName string
	dataDir     string
	controlPath string
	currentID   string
	extPath     string // path to the extracted bridge extension

	mu             sync.Mutex
	outputMu       sync.RWMutex
	executionCount int
	executing      atomic.Bool
	liveStreamPath string
}

// New creates a new shared pi kernel.
func New(name, cwd string) (*Pi, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("tmux not found: %w", err)
	}
	piPath, err := exec.LookPath("pi")
	if err != nil {
		return nil, fmt.Errorf("pi not found: %w", err)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, _ = filepath.Abs(cwd)

	dataDir, err := kernelDataDir(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create kernel dir: %w", err)
	}

	// Extract the bridge extension.
	extPath := filepath.Join(dataDir, "rat-bridge.ts")
	if err := os.WriteFile(extPath, []byte(bridgeExtension), 0o600); err != nil {
		return nil, fmt.Errorf("write bridge extension: %w", err)
	}

	p := &Pi{
		name:        name,
		cwd:         cwd,
		tmuxPath:    tmuxPath,
		piPath:      piPath,
		sessionName: sessionName(name),
		dataDir:     dataDir,
		controlPath: filepath.Join(dataDir, "control.log"),
		currentID:   filepath.Join(dataDir, "current-id"),
		extPath:     extPath,
	}

	if err := p.ensureStarted(); err != nil {
		return nil, err
	}
	return p, nil
}

// SessionName returns the tmux session name for a pi kernel.
func SessionName(name string) string {
	return sessionName(name)
}

func sessionName(name string) string {
	clean := make([]rune, 0, len(name))
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			clean = append(clean, r)
		} else {
			clean = append(clean, '_')
		}
	}
	return "rat-" + string(clean)
}

// Attach attaches the current terminal to the pi tmux session.
func Attach(name string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	cmd := exec.Command(tmuxPath, "attach-session", "-t", SessionName(name))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── Kernel interface ────────────────────────────────────────

// Run sends a prompt to the shared pi session and waits for the result.
func (p *Pi) Run(code string) kernel.RunResult {
	start := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.ensureStarted(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), Duration: ms(start)}
	}

	p.executionCount++
	count := p.executionCount
	id := uniqueID()

	p.executing.Store(true)
	streamPath := filepath.Join(p.dataDir, id+".stream")
	_ = os.Remove(streamPath)
	p.setLiveStream(streamPath)
	defer p.executing.Store(false)
	defer p.clearLiveStream()

	// Write request ID so the bridge extension knows to capture this result.
	if err := os.WriteFile(p.currentID, []byte(id+"\n"), 0o600); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Show a dim hint in tmux so the human knows MCP is driving.
	_ = p.tmuxRun("display-message", "-d", "2000", "-t", p.target(), "rat> "+summarize(code))

	// Type the prompt into pi via tmux send-keys.
	if err := p.sendText(code); err != nil {
		_ = os.Remove(p.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}
	// Press Enter to submit.
	if err := p.tmuxRun("send-keys", "-t", p.target(), "Enter"); err != nil {
		_ = os.Remove(p.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Wait for the bridge extension to signal completion.
	if err := p.waitForControl(id, 10*time.Minute); err != nil {
		_ = os.Remove(p.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Read the structured result.
	resultPath := filepath.Join(p.dataDir, id+".result")
	data, err := os.ReadFile(resultPath)
	_ = os.Remove(resultPath)
	if err != nil {
		return kernel.RunResult{Success: false, Error: fmt.Sprintf("read result: %v", err), ExecCount: count, Duration: ms(start)}
	}

	var result struct {
		Text         string  `json:"text"`
		Model        string  `json:"model"`
		InputTokens  int     `json:"inputTokens"`
		OutputTokens int     `json:"outputTokens"`
		Cost         float64 `json:"cost"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return kernel.RunResult{Success: true, Output: string(data), ExecCount: count, Duration: ms(start)}
	}

	output := result.Text
	if result.Model != "" {
		usage := fmt.Sprintf("\n\n%s · %d→%d tokens", result.Model, result.InputTokens, result.OutputTokens)
		if result.Cost > 0 {
			usage += fmt.Sprintf(" · $%.4f", result.Cost)
		}
		output += usage
	}

	return kernel.RunResult{Success: true, Output: output, ExecCount: count, Duration: ms(start)}
}

func (p *Pi) SendInput(text string) error {
	return p.sendText(text)
}

func (p *Pi) IsWaitingForInput() bool {
	return false
}

// Look returns session info.
func (p *Pi) Look(req kernel.LookRequest) kernel.LookResult {
	if req.At != "" {
		return kernel.LookResult{Text: fmt.Sprintf("%s: pi sessions don't have inspectable variables", req.At)}
	}
	if req.Code != "" {
		return kernel.LookResult{Text: "No completions."}
	}

	state := "idle"
	if p.executing.Load() {
		state = "busy"
	}
	text := fmt.Sprintf("pi %s | %d prompts", state, p.executionCount)
	text += fmt.Sprintf("\n\nsession: %s", p.sessionName)
	text += fmt.Sprintf("\ncwd: %s", p.cwd)
	return kernel.LookResult{Text: text}
}

// Ctl controls the pi session.
func (p *Pi) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset", "restart":
		p.mu.Lock()
		defer p.mu.Unlock()
		p.killSession()
		p.executionCount = 0
		if err := p.ensureStarted(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		return kernel.CtlResult{Text: "RESTARTED | fresh pi session"}
	case "cancel":
		// Send Escape to cancel pi's current operation.
		_ = p.tmuxRun("send-keys", "-t", p.target(), "Escape")
		_ = os.Remove(p.currentID)
		return kernel.CtlResult{Text: "CANCELLED"}
	case "output":
		return kernel.CtlResult{Text: p.liveOutput()}
	case "status":
		state := "idle"
		if p.executing.Load() {
			state = "busy"
		}
		// Detect pi version.
		version := ""
		if out, err := exec.Command(p.piPath, "--version").Output(); err == nil {
			version = strings.TrimSpace(string(out))
		}
		if version != "" {
			state += "\nruntime_version: pi " + version
		}
		return kernel.CtlResult{Text: state}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'", op)}
	}
}

func (p *Pi) Shutdown() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = os.Remove(p.currentID)
	_ = os.Remove(p.controlPath)
	p.killSession()
	return nil
}

// ── internal ────────────────────────────────────────────────

func (p *Pi) ensureStarted() error {
	if p.hasSession() {
		return nil
	}
	return p.startSession()
}

func (p *Pi) startSession() error {
	_ = os.WriteFile(p.controlPath, nil, 0o600)
	_ = os.Remove(p.currentID)
	readyFile := filepath.Join(p.dataDir, "ready")
	_ = os.Remove(readyFile)

	// Start pi in tmux with the bridge extension.
	piCmd := fmt.Sprintf("RAT_CONTROL_DIR=%s %s --extension %s --session-dir %s",
		shellQuote(p.dataDir),
		shellQuote(p.piPath),
		shellQuote(p.extPath),
		shellQuote(filepath.Join(p.dataDir, "sessions")),
	)

	if err := p.tmuxRun("new-session", "-d", "-s", p.sessionName, "-c", p.cwd, piCmd); err != nil {
		return fmt.Errorf("start pi tmux session: %w", err)
	}

	// Configure the tmux status bar.
	p.configureUI()

	// Wait for pi to signal readiness.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyFile); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Pi might still be loading (extensions, etc). Don't fail — just warn.
	return nil
}

func (p *Pi) configureUI() {
	left := fmt.Sprintf("#[bold]rat pi#[nobold] #[fg=colour45](ratmux)#[default] | %s", p.name)
	right := "#[fg=colour10]shared session#[default] • Escape cancel • Ctrl+B d detach"
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status", "on")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-position", "bottom")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-interval", "1")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-style", "bg=colour235,fg=colour252")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "message-style", "bg=colour45,fg=colour16")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-left-length", "80")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-right-length", "100")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-left", left)
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "status-right", right)
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "window-status-format", "")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "window-status-current-format", "")
	_ = p.tmuxRun("set-option", "-t", p.sessionName, "window-status-separator", "")
}

func (p *Pi) killSession() {
	if p.hasSession() {
		_ = p.tmuxRun("kill-session", "-t", p.sessionName)
	}
}

func (p *Pi) hasSession() bool {
	return exec.Command(p.tmuxPath, "has-session", "-t", p.sessionName).Run() == nil
}

func (p *Pi) target() string {
	return p.sessionName + ":0.0"
}

func (p *Pi) sendText(text string) error {
	// Use tmux load-buffer + paste-buffer for reliable multi-line input.
	bufName := fmt.Sprintf("rat-%d", time.Now().UnixNano())
	cmd := exec.Command(p.tmuxPath, "load-buffer", "-b", bufName, "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %v: %s", err, strings.TrimSpace(string(out)))
	}
	defer p.tmuxRun("delete-buffer", "-b", bufName)
	return p.tmuxRun("paste-buffer", "-d", "-b", bufName, "-t", p.target())
}

func (p *Pi) setLiveStream(path string) {
	p.outputMu.Lock()
	defer p.outputMu.Unlock()
	p.liveStreamPath = path
}

func (p *Pi) clearLiveStream() {
	p.outputMu.Lock()
	defer p.outputMu.Unlock()
	p.liveStreamPath = ""
}

func (p *Pi) liveOutput() string {
	p.outputMu.RLock()
	path := p.liveStreamPath
	p.outputMu.RUnlock()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func (p *Pi) waitForControl(id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(p.controlPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read control: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) >= 1 && parts[0] == id {
				return nil
			}
		}
		if !p.hasSession() {
			return fmt.Errorf("pi session exited")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for pi response", timeout)
}

func (p *Pi) tmuxRun(args ...string) error {
	out, err := exec.Command(p.tmuxPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func kernelDataDir(name string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(dir, "rat", "kernels", name), nil
}

func uniqueID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func summarize(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 1 {
		return lines[0] + " …"
	}
	return s
}

func ms(start time.Time) int {
	return int(time.Since(start).Milliseconds())
}
