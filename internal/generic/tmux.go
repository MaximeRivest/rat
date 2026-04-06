package generic

// TmuxKernel is a language-agnostic tmux-based kernel driven by runtime.yaml.
//
// Like the JSON Kernel, this is config-driven. The runtime author provides:
//   - A command to run in tmux (the REPL itself)
//   - A bridge script that signals completion via control files
//
// The bridge contract (same for all tmux kernels):
//   RAT_CONTROL_DIR is set in the environment. The bridge must:
//   - On ready:      write to {RAT_CONTROL_DIR}/ready
//   - On completion: read current-id, write {id}.result, append to control.log
//
// This is how bash's PROMPT_COMMAND and pi's extension both work —
// same control protocol, different hook mechanism.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/tmuxutil"
)

// TmuxKernel implements kernel.Kernel for tmux-based runtimes.
type TmuxKernel struct {
	name    string
	display string
	cwd     string

	tmuxPath    string
	sessionName string
	dataDir     string
	controlPath string
	currentID   string

	command   string // command template to run in tmux
	submitKey string // tmux key to submit input
	cancelKey string // tmux key to cancel

	mu             sync.Mutex
	executionCount int
	executing      atomic.Bool
	activityPath   string
}

// TmuxSessionName returns the tmux session name for a kernel.
func TmuxSessionName(name string) string {
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

// TmuxAttach attaches the current terminal to a tmux kernel session.
func TmuxAttach(name string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	cmd := exec.Command(tmuxPath, "attach-session", "-t", TmuxSessionName(name))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// NewTmux creates a tmux-based kernel from a runtime config.
func NewTmux(name, cwd string, cfg *RuntimeConfig, configDir string, runtimePath string, options map[string]string) (*TmuxKernel, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("tmux not found: %w", err)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, _ = filepath.Abs(cwd)

	dataDir, err := tmuxDataDir(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create kernel dir: %w", err)
	}

	// Activity log for frontend cross-client visibility.
	activityPath := filepath.Join(dataDir, "activity.jsonl")
	_ = os.Remove(activityPath)

	// Resolve the bridge script path.
	bridgePath := cfg.BridgePath(configDir)
	if bridgePath != "" {
		if _, err := os.Stat(bridgePath); err != nil {
			return nil, fmt.Errorf("bridge script not found: %s", bridgePath)
		}
	}

	runtimeBinary := runtimePath
	if runtimeBinary == "" {
		runtimeBinary, err = cfg.DetectBinary()
		if err != nil {
			return nil, err
		}
	}

	optionString, err := cfg.TmuxOptionString(options)
	if err != nil {
		return nil, err
	}
	optionEnv, err := cfg.OptionEnv(options)
	if err != nil {
		return nil, err
	}

	// Expand template variables in the command.
	command := cfg.Kernel.Command
	if optionString != "" && !strings.Contains(command, "{opts}") {
		return nil, fmt.Errorf("runtime %q defines arg-mapped options but kernel.command is missing {opts}", cfg.Name)
	}
	command = strings.ReplaceAll(command, "{runtime}", shellQuote(runtimeBinary))
	command = strings.ReplaceAll(command, "{opts}", optionString)
	command = strings.ReplaceAll(command, "{bridge}", shellQuote(bridgePath))
	command = strings.ReplaceAll(command, "{data_dir}", shellQuote(dataDir))
	command = strings.ReplaceAll(command, "{config_dir}", shellQuote(configDir))
	command = strings.ReplaceAll(command, "{cwd}", shellQuote(cwd))
	command = strings.ReplaceAll(command, "{name}", name)
	if len(optionEnv) > 0 {
		var envParts []string
		for _, key := range sortedOptionKeys(optionEnv) {
			envParts = append(envParts, key+"="+shellQuote(optionEnv[key]))
		}
		command = strings.Join(envParts, " ") + " " + command
	}

	k := &TmuxKernel{
		name:         name,
		display:      cfg.Display,
		cwd:          cwd,
		tmuxPath:     tmuxPath,
		sessionName:  TmuxSessionName(name),
		dataDir:      dataDir,
		controlPath:  filepath.Join(dataDir, "control.log"),
		currentID:    filepath.Join(dataDir, "current-id"),
		command:      command,
		submitKey:    cfg.SubmitKey(),
		cancelKey:    cfg.CancelKey(),
		activityPath: activityPath,
	}

	if err := k.ensureStarted(); err != nil {
		return nil, err
	}
	return k, nil
}

// ActivityLogPath returns the path to the activity log.
func (k *TmuxKernel) ActivityLogPath() string {
	return k.activityPath
}

// ── Kernel interface ────────────────────────────────────────

func (k *TmuxKernel) Run(code string) kernel.RunResult {
	start := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.ensureStarted(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), Duration: ms(start)}
	}

	k.executionCount++
	count := k.executionCount
	id := uniqueID()

	k.executing.Store(true)
	defer k.executing.Store(false)

	// Write request ID so the bridge knows to capture this result.
	if err := os.WriteFile(k.currentID, []byte(id+"\n"), 0o600); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Show a dim hint in tmux.
	_ = k.tmuxRun("display-message", "-d", "2000", "-t", k.target(), "rat> "+tmuxSummarize(code))

	// Type the input via tmux send-keys.
	if err := k.sendText(code); err != nil {
		_ = os.Remove(k.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}
	// Submit.
	if err := k.tmuxRun("send-keys", "-t", k.target(), k.submitKey); err != nil {
		_ = os.Remove(k.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Wait for bridge to signal completion.
	if err := k.waitForControl(id, 10*time.Minute); err != nil {
		_ = os.Remove(k.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Read the result file.
	resultPath := filepath.Join(k.dataDir, id+".result")
	data, err := os.ReadFile(resultPath)
	_ = os.Remove(resultPath)

	var output string
	if err == nil {
		// Try structured JSON result first.
		var result struct {
			Text         string  `json:"text"`
			Model        string  `json:"model"`
			InputTokens  int     `json:"inputTokens"`
			OutputTokens int     `json:"outputTokens"`
			Cost         float64 `json:"cost"`
		}
		if jsonErr := json.Unmarshal(data, &result); jsonErr == nil && result.Text != "" {
			output = result.Text
			if result.Model != "" {
				output += fmt.Sprintf("\n\n%s · %d→%d tokens", result.Model, result.InputTokens, result.OutputTokens)
				if result.Cost > 0 {
					output += fmt.Sprintf(" · $%.4f", result.Cost)
				}
			}
		} else {
			// Plain text result.
			output = strings.TrimSpace(string(data))
		}
	}

	r := kernel.RunResult{Success: true, Output: output, ExecCount: count, Duration: ms(start)}
	k.logActivity(code, r)
	return r
}

func (k *TmuxKernel) SendInput(text string) error {
	return k.sendText(text)
}

func (k *TmuxKernel) IsWaitingForInput() bool {
	return false
}

func (k *TmuxKernel) Look(req kernel.LookRequest) kernel.LookResult {
	if req.At != "" {
		return kernel.LookResult{Text: fmt.Sprintf("%s: not inspectable in %s", req.At, k.display)}
	}
	if req.Code != "" {
		return kernel.LookResult{Text: "No completions."}
	}
	state := "idle"
	if k.executing.Load() {
		state = "busy"
	}
	return kernel.LookResult{Text: fmt.Sprintf("%s %s | %d executions\n\nsession: %s\ncwd: %s",
		k.display, state, k.executionCount, k.sessionName, k.cwd)}
}

func (k *TmuxKernel) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset", "restart":
		k.mu.Lock()
		defer k.mu.Unlock()
		k.killSession()
		k.executionCount = 0
		if err := k.ensureStarted(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		return kernel.CtlResult{Text: fmt.Sprintf("RESTARTED | fresh %s session", k.display)}
	case "cancel":
		_ = k.tmuxRun("send-keys", "-t", k.target(), k.cancelKey)
		_ = os.Remove(k.currentID)
		return kernel.CtlResult{Text: "CANCELLED"}
	case "output":
		return kernel.CtlResult{Text: ""}
	case "status":
		state := "idle"
		if k.executing.Load() {
			state = "busy"
		}
		return kernel.CtlResult{Text: state}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'", op)}
	}
}

func (k *TmuxKernel) Shutdown() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	_ = os.Remove(k.currentID)
	_ = os.Remove(k.controlPath)
	k.killSession()
	return nil
}

// ── internal ────────────────────────────────────────────────

func (k *TmuxKernel) ensureStarted() error {
	if k.hasSession() {
		return nil
	}
	return k.startSession()
}

func (k *TmuxKernel) startSession() error {
	_ = os.WriteFile(k.controlPath, nil, 0o600)
	_ = os.Remove(k.currentID)
	readyFile := filepath.Join(k.dataDir, "ready")
	_ = os.Remove(readyFile)

	// Prepend RAT_CONTROL_DIR to the command.
	cmd := fmt.Sprintf("RAT_CONTROL_DIR=%s %s", shellQuote(k.dataDir), k.command)

	if err := k.tmuxRun("new-session", "-d", "-s", k.sessionName, "-c", k.cwd, cmd); err != nil {
		return fmt.Errorf("start %s tmux session: %w", k.display, err)
	}

	k.configureUI()

	// Wait for ready signal.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyFile); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func (k *TmuxKernel) configureUI() {
	tmuxutil.ConfigureSession(tmuxutil.SessionConfig{
		TmuxPath:    k.tmuxPath,
		SessionName: k.sessionName,
		Display:     k.display,
		Name:        k.name,
		CancelKey:   k.cancelKey,
	})
}

func (k *TmuxKernel) killSession() {
	if k.hasSession() {
		_ = k.tmuxRun("kill-session", "-t", k.sessionName)
	}
}

func (k *TmuxKernel) hasSession() bool {
	return exec.Command(k.tmuxPath, "has-session", "-t", k.sessionName).Run() == nil
}

func (k *TmuxKernel) target() string {
	return k.sessionName + ":0.0"
}

func (k *TmuxKernel) sendText(text string) error {
	bufName := fmt.Sprintf("rat-%d", time.Now().UnixNano())
	cmd := exec.Command(k.tmuxPath, "load-buffer", "-b", bufName, "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %v: %s", err, strings.TrimSpace(string(out)))
	}
	defer k.tmuxRun("delete-buffer", "-b", bufName)
	return k.tmuxRun("paste-buffer", "-d", "-b", bufName, "-t", k.target())
}

func (k *TmuxKernel) waitForControl(id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(k.controlPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read control: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) >= 1 && parts[0] == id {
				return nil
			}
		}
		if !k.hasSession() {
			return fmt.Errorf("%s session exited", k.display)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for %s", timeout, k.display)
}

func (k *TmuxKernel) tmuxRun(args ...string) error {
	out, err := exec.Command(k.tmuxPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (k *TmuxKernel) logActivity(code string, r kernel.RunResult) {
	if k.activityPath == "" {
		return
	}
	e := activityEntry{
		N:      r.ExecCount,
		Code:   truncate(code, 500),
		Output: truncate(r.Output, 500),
		OK:     r.Success,
		Time:   time.Now().Unix(),
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(k.activityPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

func tmuxDataDir(name string) (string, error) {
	return cachedir.Kernels(name)
}

func tmuxSummarize(s string) string {
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
