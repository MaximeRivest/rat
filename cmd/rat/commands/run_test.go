package commands

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"testing"

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

func TestStdinElicitorAcceptsInput(t *testing.T) {
	e := &stdinElicitor{reader: bufio.NewReader(strings.NewReader("hello\n"))}
	result, err := e.Elicit(context.Background(), mcp.ElicitationRequest{})
	if err != nil {
		t.Fatalf("Elicit: %v", err)
	}
	if result.Action != mcp.ElicitationResponseActionAccept {
		t.Fatalf("action = %q, want accept", result.Action)
	}
	content, ok := result.Content.(map[string]any)
	if !ok {
		t.Fatalf("content type = %T, want map[string]any", result.Content)
	}
	if got, _ := content["text"].(string); got != "hello\n" {
		t.Fatalf("content.text = %q, want %q", got, "hello\n")
	}
}

func TestStdinElicitorCancelsOnEOF(t *testing.T) {
	e := &stdinElicitor{reader: bufio.NewReader(strings.NewReader(""))}
	result, err := e.Elicit(context.Background(), mcp.ElicitationRequest{})
	if err != nil {
		t.Fatalf("Elicit: %v", err)
	}
	if result.Action != mcp.ElicitationResponseActionCancel {
		t.Fatalf("action = %q, want cancel", result.Action)
	}
}
