package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(out)
}

func TestTrimAlreadyPrinted(t *testing.T) {
	got := trimAlreadyPrinted("step 0\nstep 1\n\n✓ 10ms", "step 0\n")
	want := "step 1\n\n✓ 10ms"
	if got != want {
		t.Fatalf("trimAlreadyPrinted() = %q, want %q", got, want)
	}
}

// fakeSender records what a host sends to the kernel.
type fakeSender struct {
	mu     sync.Mutex
	inputs []string
	ctl    []string
}

func (f *fakeSender) SendInput(_ context.Context, text string) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, text)
	return mcp.NewToolResultText("input sent"), nil
}

func (f *fakeSender) Ctl(_ context.Context, op string) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctl = append(f.ctl, op)
	return mcp.NewToolResultText("CANCELLED"), nil
}

func (f *fakeSender) snapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...), append([]string(nil), f.ctl...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestTerminalHostSendsTheLine(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	w.WriteString("Alice\n")
	w.Close()
	h := newTerminalHost(r, io.Discard)
	s := &fakeSender{}
	h.inputRequest(context.Background(), s, "name: ", false)
	inputs, ctl := s.snapshot()
	if len(inputs) != 1 || inputs[0] != "Alice\n" || len(ctl) != 0 {
		t.Fatalf("inputs=%q ctl=%q", inputs, ctl)
	}
}

func TestTerminalHostCancelsWhenStdinIsClosed(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	w.Close()
	h := newTerminalHost(r, io.Discard)
	s := &fakeSender{}
	h.inputRequest(context.Background(), s, "name: ", false)
	inputs, ctl := s.snapshot()
	if len(inputs) != 0 || len(ctl) != 1 || ctl[0] != "cancel" {
		t.Fatalf("inputs=%q ctl=%q, want a cancel and no input", inputs, ctl)
	}
}

func TestEventsHostSpeaksJSONLines(t *testing.T) {
	inR, inW := io.Pipe()
	var out strings.Builder
	var outMu sync.Mutex
	h := newEventsHost(inR, writerFunc(func(p []byte) (int, error) { outMu.Lock(); defer outMu.Unlock(); return out.Write(p) }))
	s := &fakeSender{}
	h.start(context.Background(), s)

	h.output("Your name: ")
	h.inputRequest(context.Background(), s, "Your name: ", false)
	inW.Write([]byte(`{"input":"Alice"}` + "\n"))
	waitFor(t, func() bool { in, _ := s.snapshot(); return len(in) == 1 })
	h.inputDone()
	inW.Write([]byte("not json\n"))
	inW.Write([]byte(`{"cancel":true}` + "\n"))
	waitFor(t, func() bool { _, c := s.snapshot(); return len(c) == 1 })
	h.result(mcp.NewToolResultText("hi Alice"))

	inputs, ctl := s.snapshot()
	if inputs[0] != "Alice" || ctl[0] != "cancel" {
		t.Fatalf("inputs=%q ctl=%q", inputs, ctl)
	}
	outMu.Lock()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	outMu.Unlock()
	var kinds []string
	for _, l := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("stdout line is not JSON: %q", l)
		}
		kinds = append(kinds, ev["event"].(string))
	}
	want := "output input_request input_done warning result"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("events = %v, want %s", kinds, want)
	}
}

func TestEventsHostCancelsAPromptOnceStdinIsClosed(t *testing.T) {
	h := newEventsHost(strings.NewReader(""), io.Discard)
	s := &fakeSender{}
	h.start(context.Background(), s)
	waitFor(t, func() bool { h.stdinMu.Lock(); defer h.stdinMu.Unlock(); return h.stdinClosed })
	h.inputRequest(context.Background(), s, "name: ", false)
	if _, ctl := s.snapshot(); len(ctl) != 1 {
		t.Fatalf("ctl=%q, want one cancel", ctl)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestEchoFilterDropsTheTypedAnswerOnce(t *testing.T) {
	var f echoFilter
	f.expect("Alice\n", false)
	if got := f.filter("Alice\nhello Alice\n"); got != "hello Alice\n" {
		t.Fatalf("got %q", got)
	}
	if got := f.filter("Alice\n"); got != "Alice\n" {
		t.Fatalf("second Alice was dropped too: %q", got)
	}
	f.expect("Bob", false)
	if a, b := f.filter("Bo"), f.filter("b\nnext\n"); a != "" || b != "next\n" {
		t.Fatalf("split echo: %q %q", a, b)
	}
	f.expect("", true)
	if got := f.filter("\nok\n"); got != "ok\n" {
		t.Fatalf("secret: %q", got)
	}
}
