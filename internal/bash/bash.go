// Package bash implements the Kernel interface for a shared bash session.
//
// The shell itself lives inside a tmux session. Humans attach with `rat sh`
// and MCP clients inject commands into that same shell via tmux. This keeps a
// single shared namespace while giving the human a real shell frontend.
package bash

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximerivest/rat/internal/kernel"
)

var validVarNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Bash is a tmux-backed shared bash kernel.
type Bash struct {
	name               string
	cwd                string
	version            string // e.g. "5.2.15", detected at startup
	tmuxPath           string
	sessionName        string
	dataDir            string
	controlPath        string
	currentID          string
	queryDir           string
	pendingPath        string
	pendingModePath    string
	pendingSummaryPath string

	mu             sync.Mutex
	executionCount int
	executing      atomic.Bool
}

// New creates a new shared bash kernel for the given runtime name.
func New(name, cwd string) (*Bash, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("find tmux: %w", err)
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, _ = filepath.Abs(cwd)

	dataDir, err := kernelDataDir(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "queries"), 0o700); err != nil {
		return nil, fmt.Errorf("create kernel dir: %w", err)
	}

	b := &Bash{
		name:               name,
		cwd:                cwd,
		version:            detectBashVersion(),
		tmuxPath:           tmuxPath,
		sessionName:        SessionName(name),
		dataDir:            dataDir,
		controlPath:        filepath.Join(dataDir, "control.log"),
		currentID:          filepath.Join(dataDir, "current-id"),
		queryDir:           filepath.Join(dataDir, "queries"),
		pendingPath:        filepath.Join(dataDir, "pending.sh"),
		pendingModePath:    filepath.Join(dataDir, "pending.mode"),
		pendingSummaryPath: filepath.Join(dataDir, "pending.summary"),
	}
	if err := b.ensureStartedLocked(); err != nil {
		return nil, err
	}
	return b, nil
}

// SessionName returns the tmux session name used for a kernel.
func SessionName(name string) string {
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

// Attach attaches the current terminal to the kernel tmux session.
func Attach(name string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("find tmux: %w", err)
	}
	cmd := exec.Command(tmuxPath, "attach-session", "-t", SessionName(name))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Run executes bash code in the shared tmux shell.
func (b *Bash) Run(code string) kernel.RunResult {
	start := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.ensureStartedLocked(); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: b.executionCount, Duration: int(time.Since(start).Milliseconds())}
	}

	b.executionCount++
	execCount := b.executionCount
	id := uniqueID()
	outPath := filepath.Join(b.queryDir, id+".run.out")
	_ = os.Remove(outPath)

	if err := b.startCaptureLocked(outPath); err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}

	if err := b.writeCurrentID(id); err != nil {
		_ = b.stopCaptureLocked()
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}

	b.executing.Store(true)
	defer b.executing.Store(false)

	_ = b.tmuxRun("display-message", "-d", "1500", "-t", b.target(), "rat> "+summarizeCode(code))
	if err := b.sendLiteralTextLocked(code + "\n"); err != nil {
		_ = b.stopCaptureLocked()
		_ = os.Remove(b.currentID)
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}

	status, err := b.waitForControlLocked(id)
	stopErr := b.stopCaptureLocked()
	if err == nil && stopErr != nil {
		err = stopErr
	}
	if err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}

	data, readErr := os.ReadFile(outPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return kernel.RunResult{Success: false, Error: readErr.Error(), ExecCount: execCount, Duration: int(time.Since(start).Milliseconds())}
	}
	_ = os.Remove(outPath)

	output := cleanRunOutput(string(data), code)
	if status != 0 {
		errMsg := output
		if errMsg == "" {
			errMsg = fmt.Sprintf("Command exited with code %d", status)
		} else {
			errMsg += fmt.Sprintf("\nCommand exited with code %d", status)
		}
		return kernel.RunResult{
			Success:   false,
			Output:    output,
			Error:     errMsg,
			ExecCount: execCount,
			Duration:  int(time.Since(start).Milliseconds()),
		}
	}

	return kernel.RunResult{
		Success:   true,
		Output:    output,
		ExecCount: execCount,
		Duration:  int(time.Since(start).Milliseconds()),
	}
}

// IsWaitingForInput checks if the running command needs stdin.
func (b *Bash) IsWaitingForInput() bool {
	if !b.executing.Load() {
		return false
	}
	pid, err := b.panePID()
	if err != nil || pid <= 0 {
		return false
	}
	waiting, _ := processWaitingForInput(pid, "")
	return waiting
}

// SendInput sends literal input into the shared tmux pane.
func (b *Bash) SendInput(text string) error {
	if !b.hasSessionLocked() {
		return fmt.Errorf("shell session not started")
	}
	var buf strings.Builder
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		chunk := buf.String()
		buf.Reset()
		return b.tmuxRun("send-keys", "-t", b.target(), "-l", chunk)
	}

	for _, r := range text {
		switch r {
		case '\x03':
			if err := flush(); err != nil {
				return err
			}
			if err := b.tmuxRun("send-keys", "-t", b.target(), "C-c"); err != nil {
				return err
			}
		case '\x04':
			if err := flush(); err != nil {
				return err
			}
			if err := b.tmuxRun("send-keys", "-t", b.target(), "C-d"); err != nil {
				return err
			}
		case '\r', '\n':
			if err := flush(); err != nil {
				return err
			}
			if err := b.tmuxRun("send-keys", "-t", b.target(), "Enter"); err != nil {
				return err
			}
		default:
			buf.WriteRune(r)
		}
	}
	return flush()
}

// Look inspects the shared bash session.
func (b *Bash) Look(req kernel.LookRequest) kernel.LookResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureStartedLocked(); err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	if req.Code != "" {
		return b.lookCompleteLocked(req.Code, req.Cursor)
	}
	if req.At != "" {
		return b.lookAtLocked(req.At)
	}
	return b.lookOverviewLocked()
}

// Ctl controls the shared bash runtime.
func (b *Bash) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset", "restart":
		b.mu.Lock()
		defer b.mu.Unlock()
		if err := b.restartLocked(); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		if op == "reset" {
			return kernel.CtlResult{Text: "RESET | namespace cleared | 0 vars"}
		}
		return kernel.CtlResult{Text: "RESTARTED | fresh bash session"}
	case "cancel":
		if !b.hasSessionLocked() {
			return kernel.CtlResult{Text: "CANCELLED"}
		}
		if err := b.tmuxRun("send-keys", "-t", b.target(), "C-c"); err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		return kernel.CtlResult{Text: "CANCELLED"}
	case "status":
		state := "idle"
		if !b.hasSessionLocked() {
			// session not started
		} else if b.IsWaitingForInput() {
			state = "waiting_for_input"
		} else if b.executing.Load() {
			state = "busy"
		}
		if b.version != "" {
			state += "\nruntime_version: bash " + b.version
		}
		return kernel.CtlResult{Text: state}
	case "output":
		// Bash doesn't stream partial output (tmux captures to file).
		return kernel.CtlResult{Text: ""}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'. Use reset, cancel, restart, or status.", op)}
	}
}

// Shutdown tears down the tmux session.
func (b *Bash) Shutdown() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = os.Remove(b.currentID)
	_ = os.Remove(b.controlPath)
	_ = os.Remove(b.pendingPath)
	_ = os.Remove(b.pendingModePath)
	_ = os.Remove(b.pendingSummaryPath)
	_ = b.tmuxRun("pipe-pane", "-t", b.target())
	if b.hasSessionLocked() {
		_ = b.tmuxRun("kill-session", "-t", b.sessionName)
	}
	return nil
}

func (b *Bash) lookOverviewLocked() kernel.LookResult {
	id := uniqueID()
	outPath := filepath.Join(b.queryDir, id+".vars.out")
	script := fmt.Sprintf(`__rat_query() {
  while IFS= read -r name; do
    [[ -z "$name" || "$name" == _* || "$name" == BASH* || "$name" == RAT_* || "$name" == __rat* ]] && continue
    printf '%%s\x1f%%s\x1e' "$name" "${!name}"
  done < <(compgen -A variable | LC_ALL=C sort)
}
__rat_query > %s
__rat_status=$?
unset -f __rat_query
return $__rat_status
`, shellQuote(outPath))
	output, err := b.runQueryLocked(script, id, outPath)
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}

	records := strings.Split(output, "\x1e")
	vars := make([]variable, 0, len(records))
	for _, record := range records {
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		value := strings.ReplaceAll(parts[1], "\r", "")
		if name == "" {
			continue
		}
		vars = append(vars, variable{Name: name, Type: "string", Value: truncateString(value, 60)})
	}
	if len(vars) == 0 {
		return kernel.LookResult{Text: "bash idle | 0 vars"}
	}
	if len(vars) > 200 {
		vars = vars[:200]
	}

	nw, tw := 4, 4
	for _, v := range vars {
		if len(v.Name) > nw {
			nw = len(v.Name)
		}
		if len(v.Type) > tw {
			tw = len(v.Type)
		}
	}

	lines := []string{fmt.Sprintf("bash idle | %d vars", len(vars)), ""}
	for _, v := range vars {
		lines = append(lines, fmt.Sprintf("%-*s  %-*s  %s", nw, v.Name, tw, v.Type, v.Value))
	}
	return kernel.LookResult{Text: strings.Join(lines, "\n")}
}

func (b *Bash) lookAtLocked(at string) kernel.LookResult {
	sym := strings.TrimPrefix(at, "$")
	id := uniqueID()
	outPath := filepath.Join(b.queryDir, id+".at.out")

	var body strings.Builder
	if validVarNameRegexp.MatchString(sym) {
		fmt.Fprintf(&body, "if declare -p -- %s >/dev/null 2>&1; then\n", sym)
		fmt.Fprintf(&body, "  printf 'var\\x1f%%s' \"${%s}\"\n", sym)
		body.WriteString("  return 0\n")
		body.WriteString("fi\n")
	}
	fmt.Fprintf(&body, "__rat_kind=$(type -t -- %s 2>/dev/null || true)\n", shellQuote(sym))
	body.WriteString("if [[ -n \"$__rat_kind\" ]]; then\n")
	body.WriteString("  printf 'cmd\\x1f%s' \"$__rat_kind\"\n")
	body.WriteString("else\n")
	body.WriteString("  printf 'missing'\n")
	body.WriteString("fi\n")

	script := fmt.Sprintf(`__rat_query() {
%s}
__rat_query > %s
__rat_status=$?
unset -f __rat_query
unset __rat_kind
return $__rat_status
`, indent(body.String(), "  "), shellQuote(outPath))
	output, err := b.runQueryLocked(script, id, outPath)
	if err != nil {
		return kernel.LookResult{Text: fmt.Sprintf("ERROR: %v", err)}
	}
	output = strings.ReplaceAll(output, "\r", "")
	switch {
	case strings.HasPrefix(output, "var\x1f"):
		value := strings.TrimPrefix(output, "var\x1f")
		text := fmt.Sprintf("%s (string)", sym)
		if value != "" {
			text += fmt.Sprintf(" = %s", truncateString(value, 1000))
		}
		return kernel.LookResult{Text: text}
	case strings.HasPrefix(output, "cmd\x1f"):
		kind := strings.TrimSpace(strings.TrimPrefix(output, "cmd\x1f"))
		return kernel.LookResult{Text: fmt.Sprintf("%s (%s)", at, kind)}
	default:
		return kernel.LookResult{Text: fmt.Sprintf("%s: not found", at)}
	}
}

func (b *Bash) lookCompleteLocked(code string, cursor int) kernel.LookResult {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(code) {
		cursor = len(code)
	}
	textBefore := code[:cursor]
	wordStart := cursor
	for wordStart > 0 && !strings.ContainsRune(" \t\n|&;(){}[]<>'\"", rune(textBefore[wordStart-1])) {
		wordStart--
	}
	word := textBefore[wordStart:]
	prefix := strings.TrimSpace(textBefore[:wordStart])
	isArg := prefix != ""

	id := uniqueID()
	outPath := filepath.Join(b.queryDir, id+".complete.out")

	var query string
	switch {
	case strings.HasPrefix(word, "$"):
		query = fmt.Sprintf(`while IFS= read -r c; do printf '$%%s\tvariable\n' "$c"; done < <(compgen -A variable -- %s)`, shellQuote(strings.TrimPrefix(word, "$")))
	case isArg || looksLikePath(word):
		query = fmt.Sprintf(`while IFS= read -r c; do if [[ -d "$c" && "$c" != */ ]]; then c="$c/"; fi; kind=file; [[ "$c" == */ ]] && kind=folder; printf '%%s\t%%s\n' "$c" "$kind"; done < <(compgen -f -- %s)`, shellQuote(word))
	default:
		query = fmt.Sprintf(`{
  while IFS= read -r c; do
    kind=function
    case "$c" in
      cd|echo|export|set|unset|source|alias|type|read|test|if|then|else|fi|for|while|do|done|case|esac|function|return|exit|exec|eval|trap|wait|jobs|fg|bg|kill|pwd|pushd|popd|dirs|history|declare|local|readonly|shift|getopts|true|false) kind=keyword ;;
    esac
    printf '%%s\t%%s\n' "$c" "$kind"
  done < <(compgen -A function -abck -- %s)
  while IFS= read -r c; do
    if [[ -d "$c" && "$c" != */ ]]; then c="$c/"; fi
    kind=file
    [[ "$c" == */ ]] && kind=folder
    printf '%%s\t%%s\n' "$c" "$kind"
  done < <(compgen -f -- %s)
}`, shellQuote(word), shellQuote(word))
	}

	script := fmt.Sprintf(`__rat_query() {
  %s
}
__rat_query > %s
__rat_status=$?
unset -f __rat_query
return $__rat_status
`, indent(query, "  "), shellQuote(outPath))
	output, err := b.runQueryLocked(script, id, outPath)
	if err != nil {
		return kernel.LookResult{Text: "No completions."}
	}

	lines := splitNonEmptyLines(output)
	seen := map[string]struct{}{}
	formatted := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		label := strings.TrimSpace(parts[0])
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		kind := "value"
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			kind = strings.TrimSpace(parts[1])
		}
		formatted = append(formatted, fmt.Sprintf("%-20s %s", label, kind))
		if len(formatted) == 50 {
			break
		}
	}
	if len(formatted) == 0 {
		return kernel.LookResult{Text: "No completions."}
	}
	return kernel.LookResult{Text: strings.Join(formatted, "\n")}
}

func (b *Bash) runQueryLocked(script, id, outPath string) (string, error) {
	if err := os.WriteFile(outPath, nil, 0o600); err != nil {
		return "", fmt.Errorf("create query output: %w", err)
	}
	scriptPath := filepath.Join(b.queryDir, id+".sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return "", fmt.Errorf("write query script: %w", err)
	}
	defer os.Remove(scriptPath)
	defer os.Remove(outPath)

	if err := b.writeCurrentID(id); err != nil {
		return "", err
	}
	if err := b.injectSourcedScriptLocked(scriptPath, "", false); err != nil {
		_ = os.Remove(b.currentID)
		return "", err
	}
	status, err := b.waitForControlLocked(id)
	if err != nil {
		return "", err
	}
	data, readErr := os.ReadFile(outPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", readErr
	}
	if status != 0 {
		return string(data), fmt.Errorf("query exited with code %d", status)
	}
	return string(data), nil
}

func (b *Bash) ensureStartedLocked() error {
	if b.hasSessionLocked() {
		return nil
	}
	return b.startSessionLocked()
}

func (b *Bash) restartLocked() error {
	_ = b.tmuxRun("pipe-pane", "-t", b.target())
	if b.hasSessionLocked() {
		_ = b.tmuxRun("kill-session", "-t", b.sessionName)
	}
	_ = os.Remove(b.currentID)
	_ = os.Remove(b.controlPath)
	_ = os.Remove(b.pendingPath)
	_ = os.Remove(b.pendingModePath)
	_ = os.Remove(b.pendingSummaryPath)
	return b.startSessionLocked()
}

func (b *Bash) startSessionLocked() error {
	if err := os.MkdirAll(b.queryDir, 0o700); err != nil {
		return fmt.Errorf("create query dir: %w", err)
	}
	_ = os.WriteFile(b.controlPath, nil, 0o600)
	_ = os.Remove(b.currentID)
	_ = os.Remove(b.pendingPath)
	_ = os.Remove(b.pendingModePath)
	_ = os.Remove(b.pendingSummaryPath)

	cmd := fmt.Sprintf("env RAT_CONTROL_DIR=%s bash -l", shellQuote(b.dataDir))
	if err := b.tmuxRun("new-session", "-d", "-s", b.sessionName, "-c", b.cwd, cmd); err != nil {
		return err
	}
	b.configureUILocked()
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "history-limit", "200000")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "remain-on-exit", "off")
	_ = b.tmuxRun("rename-window", "-t", b.target(), "shell")
	_ = b.tmuxRun("set-window-option", "-t", b.target(), "automatic-rename", "off")
	_ = b.tmuxRun("set-window-option", "-t", b.target(), "allow-rename", "off")

	time.Sleep(300 * time.Millisecond)
	initCmd := ` export HISTCONTROL=ignorespace:ignoredups HISTSIZE=50000 HISTFILESIZE=50000; shopt -s cmdhist lithist; set -o history; bind 'set enable-bracketed-paste off' 2>/dev/null || true; __rat_user_prompt_command=$PROMPT_COMMAND; __rat_prompt_hook(){ local status=$?; if [[ -n "$__rat_user_prompt_command" ]]; then eval "$__rat_user_prompt_command"; fi; if [[ -n "${RAT_CONTROL_DIR:-}" && -f "$RAT_CONTROL_DIR/current-id" ]]; then local __rat_id; __rat_id=$(head -n 1 "$RAT_CONTROL_DIR/current-id" 2>/dev/null); if [[ -n "$__rat_id" ]]; then printf '%s\t%s\n' "$__rat_id" "$status" >> "$RAT_CONTROL_DIR/control.log"; fi; rm -f "$RAT_CONTROL_DIR/current-id"; fi; }; __rat_dispatch(){ local __rat_mode= __rat_summary= __rat_status=0 __rat_id=; if [[ -f "$RAT_CONTROL_DIR/pending.mode" ]]; then __rat_mode=$(cat "$RAT_CONTROL_DIR/pending.mode" 2>/dev/null); fi; if [[ "$__rat_mode" == "visible" && -f "$RAT_CONTROL_DIR/pending.summary" ]]; then __rat_summary=$(cat "$RAT_CONTROL_DIR/pending.summary" 2>/dev/null); printf '\033[2mrat>\033[0m %s\n' "$__rat_summary"; fi; if [[ -f "$RAT_CONTROL_DIR/pending.sh" ]]; then source "$RAT_CONTROL_DIR/pending.sh"; __rat_status=$?; fi; if [[ -f "$RAT_CONTROL_DIR/current-id" ]]; then __rat_id=$(head -n 1 "$RAT_CONTROL_DIR/current-id" 2>/dev/null); if [[ -n "$__rat_id" ]]; then printf '%s\t%s\n' "$__rat_id" "$__rat_status" >> "$RAT_CONTROL_DIR/control.log"; fi; rm -f "$RAT_CONTROL_DIR/current-id"; fi; rm -f "$RAT_CONTROL_DIR/pending.sh" "$RAT_CONTROL_DIR/pending.mode" "$RAT_CONTROL_DIR/pending.summary"; }; bind -x '"\C-]":__rat_dispatch'; PROMPT_COMMAND=__rat_prompt_hook`
	if err := b.pasteLocked(initCmd + "\nclear\n"); err != nil {
		_ = b.tmuxRun("kill-session", "-t", b.sessionName)
		return err
	}
	time.Sleep(300 * time.Millisecond)
	_ = b.tmuxRun("clear-history", "-t", b.target())
	_ = os.WriteFile(b.controlPath, nil, 0o600)
	_ = os.Remove(b.currentID)
	_ = os.Remove(b.pendingPath)
	_ = os.Remove(b.pendingModePath)
	_ = os.Remove(b.pendingSummaryPath)
	return nil
}

func (b *Bash) configureUILocked() {
	left := "#[bold]rat sh#[nobold] #[fg=colour45](ratmux)#[default] | " + b.name + " | #{b:pane_current_path}"
	right := "#[fg=colour10]shared shell#[default] • Ctrl+C interrupt • Ctrl+B d detach"
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status", "on")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-position", "bottom")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-interval", "1")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-style", "bg=colour235,fg=colour252")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "message-style", "bg=colour45,fg=colour16")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-left-length", "80")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-right-length", "100")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-left", left)
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "status-right", right)
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "window-status-format", "")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "window-status-current-format", "")
	_ = b.tmuxRun("set-option", "-t", b.sessionName, "window-status-separator", "")
}

func (b *Bash) startCaptureLocked(outPath string) error {
	if err := os.WriteFile(outPath, nil, 0o600); err != nil {
		return fmt.Errorf("create capture file: %w", err)
	}
	_ = b.tmuxRun("pipe-pane", "-t", b.target())
	return b.tmuxRun("pipe-pane", "-t", b.target(), fmt.Sprintf("cat >> %s", shellQuote(outPath)))
}

func (b *Bash) stopCaptureLocked() error {
	return b.tmuxRun("pipe-pane", "-t", b.target())
}

func (b *Bash) waitForControlLocked(id string) (int, error) {
	for {
		data, err := os.ReadFile(b.controlPath)
		if err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("read control log: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) != 2 || parts[0] != id {
				continue
			}
			status, convErr := strconv.Atoi(strings.TrimSpace(parts[1]))
			if convErr != nil {
				return 0, fmt.Errorf("parse control status: %w", convErr)
			}
			_ = os.Remove(b.currentID)
			return status, nil
		}
		if !b.hasSessionLocked() {
			_ = os.Remove(b.currentID)
			return 0, fmt.Errorf("shell session exited")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (b *Bash) writeCurrentID(id string) error {
	if err := os.WriteFile(b.currentID, []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("write current id: %w", err)
	}
	return nil
}

func (b *Bash) pasteLocked(text string) error {
	bufName := fmt.Sprintf("rat-%d", time.Now().UnixNano())
	cmd := exec.Command(b.tmuxPath, "load-buffer", "-b", bufName, "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %v: %s", err, strings.TrimSpace(string(out)))
	}
	defer b.tmuxRun("delete-buffer", "-b", bufName)
	return b.tmuxRun("paste-buffer", "-d", "-b", bufName, "-t", b.target())
}

func (b *Bash) sendLiteralTextLocked(text string) error {
	var buf strings.Builder
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		chunk := buf.String()
		buf.Reset()
		return b.tmuxRun("send-keys", "-t", b.target(), "-l", chunk)
	}
	for _, r := range text {
		switch r {
		case '\r', '\n':
			if err := flush(); err != nil {
				return err
			}
			if err := b.tmuxRun("send-keys", "-t", b.target(), "Enter"); err != nil {
				return err
			}
		default:
			buf.WriteRune(r)
		}
	}
	return flush()
}

func (b *Bash) panePID() (int, error) {
	out, err := b.tmuxOutput("display-message", "-p", "-t", b.target(), "#{pane_pid}")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func (b *Bash) target() string {
	return b.sessionName + ":0.0"
}

func (b *Bash) hasSessionLocked() bool {
	cmd := exec.Command(b.tmuxPath, "has-session", "-t", b.sessionName)
	return cmd.Run() == nil
}

func (b *Bash) tmuxRun(args ...string) error {
	cmd := exec.Command(b.tmuxPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *Bash) tmuxOutput(args ...string) (string, error) {
	cmd := exec.Command(b.tmuxPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type variable struct {
	Name  string
	Type  string
	Value string
}

func detectBashVersion() string {
	out, err := exec.Command("bash", "--version").Output()
	if err != nil {
		return ""
	}
	// First line: "GNU bash, version 5.2.15(1)-release ..."
	line := strings.SplitN(string(out), "\n", 2)[0]
	// Extract version number after "version "
	if i := strings.Index(line, "version "); i >= 0 {
		ver := line[i+8:]
		// Trim everything after the version number (parenthetical, etc.)
		for j, c := range ver {
			if c != '.' && (c < '0' || c > '9') {
				return ver[:j]
			}
		}
		return ver
	}
	return ""
}

func kernelDataDir(name string) (string, error) {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "rat", "kernels", name), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
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

func (b *Bash) injectSourcedScriptLocked(scriptPath, summary string, visible bool) error {
	loader := "source " + shellQuote(scriptPath) + "\n"
	if err := os.WriteFile(b.pendingPath, []byte(loader), 0o600); err != nil {
		return fmt.Errorf("write pending script: %w", err)
	}
	mode := "hidden\n"
	if visible {
		mode = "visible\n"
		if err := os.WriteFile(b.pendingSummaryPath, []byte(summary+"\n"), 0o600); err != nil {
			return fmt.Errorf("write pending summary: %w", err)
		}
	} else {
		_ = os.Remove(b.pendingSummaryPath)
	}
	if err := os.WriteFile(b.pendingModePath, []byte(mode), 0o600); err != nil {
		return fmt.Errorf("write pending mode: %w", err)
	}
	return b.tmuxRun("send-keys", "-t", b.target(), "C-]")
}

func cleanRunOutput(output, code string) string {
	output = stripShellNoise(output)
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = strings.ReplaceAll(output, "\r", "\n")

	codeLines := map[string]struct{}{}
	for _, line := range strings.Split(code, "\n") {
		trimmed := strings.TrimSpace(stripANSI(line))
		if trimmed != "" {
			codeLines[trimmed] = struct{}{}
		}
	}

	lines := strings.Split(output, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		clean := strings.TrimSpace(stripANSI(line))
		if clean == "" {
			filtered = append(filtered, "")
			continue
		}
		if strings.Contains(clean, "__rat_prompt_hook") || clean == "clear" || strings.Contains(clean, "$ clear") || isRatPrefixLine(clean) {
			continue
		}
		remove := false
		for codeLine := range codeLines {
			if clean == codeLine || strings.HasSuffix(clean, codeLine) {
				remove = true
				break
			}
		}
		if remove || looksLikePromptLine(clean) {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.TrimSpace(strings.Join(trimTrailingBlankLines(filtered), "\n"))
}

func trimTrailingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

func stripShellNoise(s string) string {
	s = strings.ReplaceAll(s, "\x1b[?2004h", "")
	s = strings.ReplaceAll(s, "\x1b[?2004l", "")
	return s
}

func splitNonEmptyLines(s string) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func truncateString(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func summarizeCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "(empty)"
	}
	lines := strings.Split(code, "\n")
	summary := strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		summary += " …"
	}
	return truncateString(summary, 80)
}

func looksLikePath(s string) bool {
	return strings.Contains(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~")
}

func isRatPrefixLine(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "rat> ")
}

func looksLikePromptLine(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, "$") || strings.HasSuffix(s, "#") || strings.HasSuffix(s, "%") || strings.HasSuffix(s, ">") {
		if strings.Contains(s, "@") || strings.Contains(s, ":") || strings.Contains(s, "~/") || strings.Contains(s, "/") {
			return true
		}
	}
	return false
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

var ansiEscapePattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Z0-9]|[0-9A-Za-z=<>])`)

func stripANSI(text string) string {
	return ansiEscapePattern.ReplaceAllString(text, "")
}
