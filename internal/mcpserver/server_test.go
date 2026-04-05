package mcpserver

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/kernel"
)

// resultText extracts the text from a CallToolResult.
func resultText(r *mcp.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ── formatRunResult ─────────────────────────────────────────

func TestFormatRunResultSuccess(t *testing.T) {
	r := formatRunResult(kernel.RunResult{
		Success: true, Output: "42", Duration: 10, Vars: 1,
	})
	if r.IsError {
		t.Fatal("expected success")
	}
	text := resultText(r)
	if !strings.Contains(text, "42") {
		t.Fatalf("text = %q, want 42", text)
	}
	if !strings.Contains(text, "✓ 10ms | 1 var") {
		t.Fatalf("text = %q, want hint", text)
	}
}

func TestFormatRunResultError(t *testing.T) {
	r := formatRunResult(kernel.RunResult{
		Success: false, Error: "NameError: x not defined", Duration: 5,
	})
	if !r.IsError {
		t.Fatal("expected error")
	}
	text := resultText(r)
	if !strings.Contains(text, "NameError") {
		t.Fatalf("text = %q, want NameError", text)
	}
	if !strings.Contains(text, "✗") {
		t.Fatalf("text = %q, want ✗", text)
	}
}

func TestFormatRunResultEmptyError(t *testing.T) {
	r := formatRunResult(kernel.RunResult{Success: false})
	text := resultText(r)
	if !strings.Contains(text, "execution failed") {
		t.Fatalf("text = %q, want execution failed", text)
	}
}

func TestFormatRunResultPlural(t *testing.T) {
	r := formatRunResult(kernel.RunResult{
		Success: true, Output: "ok", Duration: 100, Vars: 5,
	})
	text := resultText(r)
	if !strings.Contains(text, "5 vars") {
		t.Fatalf("text = %q, want '5 vars'", text)
	}
}

func TestFormatRunResultSingularVar(t *testing.T) {
	r := formatRunResult(kernel.RunResult{
		Success: true, Output: "ok", Duration: 50, Vars: 1,
	})
	text := resultText(r)
	if strings.Contains(text, "1 vars") {
		t.Fatalf("text = %q, want singular 'var' not 'vars'", text)
	}
	if !strings.Contains(text, "1 var") {
		t.Fatalf("text = %q, want '1 var'", text)
	}
}

func TestFormatRunResultDurationSeconds(t *testing.T) {
	r := formatRunResult(kernel.RunResult{
		Success: true, Output: "ok", Duration: 2500,
	})
	text := resultText(r)
	if !strings.Contains(text, "2.5s") {
		t.Fatalf("text = %q, want 2.5s", text)
	}
}

func TestFormatRunResultNoVars(t *testing.T) {
	r := formatRunResult(kernel.RunResult{
		Success: true, Output: "ok", Duration: 10, Vars: 0,
	})
	text := resultText(r)
	if !strings.Contains(text, "✓ 10ms") {
		t.Fatalf("text = %q, want '✓ 10ms'", text)
	}
	if strings.Contains(text, "var") {
		t.Fatalf("text = %q, should not mention vars when 0", text)
	}
}

// ── formatHint ──────────────────────────────────────────────

func TestFormatHint(t *testing.T) {
	tests := []struct {
		ok   bool
		ms   int
		vars int
		want string
	}{
		{true, 42, 3, "✓ 42ms | 3 vars"},
		{false, 1500, 1, "✗ 1.5s | 1 var"},
		{true, 0, 0, "✓ 0ms"},
		{true, 999, 0, "✓ 999ms"},
		{true, 1000, 0, "✓ 1.0s"},
		{true, 50, 10, "✓ 50ms | 10 vars"},
	}
	for _, tt := range tests {
		got := formatHint(tt.ok, tt.ms, tt.vars)
		if got != tt.want {
			t.Errorf("formatHint(%v, %d, %d) = %q, want %q", tt.ok, tt.ms, tt.vars, got, tt.want)
		}
	}
}

// ── cleanOutput ─────────────────────────────────────────────

func TestCleanOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", "hello"},
		{"ANSI colors", "\x1b[31mred\x1b[0m", "red"},
		{"CR keeps last frame", "50%\r100%", "100%"},
		{"multiline with CR", "line1\nfoo\rbar\nline3", "line1\nbar\nline3"},
		{"no CR passthrough", "a\nb", "a\nb"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanOutput(tt.in)
			if got != tt.want {
				t.Fatalf("cleanOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ── stripANSI ───────────────────────────────────────────────

func TestStripANSI(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"\x1b[31mhello\x1b[0m", "hello"},
		{"\x1b[1;32;40mbold\x1b[0m", "bold"},
		{"clean", "clean"},
		{"\x1b[?25h\x1b[?25l", ""},
		{"\x1b]0;title\x07rest", "rest"},
		{"\x1b[38;5;196mcolor\x1b[0m", "color"},
	}
	for _, tt := range tests {
		got := stripANSI(tt.in)
		if got != tt.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
