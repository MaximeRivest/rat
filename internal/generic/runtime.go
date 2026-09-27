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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
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

		// json: how rat and the kernel talk — "stdio" (default: requests
		// on stdin, replies on stdout) or "socket" (a private connection;
		// stdout/stderr are then the user's output, streamed live).
		Transport string `yaml:"transport,omitempty"`
		// json: what cancel does — "kill" (default: the process stops,
		// variables are lost), "signal" (SIGINT to the kernel's process
		// group; the kernel stops the running code and stays up) or
		// "message" ({"op":"interrupt"} on the protocol connection, read by
		// the kernel while it runs code).
		Interrupt string `yaml:"interrupt,omitempty"`
		// json: the same on Windows, where there are no signals: "message"
		// or "kill" (default).
		InterruptWindows string `yaml:"interrupt_windows,omitempty"`

		// jupyter: the kernelspec to run (kernel.json in Jupyter's kernel
		// folders), and code whose printed output is the variable overview.
		Kernelspec string `yaml:"kernelspec,omitempty"`
		Overview   string `yaml:"overview,omitempty"`

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

	// Packages a notebook declares for this runtime (rat.<key>.dependencies),
	// checked and installed by a script of the runtime's own language —
	// see internal/notebook/runtimeenv.go for the contract.
	Packages struct {
		Key    string   `yaml:"key,omitempty"`    // front-matter key: rat.<key>.dependencies
		Script string   `yaml:"script,omitempty"` // relative to runtime.yaml
		Args   []string `yaml:"args,omitempty"`   // before the script
	} `yaml:"packages,omitempty"`
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
	switch cfg.Kernel.Type {
	case "tmux", "jupyter":
		return cfg.Kernel.Type
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
	ID         int64  `json:"id,omitempty"` // echoed in the reply (kernels that do: see KERNEL-PROTOCOL.md)
	Op         string `json:"op"`
	Code       string `json:"code,omitempty"`
	At         string `json:"at,omitempty"`
	Cursor     *int   `json:"cursor,omitempty"`
	Text       string `json:"text,omitempty"`
	Full       bool   `json:"full,omitempty"`
	AllowStdin bool   `json:"allow_stdin,omitempty"`
	Token      string `json:"token,omitempty"`
}

type response struct {
	ID      int64  `json:"id,omitempty"`
	Op      string `json:"op,omitempty"`
	Success bool   `json:"success,omitempty"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Text    string `json:"text,omitempty"`
	State   string `json:"state,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Vars    int    `json:"vars,omitempty"`
	Prompt  string `json:"prompt,omitempty"` // input_request: what the program asked
	Secret  bool   `json:"secret,omitempty"` // input_request: a password-style read
	Token   string `json:"token,omitempty"`  // protocol_hello

	// complete: where the replaced text starts, and the exact matches.
	Start   *int           `json:"start,omitempty"`
	Matches []kernel.Match `json:"matches,omitempty"`

	hasSuccess bool // the message carried a "success" field (a run's reply does)
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

// Transports between rat and a json kernel.
const (
	// TransportStdio: requests on the kernel's stdin, replies on its
	// stdout. Simple, but whatever the user's code prints must be kept
	// off stdout by the kernel, and the code cannot read stdin.
	TransportStdio = "stdio"
	// TransportSocket: requests and replies on a private TCP connection
	// the kernel opens to rat (RAT_PROTOCOL_TCP_ADDR, first line
	// {"op":"protocol_hello","token":RAT_PROTOCOL_TOKEN}). The kernel's
	// stdout and stderr are the user's output, streamed live as the
	// Python kernel's are; its stdin is empty.
	TransportSocket = "socket"
)

// Interrupt modes: what `cancel` does to a running request.
const (
	// InterruptKill stops the kernel process: every variable is lost.
	// The default, for kernels that cannot survive a signal.
	InterruptKill = "kill"
	// InterruptSignal sends SIGINT to the kernel's process group, as
	// Ctrl-C in a terminal would; the kernel stops the running code and
	// replies. Needs the kernel to catch it (R: tryCatch(interrupt=)).
	InterruptSignal = "signal"
	// InterruptMessage sends {"op":"interrupt"} on the protocol
	// connection: the kernel reads it while code runs (a reader thread)
	// and interrupts that code. Works where signals do not (Windows).
	InterruptMessage = "message"
)

// Transport returns the configured transport, defaulting to stdio.
func (cfg *RuntimeConfig) Transport() string {
	if cfg.Kernel.Transport == TransportSocket {
		return TransportSocket
	}
	return TransportStdio
}

// InterruptMode returns the interrupt mode on this operating system,
// defaulting to kill.
func (cfg *RuntimeConfig) InterruptMode() string {
	mode := cfg.Kernel.Interrupt
	if goruntime.GOOS == "windows" {
		mode = cfg.Kernel.InterruptWindows
		if mode == InterruptSignal {
			mode = InterruptKill
		}
	}
	switch mode {
	case InterruptSignal, InterruptMessage:
		return mode
	}
	return InterruptKill
}

// How long a kernel may take to start and answer its first ping. Long on
// purpose: a first start may compile packages (Julia precompiles for
// minutes). A kernel that dies while starting is noticed at once.
const startTimeout = 5 * time.Minute

// How long after a first cancel a second one stops the kernel instead of
// interrupting again (interrupt: signal).
var forceAfter = 3 * time.Second

// How long look (variables, completion) waits for its reply. The reply
// is still taken off the stream when it comes (see Kernel.stale).
var lookTimeout = 30 * time.Second

// link is the protocol reader of one kernel process.
type link struct {
	ch   chan []byte   // non-event messages (replies, streaming)
	done chan struct{} // closed when the reader exits
	quit chan struct{} // closed by kill: stop delivering
	err  error         // why the reader exited; read after done
}

// outputBuf is a lock-protected text buffer, read while it is written.
type outputBuf struct {
	mu  sync.Mutex
	buf strings.Builder
	max int // keep at most the last max bytes (0: no limit)
}

func (b *outputBuf) Append(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(s)
	if b.max > 0 && b.buf.Len() > b.max {
		tail := b.buf.String()[b.buf.Len()-b.max:]
		b.buf.Reset()
		b.buf.WriteString(tail)
	}
}

func (b *outputBuf) Get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *outputBuf) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *outputBuf) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// Kernel is a language-agnostic kernel driven by a runtime.yaml config.
// It implements kernel.Kernel.
//
// I/O model: a background goroutine reads the protocol stream
// continuously. Lines with op "event" are dispatched to the event handler
// immediately. All other lines (replies, streaming) go to a channel
// consumed by the active request (Run, Look, Ctl). This lets the kernel
// push events at any time — between requests, during execution, or while
// idle.
//
// Requests are answered strictly in order and carry no id. A request
// that stops waiting (Look after 30 s) still gets its reply later; the
// kernel owes it, and it is dropped when it comes (stale), so the next
// request never reads an answer meant for another.
type Kernel struct {
	name    string
	display string

	mu      sync.Mutex // one request at a time
	writeMu sync.Mutex // one writer on the protocol stream (SendInput runs during Run)

	cmd        *exec.Cmd
	proto      io.WriteCloser
	link       *link         // the protocol reader of the running process
	outputDone chan struct{} // closed when the kernel's stdout/stderr are both closed
	stale      int           // replies owed to requests that stopped waiting
	nextID     int64         // id of the last request sent
	currentID  int64         // id of the request whose reply is awaited

	procMu sync.Mutex
	proc   *os.Process // for cancel, which must not wait for k.mu

	executionCount  int
	executing       atomic.Bool
	pending         atomic.Bool  // a request is waiting for its reply
	interruptedAt   atomic.Int64 // when cancel first interrupted the pending request (unix ns; 0: not yet)
	waitingForInput atomic.Bool
	inputPrompt     atomic.Value // kernel.InputPrompt of the read in progress
	inputSeq        atomic.Uint64

	partial    outputBuf // live output of the run in progress (Ctl "output")
	userOutput outputBuf // what the program printed during the run (socket transport)
	diag       outputBuf // what the kernel printed outside runs, for error messages

	// How to start the subprocess.
	binaryPath   string
	binaryArgs   []string // args before the script
	scriptPath   string
	cwd          string
	extraEnv     map[string]string
	activityPath string // path to activity.jsonl for frontend sharing
	transport    string
	interrupt    string
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
		transport:    cfg.Transport(),
		interrupt:    cfg.InterruptMode(),
		diag:         outputBuf{max: 64 << 10},
	}

	if err := k.ensureStarted(); err != nil {
		return nil, err
	}
	return k, nil
}

// Run executes code in the kernel subprocess. It waits as long as the
// code runs: stopping it is cancel's job, not a clock's.
func (k *Kernel) Run(code string) kernel.RunResult {
	start := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.ensureStarted(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: k.executionCount, Duration: ms(start)}
	}

	k.executionCount++
	count := k.executionCount
	k.partial.Reset()
	k.userOutput.Reset()
	k.executing.Store(true)
	defer func() {
		k.waitingForInput.Store(false)
		k.executing.Store(false)
	}()

	if err := k.send(request{Op: "run", Code: code, AllowStdin: true}); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count, Duration: ms(start)}
	}
	resp, err := k.await(0, true)
	if k.transport == TransportSocket {
		k.waitForOutputQuiet()
	}
	output := strings.TrimSpace(resp.Output)
	if k.transport == TransportSocket {
		output = strings.TrimSpace(joinOutput(resp.Output, k.userOutput.Get()))
	}
	if err != nil {
		errText := err.Error()
		if output != "" {
			errText = output + "\n" + errText
		}
		r := kernel.RunResult{Success: false, Output: output, Error: errText, ExecCount: count, Duration: ms(start)}
		k.logActivity(code, r)
		return r
	}

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

// SendInput delivers the answer to a prompt the running code waits on.
// It does not take k.mu: the run holding it is the one waiting.
func (k *Kernel) SendInput(text string) error {
	return k.send(request{Op: "input", Text: text})
}

// IsWaitingForInput returns whether the running code is blocked on a read.
func (k *Kernel) IsWaitingForInput() bool {
	return k.waitingForInput.Load()
}

// InputPrompt returns the prompt of the read the code is blocked on.
func (k *Kernel) InputPrompt() kernel.InputPrompt {
	if v, ok := k.inputPrompt.Load().(kernel.InputPrompt); ok {
		return v
	}
	return kernel.InputPrompt{}
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
		cursor := req.Cursor
		err = k.send(request{Op: "complete", Code: req.Code, Cursor: &cursor})
	case req.At != "":
		err = k.send(request{Op: "look_at", At: req.At, Full: req.Full})
	default:
		err = k.send(request{Op: "look_overview"})
	}
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	resp, err := k.await(lookTimeout, false)
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	if req.Code != "" && resp.Start != nil && resp.Error == "" {
		return kernel.LookResult{Text: resp.Text, Completion: &kernel.Completion{Start: *resp.Start, Matches: resp.Matches}}
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
		// Stop the process first, without waiting for k.mu: a run that
		// never ends holds it, and restarting is how one gets out.
		k.killProcess()
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
		// Nothing to stop when no request waits: a kernel between
		// requests keeps its variables.
		if !k.pending.Load() {
			return kernel.CtlResult{Text: "CANCELLED"}
		}
		if k.interrupt == InterruptSignal || k.interrupt == InterruptMessage {
			// Some code cannot be interrupted (a Julia loop that never
			// allocates, R inside C code). As Ctrl-C again in a terminal: a
			// second cancel, once the first had time to work, stops the
			// kernel.
			first := k.interruptedAt.Load()
			if first != 0 && time.Since(time.Unix(0, first)) >= forceAfter {
				k.killProcess()
				return kernel.CtlResult{Text: "CANCELLED | the code did not stop; kernel stopped, variables lost"}
			}
			if k.interrupt == InterruptMessage {
				if err := k.send(request{Op: "interrupt"}); err != nil {
					return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
				}
			} else {
				k.procMu.Lock()
				proc := k.proc
				k.procMu.Unlock()
				if err := procutil.InterruptGroup(proc); err != nil {
					return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
				}
			}
			k.interruptedAt.CompareAndSwap(0, time.Now().UnixNano())
			return kernel.CtlResult{Text: "CANCELLED"}
		}
		k.killProcess()
		return kernel.CtlResult{Text: "CANCELLED | kernel stopped, variables lost"}
	case "output":
		// Output of the run in progress, lock-free so Run is not blocked.
		return kernel.CtlResult{Text: k.partial.Get()}
	case "status":
		if k.executing.Load() {
			if k.waitingForInput.Load() {
				return kernel.CtlResult{Text: "waiting_for_input"}
			}
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
		resp, err := k.await(5*time.Second, false)
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

// Shutdown tears down the kernel subprocess, giving it a moment to exit
// on its own first.
func (k *Kernel) Shutdown() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.alive() && k.send(request{Op: "shutdown"}) == nil {
		select {
		case <-k.link.done:
		case <-time.After(500 * time.Millisecond):
		}
	}
	k.kill()
	return nil
}

// ── internal ────────────────────────────────────────────────

// alive reports whether the kernel process is up and its protocol stream
// open. A kernel that died (a crash, or a cancel that stopped it) is not,
// and the next request starts a fresh one.
func (k *Kernel) alive() bool {
	if k.cmd == nil || k.link == nil {
		return false
	}
	select {
	case <-k.link.done:
		return false
	default:
		return true
	}
}

func (k *Kernel) ensureStarted() error {
	if k.alive() {
		return nil
	}
	k.kill()

	args := append(append([]string{}, k.binaryArgs...), k.scriptPath)
	cmd := exec.Command(k.binaryPath, args...)
	cmd.Dir = k.cwd
	cmd.Env = append([]string{}, os.Environ()...)
	procutil.HideWindow(cmd)
	for key, value := range k.extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if k.interrupt == InterruptSignal {
		procutil.OwnProcessGroup(cmd)
		procutil.ChildrenReceiveInterrupts()
	}

	var listener net.Listener
	var token string
	var stdin io.WriteCloser
	if k.transport == TransportSocket {
		var err error
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("%s kernel: listen: %w", k.display, err)
		}
		defer listener.Close()
		token = randomToken()
		cmd.Env = append(cmd.Env,
			"RAT_PROTOCOL_TCP_ADDR="+listener.Addr().String(),
			"RAT_PROTOCOL_TOKEN="+token,
		)
		// The user's code reads an empty stdin; prompts go through the
		// protocol (input_request).
		cmd.Stdin = nil
	} else {
		var err error
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("stdin: %w", err)
		}
	}

	var outputs []io.Reader
	var protoOut io.Reader
	var outWrite *os.File // socket: the one pipe for stdout and stderr, closed here once started
	if k.transport == TransportSocket {
		// stdout and stderr share one pipe, as in a terminal: a warning
		// stays where it was written among the printed lines.
		r, w, err := os.Pipe()
		if err != nil {
			return fmt.Errorf("output pipe: %w", err)
		}
		cmd.Stdout, cmd.Stderr = w, w
		outWrite = w
		outputs = append(outputs, r)
	} else {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			return fmt.Errorf("stdout: %w", err)
		}
		protoOut = stdout
		stderr, err := cmd.StderrPipe()
		if err != nil {
			_ = stdin.Close()
			return fmt.Errorf("stderr: %w", err)
		}
		outputs = append(outputs, stderr)
	}

	k.diag.Reset()
	err := cmd.Start()
	if outWrite != nil {
		_ = outWrite.Close()
	}
	if err != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		for _, r := range outputs {
			if c, ok := r.(io.Closer); ok {
				_ = c.Close()
			}
		}
		return fmt.Errorf("start %s kernel: %w", k.display, err)
	}
	k.cmd = cmd
	k.procMu.Lock()
	k.proc = cmd.Process
	k.procMu.Unlock()

	var wg sync.WaitGroup
	for _, r := range outputs {
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			k.consumeOutput(r)
			if f, ok := r.(*os.File); ok {
				_ = f.Close()
			}
		}(r)
	}
	k.outputDone = make(chan struct{})
	go func(done chan struct{}) { wg.Wait(); close(done) }(k.outputDone)

	var reader *bufio.Reader
	if k.transport == TransportSocket {
		conn, r, err := k.acceptProtocol(listener, token)
		if err != nil {
			k.kill()
			return err
		}
		k.proto = conn
		reader = r
	} else {
		k.proto = stdin
		reader = bufio.NewReader(protoOut)
	}

	k.link = &link{ch: make(chan []byte, 64), done: make(chan struct{}), quit: make(chan struct{})}
	k.stale = 0
	go k.readerLoop(reader, k.link)

	// Ping to verify the kernel is alive.
	if err := k.send(request{Op: "ping"}); err != nil {
		k.kill()
		return err
	}
	resp, err := k.await(startTimeout, false)
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

// acceptProtocol waits for the kernel to connect and prove it is the
// process rat started (the token). It gives up at once if the process
// exits first.
func (k *Kernel) acceptProtocol(listener net.Listener, token string) (net.Conn, *bufio.Reader, error) {
	type accepted struct {
		conn   net.Conn
		reader *bufio.Reader
		err    error
	}
	ch := make(chan accepted, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			ch <- accepted{err: err}
			return
		}
		reader := bufio.NewReader(conn)
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		line, err := reader.ReadBytes('\n')
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil {
			_ = conn.Close()
			ch <- accepted{err: fmt.Errorf("read protocol hello: %w", err)}
			return
		}
		var hello response
		if json.Unmarshal(bytes.TrimSpace(line), &hello) != nil || hello.Op != "protocol_hello" || hello.Token != token {
			_ = conn.Close()
			ch <- accepted{err: fmt.Errorf("invalid protocol hello")}
			return
		}
		ch <- accepted{conn: conn, reader: reader}
	}()
	select {
	case a := <-ch:
		if a.err != nil {
			return nil, nil, fmt.Errorf("%s kernel: %w", k.display, a.err)
		}
		return a.conn, a.reader, nil
	case <-k.outputDone:
		_ = listener.Close()
		return nil, nil, k.exitError(fmt.Errorf("exited before connecting"))
	case <-time.After(startTimeout):
		_ = listener.Close()
		return nil, nil, fmt.Errorf("%s kernel did not connect within %s", k.display, startTimeout)
	}
}

// consumeOutput reads one of the kernel's output streams. During a run
// it is the user's output (socket transport); otherwise it is kept for
// error messages.
func (k *Kernel) consumeOutput(r io.Reader) {
	user := k.transport == TransportSocket
	buf := make([]byte, 8192)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			text := string(buf[:n])
			if user && k.executing.Load() {
				k.partial.Append(text)
				k.userOutput.Append(text)
			} else {
				k.diag.Append(text)
			}
		}
		if err != nil {
			return
		}
	}
}

// waitForOutputQuiet lets output written just before the reply arrive:
// the reply and the output travel on different streams.
func (k *Kernel) waitForOutputQuiet() {
	deadline := time.Now().Add(200 * time.Millisecond)
	quietSince := time.Now()
	lastLen := k.userOutput.Len()
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		n := k.userOutput.Len()
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

// readerLoop runs in a background goroutine. It reads every line of the
// protocol stream and routes it:
//   - op "event" → dispatched immediately (activity log + callback)
//   - everything else → l.ch for the active request to consume
func (k *Kernel) readerLoop(reader *bufio.Reader, l *link) {
	defer close(l.done)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			l.err = err
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
			continue
		}
		// Copy because bufio may reuse the buffer.
		msg := make([]byte, len(line))
		copy(msg, line)
		select {
		case l.ch <- msg:
		case <-l.quit:
			return
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
	k.writeMu.Lock()
	defer k.writeMu.Unlock()
	if k.proto == nil {
		return fmt.Errorf("%s kernel not started", k.display)
	}
	replied := req.Op != "input" && req.Op != "shutdown" && req.Op != "interrupt"
	if replied {
		k.nextID++
		req.ID = k.nextID
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := k.proto.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write to %s kernel: %w", k.display, err)
	}
	if replied {
		k.currentID = req.ID
		k.pending.Store(true)
	}
	return nil
}

// await returns the kernel's reply to the request just sent. A run's
// streaming messages (output, input prompts) are handled on the way, and
// replies owed to requests that stopped waiting are dropped. timeout 0
// waits as long as the kernel lives. For a run (isRun), only a message
// with a "success" field is the reply: older kernels acknowledge an
// answered prompt with {"ok": true}.
func (k *Kernel) await(timeout time.Duration, isRun bool) (response, error) {
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	if k.link == nil {
		return response{}, fmt.Errorf("%s kernel not started", k.display)
	}
	l := k.link
	// handle routes one message; it reports whether it is the reply.
	handle := func(raw []byte) (response, bool, error) {
		resp, err := decodeResponse(raw)
		if err != nil {
			return response{}, true, fmt.Errorf("decode %s kernel reply: %w", k.display, err)
		}
		switch resp.Op {
		case "output_chunk":
			k.partial.Append(resp.Text)
			return resp, false, nil
		case "input_request":
			// Store the prompt before the flag: a reader that sees the
			// flag must also see what the program asked.
			k.inputPrompt.Store(kernel.InputPrompt{Text: resp.Prompt, Secret: resp.Secret, Seq: k.inputSeq.Add(1)})
			k.waitingForInput.Store(true)
			return resp, false, nil
		case "input_delivered":
			k.waitingForInput.Store(false)
			return resp, false, nil
		}
		// A kernel that echoes ids says whose reply this is; one that does
		// not answers in order, and the replies owed to requests that
		// stopped waiting come first.
		if resp.ID != 0 {
			if resp.ID != k.currentID {
				if k.stale > 0 {
					k.stale--
				}
				return resp, false, nil
			}
			return resp, true, nil
		}
		if k.stale > 0 {
			k.stale--
			return resp, false, nil
		}
		if isRun && !resp.hasSuccess {
			return resp, false, nil
		}
		return resp, true, nil
	}
	for {
		select {
		case raw := <-l.ch:
			resp, final, err := handle(raw)
			if final {
				k.requestDone()
				return resp, err
			}
		case <-l.done:
			// The reader stopped: take what it delivered before.
			for drained := false; !drained; {
				select {
				case raw := <-l.ch:
					if resp, final, err := handle(raw); final {
						k.requestDone()
						return resp, err
					}
				default:
					drained = true
				}
			}
			k.requestDone()
			return response{}, k.exitError(l.err)
		case <-deadline:
			// The reply is still owed; the next request must not take it.
			k.stale++
			return response{}, fmt.Errorf("%s kernel: no reply after %s", k.display, timeout)
		}
	}
}

func decodeResponse(raw []byte) (response, error) {
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return response{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		_, resp.hasSuccess = fields["success"]
	}
	return resp, nil
}

// exitError describes a kernel that stopped, with what it printed.
func (k *Kernel) exitError(cause error) error {
	if k.outputDone != nil {
		select {
		case <-k.outputDone:
		case <-time.After(200 * time.Millisecond):
		}
	}
	diag := strings.TrimSpace(k.diag.Get())
	if diag != "" {
		lines := strings.Split(diag, "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		return fmt.Errorf("%s kernel exited: %s", k.display, strings.Join(lines, "\n"))
	}
	if cause == nil || cause == io.EOF {
		return fmt.Errorf("%s kernel exited", k.display)
	}
	return fmt.Errorf("%s kernel exited: %v", k.display, cause)
}

// killProcess stops the kernel process without k.mu. The request waiting
// on it sees the protocol stream close and returns.
func (k *Kernel) killProcess() {
	k.procMu.Lock()
	proc := k.proc
	k.procMu.Unlock()
	if proc == nil {
		return
	}
	if k.interrupt == InterruptSignal {
		_ = procutil.KillGroup(proc)
	} else {
		_ = proc.Kill()
	}
}

// kill stops the kernel and releases everything it held. Holds k.mu.
func (k *Kernel) kill() {
	k.writeMu.Lock()
	if k.proto != nil {
		_ = k.proto.Close()
		k.proto = nil
	}
	k.writeMu.Unlock()
	if k.cmd != nil {
		k.killProcess()
		_ = k.cmd.Wait()
		k.cmd = nil
	}
	k.procMu.Lock()
	k.proc = nil
	k.procMu.Unlock()
	// Stop the reader goroutine.
	if k.link != nil {
		close(k.link.quit)
		select {
		case <-k.link.done:
		case <-time.After(2 * time.Second):
		}
		k.link = nil
	}
	k.requestDone()
	k.waitingForInput.Store(false)
}

// requestDone marks the end of the request a cancel may act on.
func (k *Kernel) requestDone() {
	k.pending.Store(false)
	k.interruptedAt.Store(0)
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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
