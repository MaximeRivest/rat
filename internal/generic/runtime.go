// Package generic implements a kernel driven by a runtime.yaml config file.
//
// Instead of writing Go code for each language, a runtime author provides
// a kernel script (any language) that speaks the rat kernel protocol
// (JSON lines over stdin/stdout) and a runtime.yaml that tells rat how
// to start it.
//
// The Go code here is language-agnostic — it spawns the kernel script,
// sends JSON requests, reads JSON responses, exactly like internal/python
// but without any Python-specific logic.
package generic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/procutil"
)

// RuntimeConfig is the runtime.yaml schema.
//
// A runtime can use one of two kernel types:
//
//   - json: a subprocess speaking the JSON kernel protocol over stdin/stdout
//   - tmux: an interactive REPL running in a tmux session, with a bridge
//     script that signals completion via control files
//
// And one of three frontend types:
//
//   - tmux: attach to the kernel's tmux session (for tmux kernels)
//   - native: a language-specific REPL hooked to route execution through MCP
//   - repl: a generic MCP-connected thin wrapper REPL (default fallback)
type RuntimeConfig struct {
	Name    string `yaml:"name"`    // e.g. "r", "julia", "pi"
	Display string `yaml:"display"` // e.g. "R", "Julia", "pi"

	Detect struct {
		Commands []string `yaml:"commands"` // binaries to search PATH for
		Env      string   `yaml:"env"`      // env var override (e.g. RAT_R)
	} `yaml:"detect"`

	Kernel struct {
		Type   string   `yaml:"type"`             // "json" (default) or "tmux"
		Script string   `yaml:"script,omitempty"` // json: kernel script path
		Args   []string `yaml:"args,omitempty"`   // json: extra args before script

		Command string `yaml:"command,omitempty"` // tmux: command to run in session
		Bridge  string `yaml:"bridge,omitempty"`  // tmux: bridge script (relative to runtime.yaml)
		Submit  string `yaml:"submit,omitempty"`  // tmux: key to submit input (default: Enter)
		Cancel  string `yaml:"cancel,omitempty"`  // tmux: key to cancel (default: C-c)
	} `yaml:"kernel"`

	Frontend struct {
		Type    string `yaml:"type,omitempty"`    // "tmux", "native", or "repl" (default)
		Command string `yaml:"command,omitempty"` // native: command template with {mcp_url}, {name}, etc.
		Prompt  string `yaml:"prompt,omitempty"`  // repl: prompt string (default: "lang> ")

		Fallback *FrontendFallback `yaml:"fallback,omitempty"` // fallback if native command not found
	} `yaml:"frontend"`

	Options map[string]RuntimeOption `yaml:"options,omitempty"`
	Install InstallConfig            `yaml:"install"`
}

// FrontendFallback defines what to use when the primary frontend isn't available.
type FrontendFallback struct {
	Type   string `yaml:"type,omitempty"`   // "repl" or "tmux"
	Prompt string `yaml:"prompt,omitempty"` // for repl type
}

// RuntimeOption describes a user-facing option supported by a runtime.
type RuntimeOption struct {
	Type        string   `yaml:"type,omitempty"`        // "string" (default) or "bool"
	Arg         string   `yaml:"arg,omitempty"`         // CLI flag to emit, e.g. --model
	Env         string   `yaml:"env,omitempty"`         // env var to set, e.g. AWS_PROFILE
	Enum        []string `yaml:"enum,omitempty"`        // allowed values for string options
	Description string   `yaml:"description,omitempty"` // help text
}

// InstallConfig defines how `rat install <lang>` should prepare a runtime.
type InstallConfig struct {
	CheckCommands []string     `yaml:"check_commands,omitempty"`
	CheckEnv      []string     `yaml:"check_env,omitempty"`
	Runtime       *InstallStep `yaml:"runtime,omitempty"`
	Frontend      *InstallStep `yaml:"frontend,omitempty"`
	Smoke         InstallSmoke `yaml:"smoke,omitempty"`
}

// InstallStep is one dependency-installation phase.
type InstallStep struct {
	Manager string   `yaml:"manager,omitempty"` // e.g. "pip", "r", "none"
	Deps    []string `yaml:"deps,omitempty"`
}

// InstallSmoke is the post-install smoke test.
type InstallSmoke struct {
	Run    string `yaml:"run,omitempty"`
	Expect string `yaml:"expect,omitempty"`
	Ctl    string `yaml:"ctl,omitempty"`
}

// KernelType returns the kernel type, defaulting to "json".
func (cfg *RuntimeConfig) KernelType() string {
	if cfg.Kernel.Type == "tmux" {
		return "tmux"
	}
	return "json"
}

// FrontendType returns the frontend type, defaulting based on kernel type.
func (cfg *RuntimeConfig) FrontendType() string {
	if cfg.Frontend.Type != "" {
		return cfg.Frontend.Type
	}
	if cfg.KernelType() == "tmux" {
		return "tmux"
	}
	return "repl"
}

// SubmitKey returns the tmux key for submitting input.
func (cfg *RuntimeConfig) SubmitKey() string {
	if cfg.Kernel.Submit != "" {
		return cfg.Kernel.Submit
	}
	return "Enter"
}

// CancelKey returns the tmux key for cancelling.
func (cfg *RuntimeConfig) CancelKey() string {
	if cfg.Kernel.Cancel != "" {
		return cfg.Kernel.Cancel
	}
	return "C-c"
}

// BridgePath returns the absolute path to the bridge script.
func (cfg *RuntimeConfig) BridgePath(configDir string) string {
	if cfg.Kernel.Bridge == "" {
		return ""
	}
	if filepath.IsAbs(cfg.Kernel.Bridge) {
		return cfg.Kernel.Bridge
	}
	return filepath.Join(configDir, cfg.Kernel.Bridge)
}

// RuntimeInstallStep returns the primary dependency-install step.
func (cfg *RuntimeConfig) RuntimeInstallStep() InstallStep {
	if cfg.Install.Runtime == nil {
		return InstallStep{}
	}
	return *cfg.Install.Runtime
}

// FrontendInstallStep returns the optional frontend dependency step.
func (cfg *RuntimeConfig) FrontendInstallStep() InstallStep {
	if cfg.Install.Frontend == nil {
		return InstallStep{}
	}
	return *cfg.Install.Frontend
}

// LoadConfig reads a runtime.yaml file.
func LoadConfig(path string) (*RuntimeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runtime config: %w", err)
	}
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse runtime config: %w", err)
	}
	return &cfg, nil
}

// DetectBinary finds the runtime binary using the config's detection rules.
// Returns the full path or an error.
func (cfg *RuntimeConfig) DetectBinary() (string, error) {
	// 1. Environment variable override
	if cfg.Detect.Env != "" {
		if v := os.Getenv(cfg.Detect.Env); v != "" {
			if _, err := os.Stat(v); err == nil {
				return v, nil
			}
		}
	}

	// 2. Search PATH for each candidate
	for _, cmd := range cfg.Detect.Commands {
		if path, err := exec.LookPath(cmd); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s not found (tried: %s)", cfg.Display, strings.Join(cfg.Detect.Commands, ", "))
}

// KernelScriptPath returns the absolute path to the kernel script,
// resolved relative to the directory containing runtime.yaml.
func (cfg *RuntimeConfig) KernelScriptPath(configDir string) string {
	if filepath.IsAbs(cfg.Kernel.Script) {
		return cfg.Kernel.Script
	}
	return filepath.Join(configDir, cfg.Kernel.Script)
}

// OptionArgs renders configured runtime options as CLI args.
func (cfg *RuntimeConfig) OptionArgs(options map[string]string) ([]string, error) {
	norm, err := cfg.NormalizeOptions(options)
	if err != nil {
		return nil, err
	}
	keys := sortedOptionKeys(norm)
	args := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		spec := cfg.Options[key]
		if spec.Arg == "" {
			continue
		}
		value := norm[key]
		if spec.optionType() == "bool" {
			if isTrue(value) {
				args = append(args, spec.Arg)
			}
			continue
		}
		args = append(args, spec.Arg, value)
	}
	return args, nil
}

// OptionEnv renders configured runtime options as env vars.
func (cfg *RuntimeConfig) OptionEnv(options map[string]string) (map[string]string, error) {
	norm, err := cfg.NormalizeOptions(options)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for key, value := range norm {
		spec := cfg.Options[key]
		if spec.Env != "" {
			env[spec.Env] = value
		}
	}
	return env, nil
}

// TmuxOptionString renders configured options for insertion into a shell command.
func (cfg *RuntimeConfig) TmuxOptionString(options map[string]string) (string, error) {
	args, err := cfg.OptionArgs(options)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " "), nil
}

// NormalizeOptions validates option names and values against runtime.yaml.
func (cfg *RuntimeConfig) NormalizeOptions(options map[string]string) (map[string]string, error) {
	if len(options) == 0 {
		return map[string]string{}, nil
	}
	norm := make(map[string]string, len(options))
	for key, value := range options {
		spec, ok := cfg.Options[key]
		if !ok {
			return nil, fmt.Errorf("unknown option %q for %s runtime", key, cfg.Name)
		}
		value = strings.TrimSpace(value)
		switch spec.optionType() {
		case "bool":
			if value == "" {
				value = "true"
			}
			if !isBool(value) {
				return nil, fmt.Errorf("option %q must be true or false", key)
			}
		default:
			if value == "" {
				return nil, fmt.Errorf("option %q cannot be empty", key)
			}
		}
		if len(spec.Enum) > 0 && !containsString(spec.Enum, value) {
			return nil, fmt.Errorf("option %q must be one of: %s", key, strings.Join(spec.Enum, ", "))
		}
		norm[key] = value
	}
	return norm, nil
}

func (o RuntimeOption) optionType() string {
	if o.Type == "bool" {
		return "bool"
	}
	return "string"
}

func sortedOptionKeys(options map[string]string) []string {
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isBool(value string) bool {
	switch strings.ToLower(value) {
	case "1", "0", "true", "false", "yes", "no", "on", "off":
		return true
	default:
		return false
	}
}

func isTrue(value string) bool {
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ── Generic Kernel ──────────────────────────────────────────

// request/response match the kernel protocol JSON schema.
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
	State   string `json:"state,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Vars    int    `json:"vars,omitempty"`
}

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

// Event is a kernel-initiated notification (pushed, not requested).
type Event struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data,omitempty"`
}

// Kernel is a language-agnostic kernel driven by a runtime.yaml config.
// It implements kernel.Kernel.
//
// I/O model: a background goroutine reads stdout continuously. Lines with
// op "event" are dispatched to the event handler immediately. All other
// lines (responses, streaming) go to a channel consumed by the active
// request (Run, Look, Ctl). This lets the kernel push events at any time
// — between requests, during execution, or while idle.
type Kernel struct {
	name    string
	display string

	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stderrBuf      bytes.Buffer
	executionCount int
	executing      atomic.Bool

	// Background reader routes stdout lines here.
	responseCh chan []byte   // non-event messages (responses, streaming)
	readerDone chan struct{} // closed when reader goroutine exits
	readerErr  error         // set by reader goroutine before closing readerDone

	// How to start the subprocess.
	binaryPath   string
	binaryArgs   []string // args before the script
	scriptPath   string
	cwd          string
	extraEnv     map[string]string
	activityPath string // path to activity.jsonl for frontend sharing
}

// New creates a generic kernel from a runtime config.
// If runtimePath is non-empty, it overrides auto-detection.
func New(name, cwd string, cfg *RuntimeConfig, configDir string, runtimePath string, options map[string]string) (*Kernel, error) {
	binary := runtimePath
	if binary == "" {
		var err error
		binary, err = cfg.DetectBinary()
		if err != nil {
			return nil, err
		}
	}

	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, _ = filepath.Abs(cwd)

	scriptPath := cfg.KernelScriptPath(configDir)
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("kernel script not found: %s", scriptPath)
	}

	binaryArgs, err := cfg.OptionArgs(options)
	if err != nil {
		return nil, err
	}
	extraEnv, err := cfg.OptionEnv(options)
	if err != nil {
		return nil, err
	}

	// Activity log lives in the canonical cache dir.
	kdir, err := cachedir.Kernels(name)
	if err != nil {
		return nil, fmt.Errorf("resolve cache dir: %w", err)
	}
	activityPath := filepath.Join(kdir, "activity.jsonl")
	_ = os.Remove(activityPath)

	extraEnv["RAT_ACTIVITY_LOG"] = activityPath

	k := &Kernel{
		name:         name,
		display:      cfg.Display,
		binaryPath:   binary,
		binaryArgs:   append(append([]string{}, cfg.Kernel.Args...), binaryArgs...),
		extraEnv:     extraEnv,
		activityPath: activityPath,
		scriptPath:   scriptPath,
		cwd:          cwd,
	}

	if err := k.ensureStarted(); err != nil {
		return nil, err
	}
	return k, nil
}

// Run executes code in the kernel subprocess.
func (k *Kernel) Run(code string) kernel.RunResult {
	start := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.ensureStarted(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), Duration: ms(start)}
	}

	k.executionCount++
	count := k.executionCount
	k.executing.Store(true)
	defer k.executing.Store(false)

	if err := k.send(request{Op: "run", Code: code}); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}

	// Read responses, skipping streaming messages.
	// Events are handled by the background reader — they never arrive here.
	var resp response
	for {
		var err error
		resp, err = k.readResponse(5 * time.Minute)
		if err != nil {
			return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
		}
		switch resp.Op {
		case "output_chunk", "input_request", "input_delivered":
			continue
		}
		break
	}

	output := strings.TrimSpace(resp.Output)
	if !resp.Success {
		errText := strings.TrimSpace(resp.Error)
		if output != "" {
			errText = strings.TrimSpace(output + "\n" + errText)
		}
		if errText == "" {
			errText = "execution failed"
		}
		r := kernel.RunResult{Success: false, Output: output, Error: errText, ExecCount: count, Duration: ms(start), Vars: resp.Vars}
		k.logActivity(code, r)
		return r
	}
	r := kernel.RunResult{Success: true, Output: output, ExecCount: count, Duration: ms(start), Vars: resp.Vars}
	k.logActivity(code, r)
	return r
}

// ActivityLogPath returns the path to the activity log for this kernel.
func (k *Kernel) ActivityLogPath() string {
	return k.activityPath
}

// logActivity appends an execution record to the activity log so
// frontends can see what other MCP clients executed.
func (k *Kernel) logActivity(code string, r kernel.RunResult) {
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SendInput writes text to a waiting input prompt.
func (k *Kernel) SendInput(text string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.send(request{Op: "input", Text: text})
}

// IsWaitingForInput returns whether the kernel is blocked on stdin.
func (k *Kernel) IsWaitingForInput() bool {
	return false // generic kernel doesn't track this yet
}

// Look inspects the runtime state.
func (k *Kernel) Look(req kernel.LookRequest) kernel.LookResult {
	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.ensureStarted(); err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	var err error
	switch {
	case req.Code != "":
		err = k.send(request{Op: "complete", Code: req.Code, Cursor: req.Cursor})
	case req.At != "":
		err = k.send(request{Op: "look_at", At: req.At})
	default:
		err = k.send(request{Op: "look_overview"})
	}
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	resp, err := k.readResponse(30 * time.Second)
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	if resp.Text != "" {
		return kernel.LookResult{Text: resp.Text}
	}
	if resp.Error != "" {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %s", resp.Error)}
	}
	return kernel.LookResult{Text: ""}
}

// Ctl controls the runtime.
func (k *Kernel) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset", "restart":
		k.mu.Lock()
		defer k.mu.Unlock()
		k.kill()
		k.executionCount = 0
		// Clear activity log so frontends don't show stale entries.
		if k.activityPath != "" {
			os.Remove(k.activityPath)
		}
		if err := k.ensureStarted(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		if op == "reset" {
			return kernel.CtlResult{Text: "RESET | namespace cleared | 0 vars"}
		}
		return kernel.CtlResult{Text: "RESTARTED | fresh session"}
	case "cancel":
		if k.cmd != nil && k.cmd.Process != nil {
			_ = k.cmd.Process.Kill()
		}
		return kernel.CtlResult{Text: "CANCELLED"}
	case "output":
		// No streaming output buffer for generic kernels.
		return kernel.CtlResult{Text: ""}
	case "status":
		if k.executing.Load() {
			return kernel.CtlResult{Text: "busy"}
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		if err := k.ensureStarted(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		if err := k.send(request{Op: "status"}); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		resp, err := k.readResponse(5 * time.Second)
		if err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		if resp.Text != "" {
			return kernel.CtlResult{Text: resp.Text}
		}
		if resp.State != "" {
			return kernel.CtlResult{Text: resp.State}
		}
		if resp.Error != "" {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %s", resp.Error)}
		}
		return kernel.CtlResult{Text: "idle"}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'", op)}
	}
}

// Shutdown tears down the kernel subprocess.
func (k *Kernel) Shutdown() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	_ = k.send(request{Op: "shutdown"})
	k.kill()
	return nil
}

// ── internal ────────────────────────────────────────────────

func (k *Kernel) ensureStarted() error {
	if k.cmd != nil && k.cmd.Process != nil && k.cmd.ProcessState == nil {
		return nil
	}

	args := append(append([]string{}, k.binaryArgs...), k.scriptPath)
	cmd := exec.Command(k.binaryPath, args...)
	cmd.Dir = k.cwd
	cmd.Env = append([]string{}, os.Environ()...)
	procutil.HideWindow(cmd)
	for key, value := range k.extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stderr: %w", err)
	}

	k.stderrBuf.Reset()
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start %s kernel: %w", k.display, err)
	}
	go func() { _, _ = io.Copy(&k.stderrBuf, stderr) }()

	k.cmd = cmd
	k.stdin = stdin
	k.responseCh = make(chan []byte, 32)
	k.readerDone = make(chan struct{})
	k.readerErr = nil

	// Start background reader that routes stdout lines.
	go k.readerLoop(bufio.NewReader(stdout))

	// Ping to verify the kernel is alive.
	if err := k.send(request{Op: "ping"}); err != nil {
		k.kill()
		return err
	}
	resp, err := k.readResponse(10 * time.Second)
	if err != nil {
		k.kill()
		return err
	}
	if !resp.OK {
		k.kill()
		return fmt.Errorf("%s kernel failed to initialize: %s", k.display, resp.Error)
	}
	return nil
}

// readerLoop runs in a background goroutine. It reads every line from
// the kernel's stdout and routes it:
//   - op "event" → dispatched immediately (activity log + callback)
//   - everything else → responseCh for the active request to consume
func (k *Kernel) readerLoop(reader *bufio.Reader) {
	defer close(k.readerDone)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			k.readerErr = err
			return
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// Peek at op to decide where to route.
		var peek struct {
			Op string `json:"op"`
		}
		if json.Unmarshal(line, &peek) != nil {
			continue
		}

		if peek.Op == "event" {
			k.dispatchEvent(line)
		} else {
			// Copy because bufio may reuse the buffer.
			msg := make([]byte, len(line))
			copy(msg, line)
			select {
			case k.responseCh <- msg:
			case <-time.After(30 * time.Second):
				// Response channel full for 30s — something is very wrong.
				// Drop the message to avoid blocking the reader forever.
			}
		}
	}
}

// dispatchEvent handles an event line from the kernel.
func (k *Kernel) dispatchEvent(raw []byte) {
	var evt struct {
		Op   string                 `json:"op"`
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(raw, &evt) != nil {
		return
	}
	k.logEvent(evt.Type, evt.Data)
}

// logEvent writes an event to the activity log so REPL frontends see it.
func (k *Kernel) logEvent(evtType string, data map[string]interface{}) {
	if k.activityPath == "" {
		return
	}
	entry := map[string]interface{}{
		"event": evtType,
		"data":  data,
		"t":     time.Now().Unix(),
	}
	jsonData, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(k.activityPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(jsonData, '\n'))
}

func (k *Kernel) send(req request) error {
	if k.stdin == nil {
		return fmt.Errorf("%s kernel not started", k.display)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := k.stdin.Write(append(data, '\n')); err != nil {
		k.kill()
		return fmt.Errorf("write to %s kernel: %w", k.display, err)
	}
	return nil
}

// readResponse waits for the next non-event response from the kernel.
// Events arriving while waiting are dispatched automatically by the
// background reader — they never appear here.
func (k *Kernel) readResponse(timeout time.Duration) (response, error) {
	select {
	case raw, ok := <-k.responseCh:
		if !ok {
			stderr := strings.TrimSpace(k.stderrBuf.String())
			if stderr != "" {
				return response{}, fmt.Errorf("%s kernel exited: %s", k.display, stderr)
			}
			return response{}, fmt.Errorf("%s kernel exited: %v", k.display, k.readerErr)
		}
		var resp response
		if err := json.Unmarshal(raw, &resp); err != nil {
			return response{}, fmt.Errorf("decode %s kernel response: %w", k.display, err)
		}
		return resp, nil

	case <-k.readerDone:
		// Reader exited — drain any remaining responses.
		select {
		case raw := <-k.responseCh:
			var resp response
			if err := json.Unmarshal(raw, &resp); err != nil {
				return response{}, fmt.Errorf("decode %s kernel response: %w", k.display, err)
			}
			return resp, nil
		default:
		}
		stderr := strings.TrimSpace(k.stderrBuf.String())
		if stderr != "" {
			return response{}, fmt.Errorf("%s kernel exited: %s", k.display, stderr)
		}
		return response{}, fmt.Errorf("%s kernel exited: %v", k.display, k.readerErr)

	case <-time.After(timeout):
		return response{}, fmt.Errorf("%s kernel: timeout after %s", k.display, timeout)
	}
}

func (k *Kernel) kill() {
	if k.stdin != nil {
		_ = k.stdin.Close()
		k.stdin = nil
	}
	if k.cmd != nil {
		if k.cmd.Process != nil {
			_ = k.cmd.Process.Kill()
		}
		_ = k.cmd.Wait()
		k.cmd = nil
	}
	// Wait for reader goroutine to finish.
	if k.readerDone != nil {
		select {
		case <-k.readerDone:
		case <-time.After(2 * time.Second):
		}
		k.readerDone = nil
	}
	// Drain response channel.
	if k.responseCh != nil {
		for {
			select {
			case <-k.responseCh:
			default:
				goto drained
			}
		}
	drained:
		k.responseCh = nil
	}
}

func ms(start time.Time) int {
	return int(time.Since(start).Milliseconds())
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
