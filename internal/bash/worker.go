package bash

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unsafe"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	varSeparator    = "\x1f"
	recordSeparator = "\x1e"
)

var (
	errReadTimeout     = errors.New("read timeout")
	ErrInputCancelled  = errors.New("input cancelled by user")
	ansiEscapePattern  = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Z0-9]|[0-9A-Za-z=<>])`)
	validVarNameRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type outputCallback func(stream, chunk, accumulated string)
type stdinCallback func(StdinRequest) (string, error)

type BashWorker struct {
	cwd          string
	extraEnv     map[string]string
	bashPath     string
	created      time.Time
	lastActivity time.Time

	mu             sync.Mutex
	executionCount int
	cmd            *exec.Cmd
	ptyFile        *os.File
	ctlRead        *os.File
	ctlBuf         bytes.Buffer

	executing atomic.Bool // true while a user command is in flight

	// interruptPty is a separate reference to the PTY master, protected by
	// its own mutex so Interrupt() can write Ctrl+C without acquiring w.mu
	// (which is held for the entire duration of ExecuteStreaming).
	interruptMu  sync.Mutex
	interruptPty *os.File
}

func NewBashWorker(cwd string, extraEnv map[string]string) (*BashWorker, error) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		bashPath = "/bin/bash"
	}

	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	return &BashWorker{
		cwd:          cwd,
		extraEnv:     extraEnv,
		bashPath:     bashPath,
		created:      now,
		lastActivity: now,
	}, nil
}

func (w *BashWorker) Execute(code string, storeHistory bool, execID string) ExecuteResult {
	return w.ExecuteStreaming(code, storeHistory, execID, nil, nil)
}

func (w *BashWorker) ExecuteStreaming(code string, storeHistory bool, execID string, onOutput outputCallback, onStdin stdinCallback) ExecuteResult {
	w.mu.Lock()
	defer w.mu.Unlock()

	start := time.Now()
	if err := w.ensureStartedLocked(); err != nil {
		return failureResult("StartupError", err.Error(), "", w.executionCount, start)
	}

	w.lastActivity = time.Now().UTC()
	if storeHistory {
		w.executionCount++
	}
	execCount := w.executionCount

	if w.ptyFile != nil {
		_, _ = w.ptyFile.Write([]byte{3})
		time.Sleep(50 * time.Millisecond)
		_, _ = w.readAvailableLocked(100 * time.Millisecond)
	}

	cleanCode := strings.TrimRightFunc(code, unicode.IsSpace)
	w.drainControlLocked()

	if err := w.writeCommandSequenceLocked(cleanCode, !storeHistory); err != nil {
		return failureResult("WriteError", err.Error(), "", execCount, start)
	}

	w.executing.Store(true)
	stdout, exitCode, err := w.readUntilDoneLocked(code, execID, onOutput, onStdin, false)
	w.executing.Store(false)
	if err != nil {
		if errors.Is(err, ErrInputCancelled) {
			return ExecuteResult{
				Success:        false,
				Stdout:         stdout,
				Stderr:         "",
				Result:         nil,
				Error:          &ExecuteError{Type: "InputCancelled", Message: "Input cancelled by user", Traceback: []string{}},
				DisplayData:    []DisplayData{},
				Assets:         []Asset{},
				ExecutionCount: execCount,
				Duration:       int(time.Since(start).Milliseconds()),
			}
		}
		return failureResult(typeName(err), err.Error(), stdout, execCount, start)
	}

	if exitCode != 0 {
		traceback := []string{}
		if stdout != "" {
			traceback = []string{stdout}
		}
		return ExecuteResult{
			Success: false,
			Stdout:  stdout,
			Stderr:  "",
			Result:  nil,
			Error: &ExecuteError{
				Type:      "BashError",
				Message:   fmt.Sprintf("Command exited with code %d", exitCode),
				Traceback: traceback,
			},
			DisplayData:    []DisplayData{},
			Assets:         []Asset{},
			ExecutionCount: execCount,
			Duration:       int(time.Since(start).Milliseconds()),
		}
	}

	return ExecuteResult{
		Success:        true,
		Stdout:         stdout,
		Stderr:         "",
		Result:         nil,
		Error:          nil,
		DisplayData:    []DisplayData{},
		Assets:         []Asset{},
		ExecutionCount: execCount,
		Duration:       int(time.Since(start).Milliseconds()),
	}
}

func (w *BashWorker) Complete(code string, cursorPos int) CompleteResult {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureStartedLocked(); err != nil {
		return CompleteResult{Matches: []CompletionItem{}, CursorStart: cursorPos, CursorEnd: cursorPos, Source: "runtime"}
	}

	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(code) {
		cursorPos = len(code)
	}

	textBefore := code[:cursorPos]
	wordStart := cursorPos
	for wordStart > 0 && !strings.ContainsRune(" \t\n|&;(){}[]<>'\"", rune(textBefore[wordStart-1])) {
		wordStart--
	}
	word := textBefore[wordStart:]

	matches := []CompletionItem{}
	seen := map[string]struct{}{}

	addMatch := func(label, kind string) {
		if _, ok := seen[label]; ok || label == "" {
			return
		}
		seen[label] = struct{}{}
		insert := label
		matches = append(matches, CompletionItem{Label: label, InsertText: &insert, Kind: kind})
	}

	// Determine if the word is a command (first token) or an argument.
	isArg := false
	prefix := strings.TrimSpace(textBefore[:wordStart])
	if prefix != "" {
		// There's text before our word — we're completing an argument, not a command
		isArg = true
	}

	var completions []string
	if strings.HasPrefix(word, "$") {
		varPrefix := strings.TrimPrefix(word, "$")
		completions = w.compgenLocked("compgen -A variable -- " + shellQuote(varPrefix))
		for _, c := range completions {
			addMatch("$"+c, "variable")
		}
	} else if isArg || looksLikePath(word) {
		// Argument position or explicit path: complete files and directories
		completions = w.compgenLocked("compgen -f -- " + shellQuote(word))
		for _, c := range completions {
			kind := "file"
			if strings.HasSuffix(c, "/") {
				kind = "folder"
			}
			addMatch(c, kind)
		}
	} else {
		// Command position: complete commands, then also files as fallback
		completions = w.compgenLocked("compgen -A function -abck -- " + shellQuote(word))
		for _, c := range completions {
			kind := "function"
			if isBashKeyword(c) {
				kind = "keyword"
			}
			addMatch(c, kind)
		}
		// Also offer files (e.g. ./script.sh)
		completions = w.compgenLocked("compgen -f -- " + shellQuote(word))
		for _, c := range completions {
			kind := "file"
			if strings.HasSuffix(c, "/") {
				kind = "folder"
			}
			addMatch(c, kind)
		}
	}

	if len(matches) > 50 {
		matches = matches[:50]
	}

	return CompleteResult{Matches: matches, CursorStart: wordStart, CursorEnd: cursorPos, Source: "runtime"}
}

func (w *BashWorker) Hover(code string, cursorPos int) HoverResult {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureStartedLocked(); err != nil {
		return HoverResult{Found: false}
	}

	start, end := wordBounds(code, cursorPos)
	if start == end {
		return HoverResult{Found: false}
	}
	word := code[start:end]

	if start > 0 && code[start-1] == '$' {
		if value, ok := w.getVariableValueLocked(word); ok {
			name := "$" + word
			typeName := "variable"
			return HoverResult{Found: true, Name: &name, Type: &typeName, Value: &value}
		}
		return HoverResult{Found: false}
	}

	if strings.HasPrefix(word, "$") {
		nameOnly := strings.TrimPrefix(word, "$")
		if value, ok := w.getVariableValueLocked(nameOnly); ok {
			name := word
			typeName := "variable"
			return HoverResult{Found: true, Name: &name, Type: &typeName, Value: &value}
		}
		return HoverResult{Found: false}
	}

	cmdType := w.commandTypeLocked(word)
	if cmdType == "" {
		return HoverResult{Found: false}
	}
	name := word
	return HoverResult{Found: true, Name: &name, Type: &cmdType}
}

func (w *BashWorker) GetVariables(filterPattern string) VariablesResult {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureStartedLocked(); err != nil {
		return VariablesResult{Variables: []Variable{}, Count: 0, Truncated: false}
	}

	vars := w.collectVariablesLocked(filterPattern)
	count := len(vars)
	truncated := false
	if count > 200 {
		vars = vars[:200]
		truncated = true
	}
	return VariablesResult{Variables: vars, Count: count, Truncated: truncated}
}

func (w *BashWorker) GetVariableDetail(name string, _ []string, maxValueLength int) VariableDetail {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureStartedLocked(); err != nil {
		return VariableDetail{Name: name, Type: "undefined", Value: "", Expandable: false, Truncated: false}
	}

	value, ok := w.getVariableValueLocked(name)
	if !ok {
		return VariableDetail{Name: name, Type: "undefined", Value: "", Expandable: false, Truncated: false}
	}

	if maxValueLength <= 0 {
		maxValueLength = 1000
	}
	fullValue := value
	display := truncateString(fullValue, maxValueLength)
	length := len(fullValue)
	truncated := len(display) < len(fullValue)
	return VariableDetail{
		Name:       name,
		Type:       "string",
		Value:      display,
		Expandable: false,
		Length:     &length,
		FullValue:  &fullValue,
		Truncated:  truncated,
	}
}

func (w *BashWorker) IsComplete(code string) IsCompleteResult {
	singleQuotes := strings.Count(code, "'")
	doubleQuotes := strings.Count(code, `"`) - strings.Count(code, `\\"`)
	if singleQuotes%2 != 0 || doubleQuotes%2 != 0 {
		return IsCompleteResult{Status: "incomplete", Indent: ""}
	}

	if strings.Count(code, "(") > strings.Count(code, ")") ||
		strings.Count(code, "{") > strings.Count(code, "}") ||
		strings.Count(code, "[") > strings.Count(code, "]") {
		return IsCompleteResult{Status: "incomplete", Indent: "  "}
	}

	trimmed := strings.TrimRightFunc(code, unicode.IsSpace)
	if strings.HasSuffix(trimmed, "\\") || strings.HasSuffix(trimmed, "|") || strings.HasSuffix(trimmed, "&&") || strings.HasSuffix(trimmed, "||") {
		return IsCompleteResult{Status: "incomplete", Indent: ""}
	}

	words := regexp.MustCompile(`\b\w+\b`).FindAllString(code, -1)
	depth := 0
	for _, word := range words {
		switch word {
		case "if", "for", "while", "until", "case":
			depth++
		case "fi", "done", "esac":
			depth--
		}
	}
	if depth > 0 {
		return IsCompleteResult{Status: "incomplete", Indent: "  "}
	}

	cmd := exec.Command(w.bashPath, "-n")
	cmd.Stdin = strings.NewReader(code)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.ToLower(stderr.String())
		if strings.Contains(message, "unexpected eof") || strings.Contains(message, "unexpected end of file") {
			return IsCompleteResult{Status: "incomplete", Indent: "  "}
		}
		return IsCompleteResult{Status: "invalid", Indent: ""}
	}

	return IsCompleteResult{Status: "complete", Indent: ""}
}

func (w *BashWorker) GetHistory(n int, pattern string, before *int) HistoryResult {
	w.mu.Lock()
	defer w.mu.Unlock()

	if n <= 0 {
		n = 20
	}
	if err := w.ensureStartedLocked(); err != nil {
		return HistoryResult{Entries: []HistoryEntry{}, HasMore: false}
	}

	matcher, err := compileOptionalGlobPattern(pattern)
	if err != nil {
		return HistoryResult{Entries: []HistoryEntry{}, HasMore: false}
	}

	output, _, err := w.runProbeLocked("history -n >/dev/null 2>&1 || true; HISTTIMEFORMAT= history")
	if err != nil {
		return HistoryResult{Entries: []HistoryEntry{}, HasMore: false}
	}

	history := parseHistoryOutput(output)
	filtered := make([]HistoryEntry, 0, len(history))
	for _, entry := range history {
		if before != nil && entry.HistoryIndex >= *before {
			continue
		}
		if matcher != nil && !matcher.MatchString(entry.Code) {
			continue
		}
		filtered = append(filtered, entry)
	}

	if len(filtered) == 0 {
		return HistoryResult{Entries: []HistoryEntry{}, HasMore: false}
	}

	start := len(filtered) - n
	if start < 0 {
		start = 0
	}
	entries := append([]HistoryEntry(nil), filtered[start:]...)
	return HistoryResult{Entries: entries, HasMore: start > 0}
}

// IsWaitingForInput checks if a running command needs stdin input.
// Returns true only when a command is executing AND (bash or a child
// is blocked reading stdin). When idle (no command running), bash is
// always reading stdin for the next command — that doesn't count.
// Lock-free — safe to call during execution.
func (w *BashWorker) IsWaitingForInput() bool {
	if !w.executing.Load() {
		return false // idle — bash reading next command, not user input
	}
	if w.cmd == nil || w.cmd.Process == nil {
		return false
	}
	waiting, _ := processWaitingForInput(w.cmd.Process.Pid, "")
	return waiting
}

// SendInput writes text to the PTY without acquiring the execution lock.
// Safe to call while a command is running — that's the intended use case.
func (w *BashWorker) SendInput(text string) error {
	w.interruptMu.Lock()
	f := w.interruptPty
	w.interruptMu.Unlock()
	if f == nil {
		return fmt.Errorf("bash process not started")
	}
	_, err := f.Write([]byte(text))
	return err
}

func (w *BashWorker) Interrupt() bool {
	// Write Ctrl+C (ETX, 0x03) to the PTY master. The terminal driver
	// delivers SIGINT to the foreground process group, which correctly
	// handles job control (child commands in their own process groups).
	//
	// We use interruptPty with its own mutex instead of w.mu because
	// ExecuteStreaming holds w.mu for the entire execution duration.
	w.interruptMu.Lock()
	f := w.interruptPty
	w.interruptMu.Unlock()

	if f == nil {
		return false
	}
	_, err := f.Write([]byte{3}) // Ctrl+C
	if err != nil {
		return false
	}

	// After Ctrl+C kills the foreground process, bash shows a new prompt,
	// which fires PROMPT_COMMAND. But if PROMPT_COMMAND was already consumed
	// or unset, we re-inject it as a fallback so fd 3 gets written.
	go func() {
		time.Sleep(150 * time.Millisecond)
		w.interruptMu.Lock()
		f := w.interruptPty
		w.interruptMu.Unlock()
		if f != nil {
			_, _ = f.Write([]byte(" printf '%s\\n' \"$?\" >&3\n"))
		}
	}()

	return true
}

func (w *BashWorker) Reset() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.killProcessLocked()
	w.executionCount = 0
	return w.ensureStartedLocked()
}

func (w *BashWorker) Shutdown() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.killProcessLocked()
	return nil
}

func (w *BashWorker) Info() WorkerInfo {
	w.mu.Lock()
	defer w.mu.Unlock()

	running := w.cmd != nil && w.cmd.ProcessState == nil
	return WorkerInfo{
		CWD:            w.cwd,
		BashPath:       w.bashPath,
		ExecutionCount: w.executionCount,
		Created:        w.created,
		LastActivity:   w.lastActivity,
		Running:        running,
	}
}

func (w *BashWorker) ensureStartedLocked() error {
	if w.cmd != nil && w.cmd.Process != nil && w.cmd.ProcessState == nil {
		return nil
	}

	cmd := exec.Command(w.bashPath, "-l", "-i")
	cmd.Dir = w.cwd
	cmd.Env = envMapToList(w.buildCleanEnv())

	// Side channel on fd 3: bash writes one exit code line per completed command.
	ctlRead, ctlWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = []*os.File{ctlWrite} // child sees this as fd 3

	attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true}
	// Set a reasonable default PTY size so commands like `ls` format correctly.
	winSize := &pty.Winsize{Rows: 24, Cols: 200}
	ptyFile, err := pty.StartWithAttrs(cmd, winSize, attrs)
	if err != nil {
		ctlRead.Close()
		ctlWrite.Close()
		return err
	}
	_ = ctlWrite.Close() // child owns fd 3 now

	w.cmd = cmd
	w.ptyFile = ptyFile
	w.ctlRead = ctlRead
	w.ctlBuf.Reset()

	w.interruptMu.Lock()
	w.interruptPty = ptyFile
	w.interruptMu.Unlock()

	time.Sleep(200 * time.Millisecond)
	_, _ = w.readAvailableLocked(500 * time.Millisecond)
	w.drainControlLocked()
	// Set environment, disable prompts. Must come after .bashrc has finished.
	_ = w.writeCommandLocked("export HISTCONTROL=ignorespace:ignoredups HISTSIZE=50000 HISTFILESIZE=50000; shopt -s cmdhist lithist; set -o history; PS0='' PS1='' PS2='' PS3='' PS4=''; unset PROMPT_COMMAND", true)
	time.Sleep(50 * time.Millisecond)
	_, _ = w.readAvailableLocked(100 * time.Millisecond)
	w.drainControlLocked()
	_ = w.writeCommandLocked("history -c; history -r; bind 'set enable-bracketed-paste off' 2>/dev/null || true", true)
	time.Sleep(50 * time.Millisecond)
	_, _ = w.readAvailableLocked(100 * time.Millisecond)
	w.drainControlLocked()
	// Force stty -echo as the LAST init step, after everything else that
	// might reset terminal settings (.bashrc, /etc/profile, etc).
	_ = w.writeCommandLocked("stty -echo 2>/dev/null || true", true)
	time.Sleep(50 * time.Millisecond)
	_, _ = w.readAvailableLocked(100 * time.Millisecond)
	w.drainControlLocked()
	return nil
}

func (w *BashWorker) buildCleanEnv() map[string]string {
	essential := []string{
		"HOME", "USER", "LOGNAME", "SHELL",
		"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_NUMERIC", "LC_TIME", "LC_COLLATE",
		"DISPLAY", "WAYLAND_DISPLAY",
		"DBUS_SESSION_BUS_ADDRESS", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO",
		"EDITOR", "VISUAL", "PAGER", "TMPDIR", "TZ",
	}
	env := map[string]string{}
	for _, key := range essential {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	if value, ok := os.LookupEnv("HISTFILE"); ok && value != "" {
		env["HISTFILE"] = value
	} else if home := env["HOME"]; home != "" {
		env["HISTFILE"] = filepath.Join(home, ".bash_history")
	}
	env["PATH"] = os.Getenv("PATH")
	if env["PATH"] == "" {
		env["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	env["TERM"] = "xterm-256color"
	for k, v := range w.extraEnv {
		env[k] = v
	}
	return env
}

func (w *BashWorker) writeCommandLocked(command string, hideHistory bool) error {
	if w.ptyFile == nil {
		return errors.New("bash process not started")
	}
	prefix := ""
	if hideHistory {
		prefix = " " // HISTCONTROL=ignorespace hides internal commands from history
	}
	_, err := w.ptyFile.Write([]byte(prefix + command + "\n"))
	return err
}

// writeCommandSequenceLocked sends a user-visible command followed by a hidden
// control line that writes the previous exit code to fd 3. This keeps shell
// history clean (only the user command is recorded) while giving Go a reliable
// completion signal on the side channel.
func (w *BashWorker) writeCommandSequenceLocked(command string, hideHistory bool) error {
	if w.ptyFile == nil {
		return errors.New("bash process not started")
	}
	prefix := ""
	if hideHistory {
		prefix = " "
	}
	// We need to know when the user command finishes WITHOUT polluting
	// stdin (which would break `read`, `cat`, etc). Solution: set a trap
	// on the same line as the command. The trap fires when the command
	// exits but doesn't interfere with stdin.
	//
	// PROMPT_COMMAND fires after each command completes, before the next
	// prompt. By setting it on the SAME line as the user command (using ;),
	// bash processes them as one unit. PROMPT_COMMAND only fires after
	// the entire line completes.
	line := fmt.Sprintf(" PROMPT_COMMAND='__pc(){ printf \"%%s\\n\" \"$1\" >&3; unset PROMPT_COMMAND; };__pc $?'; %s%s", prefix, command)
	_, err := w.ptyFile.Write([]byte(line + "\n"))
	return err
}

func (w *BashWorker) readAvailableLocked(timeout time.Duration) (string, error) {
	if w.ptyFile == nil {
		return "", nil
	}

	var buf bytes.Buffer
	currentTimeout := timeout
	for {
		chunk, err := readChunkWithTimeout(w.ptyFile, currentTimeout)
		if err != nil {
			if errors.Is(err, errReadTimeout) {
				break
			}
			return buf.String(), err
		}
		buf.Write(chunk)
		currentTimeout = 50 * time.Millisecond
	}
	return buf.String(), nil
}

func (w *BashWorker) drainControlLocked() {
	for {
		_, ok, err := w.readControlCodeLocked(10 * time.Millisecond)
		if err != nil || !ok {
			return
		}
	}
}

func (w *BashWorker) readControlCodeLocked(timeout time.Duration) (int, bool, error) {
	if w.ctlRead == nil {
		return 0, false, nil
	}
	// If we already have a full line buffered, parse it immediately.
	if code, ok := parseControlBuffer(&w.ctlBuf); ok {
		return code, true, nil
	}
	chunk, err := readChunkWithTimeout(w.ctlRead, timeout)
	if err != nil {
		if errors.Is(err, errReadTimeout) {
			return 0, false, nil
		}
		return 0, false, err
	}
	w.ctlBuf.Write(chunk)
	if code, ok := parseControlBuffer(&w.ctlBuf); ok {
		return code, true, nil
	}
	return 0, false, nil
}

func parseControlBuffer(buf *bytes.Buffer) (int, bool) {
	data := buf.String()
	idx := strings.IndexByte(data, '\n')
	if idx < 0 {
		return 0, false
	}
	line := strings.TrimSpace(data[:idx])
	rest := data[idx+1:]
	buf.Reset()
	buf.WriteString(rest)
	if line == "" {
		return 0, false
	}
	code, err := strconv.Atoi(line)
	if err != nil {
		return 0, false
	}
	return code, true
}

func (w *BashWorker) readUntilDoneLocked(code, execID string, onOutput outputCallback, onStdin stdinCallback, raw bool) (string, int, error) {
	if w.ptyFile == nil || w.ctlRead == nil {
		return "", 0, errors.New("bash process not started")
	}

	visibleAccumulated := ""
	idleCount := 0
	lastInputVisibleLength := 0
	codeLines := splitNonEmptyTrimmedLines(code)
	codeLineSet := make(map[string]struct{}, len(codeLines))
	for _, line := range codeLines {
		codeLineSet[line] = struct{}{}
	}

	promptPatterns := []string{
		": ", "? ", "> ", "] ", ") ",
		":", "?",
		"password:", "Password:", "PASSWORD:",
		"[y/n]", "[Y/n]", "[y/N]", "[Y/N]",
		"(y/n)", "(Y/n)", "(y/N)", "(Y/N)",
		"(yes/no)", "(Yes/No)", "(YES/NO)",
	}

	for {
		if exitCode, ok, err := w.readControlCodeLocked(0); err != nil {
			return visibleAccumulated, 0, err
		} else if ok {
			// small final drain for residual PTY output after prompt hook fires
			if tail, _ := w.readAvailableLocked(30 * time.Millisecond); tail != "" {
				piece := tail
				if !raw {
					piece = filterVisibleChunk(piece, codeLineSet)
				}
				if piece != "" {
					visibleAccumulated += piece
					if onOutput != nil && !raw {
						onOutput("stdout", piece, visibleAccumulated)
					}
				}
			}
			stdout := strings.TrimRight(rawRightVisible(visibleAccumulated), "\n")
			return stdout, exitCode, nil
		}

		chunk, err := readChunkWithTimeout(w.ptyFile, 100*time.Millisecond)
		if err == nil {
			idleCount = 0
			piece := string(chunk)
			if !raw {
				piece = filterVisibleChunk(piece, codeLineSet)
			}
			if piece != "" {
				visibleAccumulated += piece
				if onOutput != nil && !raw {
					onOutput("stdout", piece, visibleAccumulated)
				}
			}
			continue
		}

		if !errors.Is(err, errReadTimeout) {
			return visibleAccumulated, 0, err
		}

		idleCount++
		if onStdin == nil || idleCount < 5 {
			continue
		}

		needsInput := false
		cleanOutput := strings.TrimRight(stripANSI(visibleAccumulated), "\r\n")
		detectedPrompt := lastLine(cleanOutput)
		if waiting, prompt := processWaitingForInput(w.cmd.Process.Pid, detectedPrompt); waiting {
			needsInput = true
			detectedPrompt = prompt
		}
		if !needsInput && cleanOutput != "" && len(visibleAccumulated) > lastInputVisibleLength {
			for _, pattern := range promptPatterns {
				if strings.HasSuffix(cleanOutput, pattern) {
					needsInput = true
					break
				}
			}
		}
		if !needsInput {
			continue
		}

		request := StdinRequest{Prompt: detectedPrompt, Password: strings.Contains(strings.ToLower(detectedPrompt), "password"), ExecID: execID}
		userInput, inputErr := onStdin(request)
		if inputErr != nil {
			if errors.Is(inputErr, ErrInputCancelled) {
				return visibleAccumulated, 0, ErrInputCancelled
			}
			return visibleAccumulated, 0, inputErr
		}
		userInput = strings.TrimRight(userInput, "\r\n") + "\r"
		if _, err := w.ptyFile.Write([]byte(userInput)); err != nil {
			return visibleAccumulated, 0, err
		}
		lastInputVisibleLength = len(visibleAccumulated)
		idleCount = 0
	}
}

func (w *BashWorker) compgenLocked(command string) []string {
	output, _, err := w.runProbeLocked(command)
	if err != nil {
		return []string{}
	}
	lines := splitNonEmptyLines(output)
	results := make([]string, 0, len(lines))
	seen := map[string]struct{}{}
	for _, line := range lines {
		candidate := strings.TrimSpace(line)
		if candidate == "" {
			continue
		}
		if looksLikePath(candidate) && w.pathIsDirLocked(candidate) && !strings.HasSuffix(candidate, "/") {
			candidate += "/"
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		results = append(results, candidate)
	}
	return results
}

func (w *BashWorker) collectVariablesLocked(filterPattern string) []Variable {
	pattern, err := compileOptionalPattern(filterPattern)
	if err != nil {
		return []Variable{}
	}

	command := `while IFS= read -r name; do printf '%s\x1f%s\x1e' "$name" "${!name}"; done < <(compgen -A variable | LC_ALL=C sort)`
	output, _, err := w.runProbeLocked(command)
	if err != nil {
		return []Variable{}
	}

	records := strings.Split(output, recordSeparator)
	variables := make([]Variable, 0, len(records))
	for _, record := range records {
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, varSeparator, 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		value := strings.ReplaceAll(parts[1], "\r", "")
		if name == "" || strings.HasPrefix(name, "_") || strings.HasPrefix(name, "BASH") {
			continue
		}
		if pattern != nil && !pattern.MatchString(name) {
			continue
		}
		display := truncateString(value, 100)
		var size *string
		if len(value) > 100 {
			s := fmt.Sprintf("%d chars", len(value))
			size = &s
		}
		variables = append(variables, Variable{Name: name, Type: "string", Value: display, Size: size, Expandable: false})
	}

	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	return variables
}

func (w *BashWorker) getVariableValueLocked(name string) (string, bool) {
	if !validVarNameRegexp.MatchString(name) {
		return "", false
	}
	command := fmt.Sprintf(`if [[ -v %s ]]; then printf '\x1e%%s' "${%s}"; fi`, name, name)
	output, _, err := w.runProbeLocked(command)
	if err != nil {
			return "", false
	}
	output = stripShellNoise(output)
	output = strings.TrimLeft(output, "\r\n")
	if !strings.HasPrefix(output, recordSeparator) {
		return "", false
	}
	value := strings.TrimPrefix(output, recordSeparator)
	value = strings.ReplaceAll(value, "\r", "")
	return value, true
}

func (w *BashWorker) commandTypeLocked(name string) string {
	if name == "" {
		return ""
	}
	output, _, err := w.runProbeLocked("type -t -- " + shellQuote(name))
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(output)
	switch value {
	case "builtin", "alias", "function", "file", "keyword":
		return value
	default:
		return ""
	}
}

func (w *BashWorker) pathIsDirLocked(path string) bool {
	path = strings.TrimSuffix(path, "/")
	expanded := path
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	} else if !filepath.IsAbs(path) {
		expanded = filepath.Join(w.cwd, path)
	}
	info, err := os.Stat(expanded)
	return err == nil && info.IsDir()
}

func (w *BashWorker) runProbeLocked(command string) (string, int, error) {
	if err := w.ensureStartedLocked(); err != nil {
		return "", 0, err
	}
	_, _ = w.readAvailableLocked(50 * time.Millisecond)
	w.drainControlLocked()
	// Probes never read stdin, so we can safely write command + control line together.
	if w.ptyFile == nil {
		return "", 0, fmt.Errorf("bash process not started")
	}
	payload := " " + command + "\n printf '%s\\n' \"$?\" >&3\n"
	if _, err := w.ptyFile.Write([]byte(payload)); err != nil {
		return "", 0, err
	}
	return w.readUntilDoneLocked(command, "", nil, nil, true)
}

func (w *BashWorker) killProcessLocked() {
	if w.ptyFile != nil {
		_ = w.ptyFile.Close()
	}
	if w.ctlRead != nil {
		_ = w.ctlRead.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		if pgid, err := syscall.Getpgid(w.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}

		done := make(chan struct{}, 1)
		go func() {
			_, _ = w.cmd.Process.Wait()
			done <- struct{}{}
		}()

		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			if pgid, err := syscall.Getpgid(w.cmd.Process.Pid); err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	w.cmd = nil
	w.ptyFile = nil
	w.ctlRead = nil
	w.ctlBuf.Reset()

	w.interruptMu.Lock()
	w.interruptPty = nil
	w.interruptMu.Unlock()
}

func compileOptionalPattern(filterPattern string) (*regexp.Regexp, error) {
	if filterPattern == "" {
		return nil, nil
	}
	return regexp.Compile(filterPattern)
}

func compileOptionalGlobPattern(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	replacer := strings.NewReplacer(`\*`, `(?s:.*)`, `\?`, `(?s:.)`)
	compiled := "^" + replacer.Replace(regexp.QuoteMeta(pattern)) + "$"
	return regexp.Compile(compiled)
}

func parseHistoryOutput(output string) []HistoryEntry {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(stripShellNoise(output), "\r\n", "\n"), "\r", "\n"), "\n")
	re := regexp.MustCompile(`^\s*\*?\s*(\d+)\*?\s+(.*)$`)
	entries := []HistoryEntry{}
	current := -1
	for _, line := range lines {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		if match := re.FindStringSubmatch(line); len(match) == 3 {
			index, err := strconv.Atoi(match[1])
			if err != nil {
				continue
			}
			entries = append(entries, HistoryEntry{HistoryIndex: index, Code: match[2]})
			current = len(entries) - 1
			continue
		}
		if current >= 0 {
			entries[current].Code += "\n" + line
		}
	}
	return entries
}

func envMapToList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func failureResult(kind, message, stdout string, execCount int, start time.Time) ExecuteResult {
	return ExecuteResult{
		Success: false,
		Stdout:  stdout,
		Stderr:  "",
		Result:  nil,
		Error:   &ExecuteError{Type: kind, Message: message, Traceback: []string{}},
		DisplayData:    []DisplayData{},
		Assets:         []Asset{},
		ExecutionCount: execCount,
		Duration:       int(time.Since(start).Milliseconds()),
	}
}

func typeName(err error) string {
	if err == nil {
		return "Error"
	}
	name := fmt.Sprintf("%T", err)
	name = strings.TrimPrefix(name, "*")
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "Error"
	}
	return name
}

func readChunkWithTimeout(file *os.File, timeout time.Duration) ([]byte, error) {
	fd := int(file.Fd())
	for {
		var rfds unix.FdSet
		fdSet(fd, &rfds)
		tv := unix.NsecToTimeval(timeout.Nanoseconds())
		n, err := unix.Select(fd+1, &rfds, nil, nil, &tv)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return nil, err
		}
		if n == 0 {
			return nil, errReadTimeout
		}
		buf := make([]byte, 4096)
		nRead, readErr := file.Read(buf)
		if readErr != nil {
			if errors.Is(readErr, syscall.EINTR) {
				continue
			}
			return nil, readErr
		}
		return buf[:nRead], nil
	}
}

func fdSet(fd int, set *unix.FdSet) {
	// FdSet.Bits element size varies by platform (int32 on darwin, int64 on linux).
	// unsafe.Sizeof gives us the right width at compile time.
	const bitsPerWord = 8 * int(unsafe.Sizeof(set.Bits[0]))
	index := fd / bitsPerWord
	shift := uint(fd % bitsPerWord)
	set.Bits[index] |= 1 << shift
}

func stripANSI(text string) string {
	return ansiEscapePattern.ReplaceAllString(text, "")
}

func markerSeen(output, marker string) bool {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	return strings.Contains(normalized, "\n"+marker) || strings.HasPrefix(normalized, marker)
}

func parseExitCode(output, marker string) int {
	re := regexp.MustCompile(regexp.QuoteMeta(marker) + `(\d+)`)
	match := re.FindStringSubmatch(output)
	if len(match) < 2 {
		return 0
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return value
}

func filterVisibleChunk(chunk string, codeLineSet map[string]struct{}) string {
	normalized := strings.ReplaceAll(chunk, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		cleanLine := stripShellNoise(line)
		cleanLine = stripANSI(cleanLine)

		// Strip the fd 3 control suffix if it got echoed onto the end of a line.
		// This happens when stty -echo doesn't stick (e.g. .bashrc resets it).
		if idx := strings.Index(cleanLine, " printf '%s\\n' \"$?\" >&3"); idx >= 0 {
			cleanLine = cleanLine[:idx]
		}

		stripped := strings.TrimSpace(cleanLine)
		if _, ok := codeLineSet[stripped]; ok {
			continue
		}
		if stripped == "" {
			continue
		}
		if looksLikePrompt(stripped) || looksLikeMarkerFragment(stripped) {
			continue
		}
		// Skip control lines (fd 3 write, PROMPT_COMMAND setup)
		if strings.Contains(stripped, ">&3") || strings.Contains(stripped, "PROMPT_COMMAND") {
			continue
		}
		filtered = append(filtered, cleanLine)
	}
	return strings.Join(filtered, "\n")
}

func rawRightVisible(s string) string {
	s = strings.ReplaceAll(stripShellNoise(s), "\r\n", "\n")
	return strings.TrimLeft(s, "\r")
}

func stripShellNoise(s string) string {
	s = strings.ReplaceAll(s, "\x1b[?2004h", "")
	s = strings.ReplaceAll(s, "\x1b[?2004l", "")
	return s
}

func splitNonEmptyTrimmedLines(s string) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func splitNonEmptyLines(s string) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// looksLikeMarkerFragment detects partial marker/nonce fragments that
// leak through PTY chunk boundaries. These are typically long digit strings
// or fragments of the wrapped eval command.
func looksLikeMarkerFragment(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Fragments of the wrapped command: "eval '...';" or "__exit_code__" or nonce digits
	if strings.Contains(s, "eval ") && strings.Contains(s, "__exit_code__") {
		return true
	}
	// Pure nonce fragment: just digits (or digits with a few prefix chars from the marker)
	digits := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	// If >60% of the string is digits and it's short, it's likely a nonce fragment
	if len(s) < 80 && digits > 0 && float64(digits)/float64(len(s)) > 0.6 {
		return true
	}
	return false
}

// looksLikePrompt detects bare bash prompt lines like "user@host:~/path$"
// that leak through when PS1 isn't fully suppressed.
func looksLikePrompt(s string) bool {
	s = strings.TrimSpace(s)
	// user@host:path$ pattern
	if strings.Contains(s, "@") && (strings.HasSuffix(s, "$") || strings.HasSuffix(s, "$ ") || strings.HasSuffix(s, "#")) {
		// Must not contain spaces (actual output would)
		if !strings.Contains(strings.TrimRight(s, "$ #"), " ") {
			return true
		}
	}
	return false
}

func looksLikePath(s string) bool {
	return strings.Contains(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~")
}

func isBashKeyword(s string) bool {
	switch s {
	case "cd", "echo", "export", "set", "unset", "source", "alias", "type", "read", "test", "if", "then", "else", "fi", "for", "while", "do", "done", "case", "esac", "function", "return", "exit", "exec", "eval", "trap", "wait", "jobs", "fg", "bg", "kill", "pwd", "pushd", "popd", "dirs", "history", "declare", "local", "readonly", "shift", "getopts", "true", "false":
		return true
	default:
		return false
	}
}

func wordBounds(text string, cursor int) (int, int) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	start := cursor
	end := cursor
	for start > 0 && !strings.ContainsRune(" \t\n|&;(){}[]<>'\"$", rune(text[start-1])) {
		start--
	}
	for end < len(text) && !strings.ContainsRune(" \t\n|&;(){}[]<>'\"$", rune(text[end])) {
		end++
	}
	return start, end
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

func lastLine(s string) string {
	parts := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(parts) == 0 {
		return s
	}
	return parts[len(parts)-1]
}

func processWaitingForInput(pid int, fallbackPrompt string) (bool, string) {
	if runtime.GOOS != "linux" {
		return false, fallbackPrompt
	}
	pids := append([]int{pid}, descendantPIDs(pid)...)
	unknown := false
	for _, current := range pids {
		result, ok := processWaitingForStdin(current)
		if !ok {
			unknown = true
			continue
		}
		if result {
			return true, fallbackPrompt
		}
	}
	if unknown {
		return false, fallbackPrompt
	}
	return false, fallbackPrompt
}

func descendantPIDs(pid int) []int {
	childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	data, err := os.ReadFile(childrenPath)
	if err != nil {
		return []int{}
	}
	fields := strings.Fields(string(data))
	children := make([]int, 0, len(fields))
	for _, field := range fields {
		child, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		children = append(children, child)
		children = append(children, descendantPIDs(child)...)
	}
	return children
}

func processWaitingForStdin(pid int) (bool, bool) {
	path := fmt.Sprintf("/proc/%d/syscall", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	content := strings.TrimSpace(string(data))
	if content == "running" {
		return false, true
	}
	parts := strings.Fields(content)
	if len(parts) < 2 {
		return false, false
	}
	syscallNum, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, false
	}
	if syscallNum != 0 {
		return false, true
	}
	fd, err := strconv.ParseInt(parts[1], 0, 64)
	if err != nil {
		return false, false
	}
	return fd == 0, true
}
