// Package jupyter runs a Jupyter kernel (any kernelspec: ark, ir, julia,
// python3, …) as a rat kernel, speaking Jupyter's wire protocol over
// ZeroMQ directly — no Python, no jupyter_client.
//
// rat's semantics carry over: output streams live (iopub stream
// messages), displays become the same __RAT_PLOT__/__RAT_DISPLAY__ lines
// rat's own kernels print, input prompts are answered through
// SendInput, completion says where the replaced text starts
// (cursor_start), cancel interrupts as the kernelspec says (a signal or
// an interrupt_request) and a second cancel 3 s later stops the kernel.
package jupyter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-zeromq/zmq4"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/kernel"
	"github.com/maximerivest/rat/internal/procutil"
)

// Spec is a kernelspec (kernel.json).
type Spec struct {
	Argv          []string          `json:"argv"`
	DisplayName   string            `json:"display_name"`
	Language      string            `json:"language"`
	Env           map[string]string `json:"env"`
	InterruptMode string            `json:"interrupt_mode"`
	Dir           string            `json:"-"`
}

// Dirs are where kernelspecs live, most specific first, as Jupyter looks.
func Dirs() []string {
	var dirs []string
	for _, p := range filepath.SplitList(os.Getenv("JUPYTER_PATH")) {
		if p != "" {
			dirs = append(dirs, filepath.Join(p, "kernels"))
		}
	}
	if d := os.Getenv("JUPYTER_DATA_DIR"); d != "" {
		dirs = append(dirs, filepath.Join(d, "kernels"))
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs, filepath.Join(home, "Library", "Jupyter", "kernels"))
	case "windows":
		dirs = append(dirs, filepath.Join(os.Getenv("APPDATA"), "jupyter", "kernels"))
	default:
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dirs = append(dirs, filepath.Join(data, "jupyter", "kernels"))
	}
	if runtime.GOOS == "windows" {
		dirs = append(dirs, filepath.Join(os.Getenv("PROGRAMDATA"), "jupyter", "kernels"))
	} else {
		dirs = append(dirs, "/usr/local/share/jupyter/kernels", "/usr/share/jupyter/kernels")
	}
	return dirs
}

// FindSpec reads the kernelspec called name.
func FindSpec(name string) (*Spec, error) {
	for _, d := range Dirs() {
		p := filepath.Join(d, name, "kernel.json")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Spec
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		s.Dir = filepath.Dir(p)
		return &s, nil
	}
	return nil, fmt.Errorf("no Jupyter kernelspec %q (looked in %s)", name, strings.Join(Dirs(), ", "))
}

// Options configure a kernel beyond its kernelspec.
type Options struct {
	// Overview is code whose printed output is the variable overview
	// ("R idle | 3 vars" …); empty: look answers without one.
	Overview string
	// Env is added to the kernel's environment.
	Env map[string]string
}

// ── messages ────────────────────────────────────────────────

type header struct {
	MsgID    string `json:"msg_id"`
	Session  string `json:"session"`
	Username string `json:"username"`
	Date     string `json:"date"`
	MsgType  string `json:"msg_type"`
	Version  string `json:"version"`
}

type message struct {
	Header  header
	Parent  header
	Content map[string]any
}

const delim = "<IDS|MSG>"

// ── the kernel ──────────────────────────────────────────────

// Kernel is a running Jupyter kernel; it implements kernel.Kernel.
type Kernel struct {
	name    string
	cwd     string
	spec    *Spec
	opts    Options
	display string

	mu      sync.Mutex // one request at a time
	sendMu  sync.Mutex
	cmd     *exec.Cmd
	exited  chan struct{}
	key     []byte
	session string
	ctx     context.Context
	cancel  context.CancelFunc
	shell   zmq4.Socket
	control zmq4.Socket
	stdin   zmq4.Socket
	iopub   zmq4.Socket
	iopubCh chan *message
	shellCh chan *message
	ctrlCh  chan *message
	stdinCh chan *message

	executionCount int
	version        string
	executing      atomic.Bool
	pending        atomic.Bool
	waiting        atomic.Bool
	prompt         atomic.Value // kernel.InputPrompt
	inputSeq       atomic.Uint64
	inputParent    atomic.Value // header of the input_request being answered
	interruptedAt  atomic.Int64

	partialMu sync.Mutex
	partial   strings.Builder
	diagMu    sync.Mutex
	diag      bytes.Buffer
}

// New starts the kernel of spec in cwd.
func New(name, cwd string, spec *Spec, opts Options) (*Kernel, error) {
	k := &Kernel{name: name, cwd: cwd, spec: spec, opts: opts, display: spec.DisplayName}
	if k.display == "" {
		k.display = spec.Language
	}
	if err := k.start(); err != nil {
		return nil, err
	}
	return k, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (k *Kernel) start() error {
	ports := make([]int, 5)
	for i := range ports {
		p, err := freePort()
		if err != nil {
			return err
		}
		ports[i] = p
	}
	k.key = []byte(randomHex(32))
	k.session = randomHex(16)
	conn := map[string]any{
		"shell_port": ports[0], "iopub_port": ports[1], "stdin_port": ports[2], "control_port": ports[3], "hb_port": ports[4],
		"ip": "127.0.0.1", "key": string(k.key), "transport": "tcp", "signature_scheme": "hmac-sha256", "kernel_name": filepath.Base(k.spec.Dir),
	}
	dir, err := cachedir.Kernels(k.name)
	if err != nil {
		return err
	}
	connFile := filepath.Join(dir, "jupyter-connection.json")
	data, _ := json.Marshal(conn)
	if err := os.WriteFile(connFile, data, 0o600); err != nil {
		return err
	}

	argv := make([]string, len(k.spec.Argv))
	for i, a := range k.spec.Argv {
		a = strings.ReplaceAll(a, "{connection_file}", connFile)
		a = strings.ReplaceAll(a, "{resource_dir}", k.spec.Dir)
		argv[i] = a
	}
	if len(argv) == 0 {
		return fmt.Errorf("kernelspec %s has no argv", k.spec.Dir)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = k.cwd
	cmd.Env = os.Environ()
	for key, v := range k.spec.Env {
		cmd.Env = append(cmd.Env, key+"="+v)
	}
	for key, v := range k.opts.Env {
		cmd.Env = append(cmd.Env, key+"="+v)
	}
	procutil.HideWindow(cmd)
	if k.interruptBySignal() {
		procutil.OwnProcessGroup(cmd)
		procutil.ChildrenReceiveInterrupts()
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return fmt.Errorf("start %s: %w", k.display, err)
	}
	w.Close()
	k.cmd = cmd
	k.exited = make(chan struct{})
	k.diagMu.Lock()
	k.diag.Reset()
	k.diagMu.Unlock()
	go func() { // the kernel's own stdout/stderr: its log, for errors
		buf := make([]byte, 8192)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				k.diagMu.Lock()
				k.diag.Write(buf[:n])
				if k.diag.Len() > 64<<10 {
					tail := append([]byte(nil), k.diag.Bytes()[k.diag.Len()-(64<<10):]...)
					k.diag.Reset()
					k.diag.Write(tail)
				}
				k.diagMu.Unlock()
			}
			if err != nil {
				r.Close()
				return
			}
		}
	}()
	go func(cmd *exec.Cmd, done chan struct{}) { _ = cmd.Wait(); close(done) }(cmd, k.exited)

	k.ctx, k.cancel = context.WithCancel(context.Background())
	id := zmq4.SocketIdentity(k.session)
	endpoint := func(p int) string { return fmt.Sprintf("tcp://127.0.0.1:%d", p) }
	k.shell = zmq4.NewDealer(k.ctx, zmq4.WithID(id), zmq4.WithDialerRetry(100*time.Millisecond), zmq4.WithDialerMaxRetries(-1))
	k.stdin = zmq4.NewDealer(k.ctx, zmq4.WithID(id), zmq4.WithDialerRetry(100*time.Millisecond), zmq4.WithDialerMaxRetries(-1))
	k.control = zmq4.NewDealer(k.ctx, zmq4.WithID(id), zmq4.WithDialerRetry(100*time.Millisecond), zmq4.WithDialerMaxRetries(-1))
	k.iopub = zmq4.NewSub(k.ctx, zmq4.WithDialerRetry(100*time.Millisecond), zmq4.WithDialerMaxRetries(-1))
	dial := func(s zmq4.Socket, port int) error {
		errCh := make(chan error, 1)
		go func() { errCh <- s.Dial(endpoint(port)) }()
		select {
		case err := <-errCh:
			return err
		case <-k.exited:
			return k.exitError("exited while starting")
		case <-time.After(5 * time.Minute):
			return fmt.Errorf("%s did not open its ports", k.display)
		}
	}
	for _, c := range []struct {
		s    zmq4.Socket
		port int
	}{{k.shell, ports[0]}, {k.iopub, ports[1]}, {k.stdin, ports[2]}, {k.control, ports[3]}} {
		if err := dial(c.s, c.port); err != nil {
			k.kill()
			return err
		}
	}
	if err := k.iopub.SetOption(zmq4.OptionSubscribe, ""); err != nil {
		k.kill()
		return err
	}
	k.iopubCh = k.reader(k.iopub)
	k.shellCh = k.reader(k.shell)
	k.ctrlCh = k.reader(k.control)
	k.stdinCh = k.reader(k.stdin)

	// Ready when it answers kernel_info (asked again until it does: the
	// first request can precede the kernel's own readiness).
	deadline := time.After(5 * time.Minute)
	for {
		id, err := k.send(k.shell, "kernel_info_request", map[string]any{})
		if err != nil {
			k.kill()
			return err
		}
		tick := time.After(2 * time.Second)
	wait:
		for {
			select {
			case m := <-k.shellCh:
				if m.Parent.MsgID == id && m.Header.MsgType == "kernel_info_reply" {
					if li, ok := m.Content["language_info"].(map[string]any); ok {
						k.version = fmt.Sprint(li["name"], " ", li["version"])
					}
					// iopub is a subscription: give it a moment so the
					// first run's output is not lost.
					time.Sleep(200 * time.Millisecond)
					return nil
				}
			case <-tick:
				break wait
			case <-k.exited:
				return k.exitError("exited while starting")
			case <-deadline:
				k.kill()
				return fmt.Errorf("%s did not answer kernel_info within 5 minutes", k.display)
			}
		}
	}
}

func (k *Kernel) interruptBySignal() bool {
	return k.spec.InterruptMode != "message" && runtime.GOOS != "windows"
}

// reader delivers a socket's messages, verified, on a channel.
func (k *Kernel) reader(s zmq4.Socket) chan *message {
	ch := make(chan *message, 256)
	go func() {
		for {
			raw, err := s.Recv()
			if err != nil {
				return
			}
			m, err := k.decode(raw.Frames)
			if err != nil {
				continue
			}
			select {
			case ch <- m:
			case <-k.ctx.Done():
				return
			}
		}
	}()
	return ch
}

func (k *Kernel) sign(parts ...[]byte) string {
	mac := hmac.New(sha256.New, k.key)
	for _, p := range parts {
		mac.Write(p)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (k *Kernel) decode(frames [][]byte) (*message, error) {
	i := 0
	for ; i < len(frames) && string(frames[i]) != delim; i++ {
	}
	if len(frames) < i+6 {
		return nil, fmt.Errorf("short message")
	}
	sig, h, p, md, c := frames[i+1], frames[i+2], frames[i+3], frames[i+4], frames[i+5]
	if !hmac.Equal([]byte(k.sign(h, p, md, c)), sig) {
		return nil, fmt.Errorf("bad signature")
	}
	m := &message{}
	if err := json.Unmarshal(h, &m.Header); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(p, &m.Parent)
	if err := json.Unmarshal(c, &m.Content); err != nil {
		return nil, err
	}
	return m, nil
}

func (k *Kernel) send(s zmq4.Socket, msgType string, content any, parent ...header) (string, error) {
	h := header{MsgID: randomHex(16), Session: k.session, Username: "rat", Date: time.Now().UTC().Format(time.RFC3339Nano), MsgType: msgType, Version: "5.3"}
	hb, _ := json.Marshal(h)
	pb := []byte("{}")
	if len(parent) > 0 {
		pb, _ = json.Marshal(parent[0])
	}
	cb, _ := json.Marshal(content)
	md := []byte("{}")
	k.sendMu.Lock()
	defer k.sendMu.Unlock()
	err := s.Send(zmq4.NewMsgFrom([]byte(delim), []byte(k.sign(hb, pb, md, cb)), hb, pb, md, cb))
	return h.MsgID, err
}

// ── kernel.Kernel ───────────────────────────────────────────

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// Run executes code and waits for its reply and its last output.
func (k *Kernel) Run(code string) kernel.RunResult {
	start := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.alive() {
		k.kill()
		if err := k.start(); err != nil {
			return kernel.RunResult{Success: false, Error: err.Error()}
		}
	}
	k.executionCount++
	count := k.executionCount
	k.partialMu.Lock()
	k.partial.Reset()
	k.partialMu.Unlock()
	k.executing.Store(true)
	k.pending.Store(true)
	defer func() {
		k.executing.Store(false)
		k.pending.Store(false)
		k.waiting.Store(false)
		k.interruptedAt.Store(0)
	}()
	id, err := k.send(k.shell, "execute_request", map[string]any{
		"code": code, "silent": false, "store_history": true, "user_expressions": map[string]any{},
		"allow_stdin": true, "stop_on_error": true,
	})
	if err != nil {
		return kernel.RunResult{Success: false, Error: err.Error(), ExecCount: count}
	}
	var out strings.Builder
	var errText string
	status := ""
	replied, idle := false, false
	add := func(s string) {
		out.WriteString(s)
		k.partialMu.Lock()
		k.partial.WriteString(s)
		k.partialMu.Unlock()
	}
	for !(replied && idle) {
		select {
		case m := <-k.iopubCh:
			if m.Parent.MsgID != id {
				continue
			}
			switch m.Header.MsgType {
			case "stream":
				add(fmt.Sprint(m.Content["text"]))
			case "display_data", "execute_result", "update_display_data":
				add(k.renderDisplay(m.Content, count))
			case "error":
				tb, _ := m.Content["traceback"].([]any)
				var lines []string
				for _, l := range tb {
					lines = append(lines, ansi.ReplaceAllString(fmt.Sprint(l), ""))
				}
				errText = strings.TrimSpace(strings.Join(lines, "\n"))
				if errText == "" {
					errText = fmt.Sprintf("%v: %v", m.Content["ename"], m.Content["evalue"])
				}
			case "status":
				if m.Content["execution_state"] == "idle" {
					idle = true
				}
			}
		case m := <-k.shellCh:
			if m.Parent.MsgID == id && m.Header.MsgType == "execute_reply" {
				replied = true
				status = fmt.Sprint(m.Content["status"])
				if status == "error" && errText == "" {
					errText = fmt.Sprintf("%v: %v", m.Content["ename"], m.Content["evalue"])
				}
			}
		case m := <-k.stdinCh:
			if m.Header.MsgType == "input_request" {
				k.inputParent.Store(m.Header)
				pw, _ := m.Content["password"].(bool)
				k.prompt.Store(kernel.InputPrompt{Text: fmt.Sprint(m.Content["prompt"]), Secret: pw, Seq: k.inputSeq.Add(1)})
				k.waiting.Store(true)
				add(fmt.Sprint(m.Content["prompt"]))
			}
		case <-k.exited:
			return kernel.RunResult{Success: false, Output: strings.TrimSpace(out.String()), Error: k.exitError("exited").Error(),
				ExecCount: count, Duration: int(time.Since(start).Milliseconds())}
		}
	}
	res := kernel.RunResult{Success: status == "ok", Output: strings.TrimSpace(out.String()), ExecCount: count,
		Duration: int(time.Since(start).Milliseconds())}
	if !res.Success {
		res.Error = errText
		if res.Error == "" {
			res.Error = "execution " + status
		}
	}
	return res
}

// renderDisplay turns a display_data/execute_result into what rat's
// own kernels print: text, a PNG plot line, or a display bundle line.
func (k *Kernel) renderDisplay(content map[string]any, count int) string {
	data, _ := content["data"].(map[string]any)
	if data == nil {
		return ""
	}
	str := func(mime string) string {
		switch v := data[mime].(type) {
		case string:
			return v
		case []any: // some kernels split long text in lines
			var b strings.Builder
			for _, x := range v {
				b.WriteString(fmt.Sprint(x))
			}
			return b.String()
		}
		return ""
	}
	text := str("text/plain")
	html := str("text/html")
	interactive := html != "" && strings.Contains(strings.ToLower(html), "<script")
	dir := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "rat", "plots")
	if os.Getenv("XDG_CACHE_HOME") == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache", "rat", "plots")
	}
	_ = os.MkdirAll(dir, 0o755)
	base := filepath.Join(dir, fmt.Sprintf("jupyter-%d-%d-%s", os.Getpid(), count, randomHex(4)))
	if png := str("image/png"); png != "" && !interactive {
		// Padded or not: Ark sends it without the trailing "=".
		raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(strings.Join(strings.Fields(png), ""), "="))
		if err == nil && os.WriteFile(base+".png", raw, 0o644) == nil {
			return "__RAT_PLOT__:" + base + ".png\n"
		}
	}
	if interactive || str("image/svg+xml") != "" || str("image/jpeg") != "" || (html != "" && (text == "" || defaultRepr.MatchString(strings.TrimSpace(text)))) {
		bundle, _ := json.Marshal(map[string]any{"data": data, "metadata": content["metadata"]})
		if os.WriteFile(base+".json.tmp", bundle, 0o644) == nil && os.Rename(base+".json.tmp", base+".json") == nil {
			return "__RAT_DISPLAY__:" + base + ".json\n"
		}
	}
	if text == "" {
		return ""
	}
	return ansi.ReplaceAllString(text, "") + "\n"
}

var defaultRepr = regexp.MustCompile(`^<[\w.]+(?: object)?(?: at 0x[0-9a-fA-F]+)?>$`)

// SendInput answers the prompt the running code waits on.
func (k *Kernel) SendInput(text string) error {
	parent, ok := k.inputParent.Load().(header)
	if !ok || !k.waiting.Load() {
		return fmt.Errorf("nothing waits for input")
	}
	_, err := k.send(k.stdin, "input_reply", map[string]any{"value": strings.TrimRight(text, "\r\n")}, parent)
	k.waiting.Store(false)
	return err
}

// IsWaitingForInput reports a pending input_request.
func (k *Kernel) IsWaitingForInput() bool { return k.waiting.Load() }

// InputPrompt returns what the waiting code asked.
func (k *Kernel) InputPrompt() kernel.InputPrompt {
	if p, ok := k.prompt.Load().(kernel.InputPrompt); ok {
		return p
	}
	return kernel.InputPrompt{}
}

// request sends one shell request and waits for its reply.
func (k *Kernel) request(msgType string, content map[string]any, timeout time.Duration) (*message, error) {
	id, err := k.send(k.shell, msgType, content)
	if err != nil {
		return nil, err
	}
	deadline := time.After(timeout)
	for {
		select {
		case m := <-k.shellCh:
			if m.Parent.MsgID == id {
				return m, nil
			}
		case <-k.iopubCh: // this request's status messages
		case <-k.exited:
			return nil, k.exitError("exited")
		case <-deadline:
			return nil, fmt.Errorf("%s: no reply to %s after %s", k.display, msgType, timeout)
		}
	}
}

// Look: completion (complete_request), inspection (inspect_request), or
// the overview (the runtime's overview code, when it has one).
func (k *Kernel) Look(req kernel.LookRequest) kernel.LookResult {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.alive() {
		return kernel.LookResult{Text: "ERROR: kernel not running"}
	}
	switch {
	case req.Code != "":
		m, err := k.request("complete_request", map[string]any{"code": req.Code, "cursor_pos": req.Cursor}, 30*time.Second)
		if err != nil {
			return kernel.LookResult{Text: "ERROR: " + err.Error()}
		}
		start, _ := m.Content["cursor_start"].(float64)
		var matches []kernel.Match
		var lines []string
		kinds := map[string]string{}
		if md, ok := m.Content["metadata"].(map[string]any); ok {
			if types, ok := md["_jupyter_types_experimental"].([]any); ok {
				for _, t := range types {
					if tm, ok := t.(map[string]any); ok {
						kinds[fmt.Sprint(tm["text"])] = fmt.Sprint(tm["type"])
					}
				}
			}
		}
		if list, ok := m.Content["matches"].([]any); ok {
			for _, x := range list {
				label := fmt.Sprint(x)
				kind := kinds[label]
				if kind == "" {
					kind = "value"
				}
				matches = append(matches, kernel.Match{Label: label, Kind: kind})
				lines = append(lines, fmt.Sprintf("%-20s %s", strings.ReplaceAll(label, " ", ""), kind))
			}
		}
		text := strings.Join(lines, "\n")
		if text == "" {
			text = "No completions."
		}
		return kernel.LookResult{Text: text, Completion: &kernel.Completion{Start: int(start), Matches: matches}}
	case req.At != "":
		m, err := k.request("inspect_request", map[string]any{"code": req.At, "cursor_pos": len([]rune(req.At)), "detail_level": 0}, 30*time.Second)
		if err != nil {
			return kernel.LookResult{Text: "ERROR: " + err.Error()}
		}
		data, _ := m.Content["data"].(map[string]any)
		if found, _ := m.Content["found"].(bool); !found || data == nil {
			return kernel.LookResult{Text: req.At + ": not found"}
		}
		return kernel.LookResult{Text: req.At + ": " + strings.TrimSpace(ansi.ReplaceAllString(fmt.Sprint(data["text/plain"]), ""))}
	default:
		if k.opts.Overview == "" {
			return kernel.LookResult{Text: k.display + " idle | ? vars"}
		}
		id, err := k.send(k.shell, "execute_request", map[string]any{
			"code": k.opts.Overview, "silent": false, "store_history": false, "user_expressions": map[string]any{}, "allow_stdin": false, "stop_on_error": false,
		})
		if err != nil {
			return kernel.LookResult{Text: "ERROR: " + err.Error()}
		}
		var out strings.Builder
		replied, idle := false, false
		deadline := time.After(30 * time.Second)
		for !(replied && idle) {
			select {
			case m := <-k.iopubCh:
				if m.Parent.MsgID != id {
					continue
				}
				if m.Header.MsgType == "stream" {
					out.WriteString(fmt.Sprint(m.Content["text"]))
				} else if m.Header.MsgType == "status" && m.Content["execution_state"] == "idle" {
					idle = true
				}
			case m := <-k.shellCh:
				if m.Parent.MsgID == id {
					replied = true
				}
			case <-k.exited:
				return kernel.LookResult{Text: "ERROR: " + k.exitError("exited").Error()}
			case <-deadline:
				return kernel.LookResult{Text: "ERROR: no overview after 30s"}
			}
		}
		return kernel.LookResult{Text: strings.TrimRight(out.String(), "\n")}
	}
}

// Ctl controls the kernel.
func (k *Kernel) Ctl(op string) kernel.CtlResult {
	switch op {
	case "cancel":
		if !k.pending.Load() {
			return kernel.CtlResult{Text: "CANCELLED"}
		}
		first := k.interruptedAt.Load()
		if first != 0 && time.Since(time.Unix(0, first)) >= 3*time.Second {
			k.killProcess()
			return kernel.CtlResult{Text: "CANCELLED | the code did not stop; kernel stopped, variables lost"}
		}
		if k.interruptBySignal() {
			if k.cmd != nil {
				_ = procutil.InterruptGroup(k.cmd.Process)
			}
		} else if _, err := k.send(k.control, "interrupt_request", map[string]any{}); err != nil {
			return kernel.CtlResult{Text: "ERROR: " + err.Error()}
		}
		k.interruptedAt.CompareAndSwap(0, time.Now().UnixNano())
		return kernel.CtlResult{Text: "CANCELLED"}
	case "reset", "restart":
		k.killProcess()
		k.mu.Lock()
		defer k.mu.Unlock()
		k.kill()
		k.executionCount = 0
		if err := k.start(); err != nil {
			return kernel.CtlResult{Text: "ERROR: " + err.Error()}
		}
		if op == "reset" {
			return kernel.CtlResult{Text: "RESET | namespace cleared | 0 vars"}
		}
		return kernel.CtlResult{Text: "RESTARTED | fresh session"}
	case "output":
		k.partialMu.Lock()
		defer k.partialMu.Unlock()
		return kernel.CtlResult{Text: k.partial.String()}
	case "status":
		state := "idle"
		if k.executing.Load() {
			state = "busy"
			if k.waiting.Load() {
				state = "waiting_for_input"
			}
		}
		if k.version != "" {
			state += "\nruntime_version: " + k.version
		}
		return kernel.CtlResult{Text: state}
	}
	return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'", op)}
}

// Shutdown asks the kernel to stop, then makes sure it did.
func (k *Kernel) Shutdown() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.alive() {
		_, _ = k.send(k.control, "shutdown_request", map[string]any{"restart": false})
		select {
		case <-k.exited:
		case <-time.After(2 * time.Second):
		}
	}
	k.kill()
	return nil
}

func (k *Kernel) alive() bool {
	if k.exited == nil {
		return false
	}
	select {
	case <-k.exited:
		return false
	default:
		return true
	}
}

func (k *Kernel) killProcess() {
	if k.cmd != nil && k.cmd.Process != nil {
		if k.interruptBySignal() {
			_ = procutil.KillGroup(k.cmd.Process)
		} else {
			_ = k.cmd.Process.Kill()
		}
	}
}

func (k *Kernel) kill() {
	k.killProcess()
	if k.exited != nil {
		select {
		case <-k.exited:
		case <-time.After(5 * time.Second):
		}
	}
	if k.cancel != nil {
		k.cancel()
	}
	for _, s := range []zmq4.Socket{k.shell, k.control, k.stdin, k.iopub} {
		if s != nil {
			_ = s.Close()
		}
	}
	k.shell, k.control, k.stdin, k.iopub = nil, nil, nil, nil
}

func (k *Kernel) exitError(what string) error {
	k.diagMu.Lock()
	diag := strings.TrimSpace(k.diag.String())
	k.diagMu.Unlock()
	if diag != "" {
		lines := strings.Split(diag, "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		return fmt.Errorf("%s %s: %s", k.display, what, strings.Join(lines, "\n"))
	}
	return fmt.Errorf("%s %s", k.display, what)
}
