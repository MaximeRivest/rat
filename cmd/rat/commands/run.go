package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/maximerivest/rat/internal/mcpclient"
)

var (
	runEvents  bool
	runTimeout time.Duration
)

func init() {
	runCmd.Flags().BoolVar(&runEvents, "events", false,
		"machine mode: JSON events on stdout (one per line), answers and cancel as JSON lines on stdin")
	runCmd.Flags().DurationVar(&runTimeout, "timeout", 5*time.Minute,
		"stop waiting for the result after this long (0 = no limit); the kernel keeps running")
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:     "run <runtime> '<code>'",
	Short:   "Execute code on a kernel",
	GroupID: "daily",
	Long: `Run code on a kernel.

Resolves the runtime name, auto-starts the kernel if needed, executes
the code, prints output, and exits.

The runtime can be a language (py, sh, r, jl, js) which resolves
to your current project's kernel, or a full name (py@myproject, py-ml).

When the code asks for input (Python's input() or getpass), rat run
reads one line from its own stdin and sends it to the program; a
password prompt reads without echo when stdin is a terminal. If stdin
is closed, the read is cancelled instead of waiting forever.

--events is for programs that host a run (editors, notebook views).
stdout carries one JSON object per line:

  {"event":"started","run_id":"..."}               this run's id in "rat events"
  {"event":"output","text":"..."}                  output as it arrives
  {"event":"input_request","prompt":"...","secret":false}
  {"event":"input_done"}                           the program stopped waiting
  {"event":"result","success":true,"text":"...","result":{...}}
  {"event":"error","message":"..."}                rat could not complete the run

and stdin accepts one JSON object per line:

  {"input":"Alice"}      answer the pending prompt
  {"cancel":true}        interrupt the kernel (variables survive)

Closing stdin means no answers will come: a later prompt is cancelled.

Examples:
  rat run py 'x = 42'
  rat run py 'print(x)'
  rat run sh 'ls -la'
  rat run py@myproject 'df.head()'
  rat run py-ml 'import torch'
  rat run --timeout 0 py 'train()'`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		code := strings.Join(args[1:], " ")

		ctx, cancel := context.Background(), context.CancelFunc(func() {})
		if runTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, runTimeout)
		}
		defer cancel()

		k, action, err := ensureKernel(name)
		if err != nil {
			return err
		}
		printKernelAction(k, action)

		var host runHost
		if runEvents {
			host = newEventsHost(os.Stdin, os.Stdout)
		} else {
			host = newTerminalHost(os.Stdin, os.Stdout)
		}

		// Notifications arrive on the client's reader goroutine; answering
		// a prompt makes another call, so it must happen off that goroutine.
		var session *mcpclient.Session
		sessionReady := make(chan struct{})
		session, err = mcpclient.Connect(ctx, k.Port, mcpclient.ConnectOpts{
			OnNotification: func(n mcp.JSONRPCNotification) {
				switch n.Method {
				case "rat/output":
					if text, ok := n.Params.AdditionalFields["text"].(string); ok && text != "" {
						host.output(text)
					}
				case "rat/input_request":
					prompt, _ := n.Params.AdditionalFields["prompt"].(string)
					secret, _ := n.Params.AdditionalFields["secret"].(bool)
					go func() {
						<-sessionReady
						host.inputRequest(ctx, session, prompt, secret)
					}()
				case "rat/input_done":
					host.inputDone()
				case "rat/event":
					// The kernel's broadcast of this very run carries the
					// id every other follower sees: hand it to the host so
					// it can tell its own run from the others.
					f := n.Params.AdditionalFields
					if f["kind"] == "run_started" {
						<-sessionReady
						if id, _ := f["run_id"].(string); id != "" && f["caller_id"] == session.SessionID() {
							host.started(id)
						}
					}
				}
			},
		})
		if err != nil {
			return host.fail(err)
		}
		close(sessionReady)
		defer session.Close()
		host.start(ctx, session)

		result, err := session.Run(ctx, code)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("no result after %s (the kernel is still running; `rat cancel %s` interrupts it, --timeout 0 waits without limit)", runTimeout, name)
			}
			return host.fail(err)
		}
		host.result(result)
		if result.IsError {
			os.Exit(1)
		}
		return nil
	},
}

// runHost is who rat run serves: a person at a terminal, or a program
// speaking --events.
type runHost interface {
	start(ctx context.Context, s inputSender)
	started(runID string)
	output(text string)
	inputRequest(ctx context.Context, s inputSender, prompt string, secret bool)
	inputDone()
	result(r *mcp.CallToolResult)
	fail(err error) error
}

// inputSender is the part of the session a host needs to answer or
// interrupt a waiting program.
type inputSender interface {
	SendInput(ctx context.Context, text string) (*mcp.CallToolResult, error)
	Ctl(ctx context.Context, op string) (*mcp.CallToolResult, error)
}

// ── terminal ─────────────────────────────────────────────────────

type terminalHost struct {
	in      *bufio.Reader
	inFile  *os.File // for no-echo password reads when it is a terminal
	out     io.Writer
	mu      sync.Mutex
	printed strings.Builder
	echo    echoFilter
}

// echoFilter drops the kernel's echo of an answer typed at a terminal:
// the terminal already showed it. (The kernel echoes so that a notebook's
// output reads "Your name: Alice"; a person at a terminal would see it
// twice.) The next output that starts with the answer loses that prefix.
type echoFilter struct{ pending string }

func (f *echoFilter) expect(answer string, secret bool) {
	if secret {
		f.pending = "\n" // getpass prints only the newline
		return
	}
	f.pending = strings.TrimRight(strings.ReplaceAll(answer, "\r\n", "\n"), "\n") + "\n"
}

func (f *echoFilter) filter(text string) string {
	if f.pending == "" {
		return text
	}
	switch {
	case strings.HasPrefix(text, f.pending):
		text, f.pending = text[len(f.pending):], ""
	case strings.HasPrefix(f.pending, text):
		f.pending, text = f.pending[len(text):], ""
	default:
		f.pending = ""
	}
	return text
}

func newTerminalHost(in *os.File, out io.Writer) *terminalHost {
	return &terminalHost{in: bufio.NewReader(in), inFile: in, out: out}
}

func (h *terminalHost) start(context.Context, inputSender) {}

func (h *terminalHost) started(string) {}

func (h *terminalHost) output(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.printed.WriteString(text) // the whole text: the result repeats the echo too
	fmt.Fprint(h.out, h.echo.filter(text))
}

// The prompt itself reaches the terminal through the output stream (the
// program printed it), so nothing is printed here — only read.
func (h *terminalHost) inputRequest(ctx context.Context, s inputSender, _ string, secret bool) {
	var line string
	var err error
	typed := h.inFile != nil && term.IsTerminal(int(h.inFile.Fd()))
	if secret && typed {
		var b []byte
		b, err = term.ReadPassword(int(h.inFile.Fd()))
		line = string(b) + "\n"
		fmt.Fprintln(os.Stderr)
	} else {
		line, err = h.in.ReadString('\n')
		if err == io.EOF && line != "" {
			err = nil
		}
	}
	if err != nil {
		// No answer can come (stdin closed): interrupt the read rather
		// than leave the program waiting on nobody.
		_, _ = s.Ctl(ctx, "cancel")
		return
	}
	if typed {
		h.mu.Lock()
		h.echo.expect(line, secret)
		h.mu.Unlock()
	}
	if _, err := s.SendInput(ctx, line); err != nil {
		fmt.Fprintf(os.Stderr, "rat: could not deliver input: %v\n", err)
	}
}

func (h *terminalHost) inputDone() {}

func (h *terminalHost) result(r *mcp.CallToolResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// What the stream did not bring comes with the result — possibly the
	// echo of a typed answer, when the run ended between two ticks.
	text := strings.TrimLeft(h.echo.filter(trimAlreadyPrinted(mcpclient.ExtractText(r), h.printed.String())), "\n")
	if text != "" {
		fmt.Fprintln(h.out, text)
	}
}

func (h *terminalHost) fail(err error) error { return err }

// ── events ───────────────────────────────────────────────────────

type eventsHost struct {
	in  io.Reader
	mu  sync.Mutex
	enc *json.Encoder

	stdinMu     sync.Mutex
	stdinClosed bool
	waiting     bool
}

func newEventsHost(in io.Reader, out io.Writer) *eventsHost {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return &eventsHost{in: in, enc: enc}
}

func (h *eventsHost) emit(v map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.enc.Encode(v) // Encode writes one line and the newline
}

// hostMessage is one line a hosting program writes to rat's stdin.
type hostMessage struct {
	Input  *string `json:"input"`
	Cancel bool    `json:"cancel"`
}

// start reads the host's answers for the whole run and forwards each as
// it comes. The kernel drops an answer that arrives while nothing is
// waiting, so a host answers an input_request, never ahead of one.
func (h *eventsHost) start(ctx context.Context, s inputSender) {
	go func() {
		sc := bufio.NewScanner(h.in)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var m hostMessage
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				h.emit(map[string]any{"event": "warning", "message": "ignored a stdin line that is not JSON: " + err.Error()})
				continue
			}
			switch {
			case m.Cancel:
				_, _ = s.Ctl(ctx, "cancel")
			case m.Input != nil:
				// Answered: closing stdin now must not cancel this prompt.
				h.stdinMu.Lock()
				h.waiting = false
				h.stdinMu.Unlock()
				if _, err := s.SendInput(ctx, *m.Input); err != nil {
					h.emit(map[string]any{"event": "warning", "message": "could not deliver input: " + err.Error()})
				}
			}
		}
		h.stdinMu.Lock()
		h.stdinClosed = true
		wasWaiting := h.waiting
		h.stdinMu.Unlock()
		if wasWaiting {
			_, _ = s.Ctl(ctx, "cancel")
		}
	}()
}

func (h *eventsHost) started(runID string) {
	h.emit(map[string]any{"event": "started", "run_id": runID})
}

func (h *eventsHost) output(text string) {
	h.emit(map[string]any{"event": "output", "text": text})
}

func (h *eventsHost) inputRequest(ctx context.Context, s inputSender, prompt string, secret bool) {
	h.stdinMu.Lock()
	closed := h.stdinClosed
	h.waiting = !closed
	h.stdinMu.Unlock()
	if closed {
		_, _ = s.Ctl(ctx, "cancel")
		return
	}
	h.emit(map[string]any{"event": "input_request", "prompt": prompt, "secret": secret})
}

func (h *eventsHost) inputDone() {
	h.stdinMu.Lock()
	h.waiting = false
	h.stdinMu.Unlock()
	h.emit(map[string]any{"event": "input_done"})
}

func (h *eventsHost) result(r *mcp.CallToolResult) {
	ev := map[string]any{
		"event":   "result",
		"success": !r.IsError,
		"text":    mcpclient.ExtractText(r),
	}
	if r.StructuredContent != nil {
		ev["result"] = r.StructuredContent
	}
	h.emit(ev)
}

func (h *eventsHost) fail(err error) error {
	h.emit(map[string]any{"event": "error", "message": err.Error()})
	return err
}

// trimAlreadyPrinted removes the already-streamed prefix from the final
// tool result text so output isn't printed twice.
func trimAlreadyPrinted(text, printed string) string {
	text = strings.TrimSpace(text)
	printed = strings.TrimSpace(printed)
	if printed == "" {
		return text
	}
	if strings.HasPrefix(text, printed) {
		return strings.TrimSpace(strings.TrimPrefix(text, printed))
	}
	return text
}
