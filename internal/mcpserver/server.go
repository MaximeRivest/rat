// Package mcpserver wraps a kernel.Kernel as an MCP server.
//
// This is the shared MCP layer — same code for bash, Python, R, Julia.
// It registers three tools (run, look, ctl) and delegates to the kernel.
//
// Go concept: this package depends on the kernel.Kernel INTERFACE,
// not on any specific kernel implementation. That's how one MCP server
// serves every language — it doesn't know or care which kernel it talks to.
package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/maximerivest/rat/internal/kernel"
)

// New creates an MCP server wired to the given kernel.
//
// Go concept: functions are first-class. server.AddTool takes a function
// as its second argument — the handler. We create closures that capture
// the kernel variable.
func New(name string, k kernel.Kernel) *server.MCPServer {
	s := server.NewMCPServer(name, "0.1.0",
		server.WithToolCapabilities(true),
		server.WithInstructions(fmt.Sprintf(
			"%s runtime. "+
				"run(code) runs code. "+
				"look() shows variables. look(at='x') inspects x. "+
				"look(code='df.he', cursor=5) completes. "+
				"ctl(op='reset') resets.",
			name,
		)),
	)

	// ── run ──────────────────────────────────────────────────
	//
	// mcp.NewTool defines the tool's name and JSON schema.
	// The schema tells MCP clients (Claude, mcp2cli) what arguments
	// the tool accepts. mcp.WithString / mcp.Required are builders
	// that construct the JSON schema declaratively.

	runTool := mcp.NewTool("run",
		mcp.WithDescription("Run code or provide input. Streams stdout/stderr."),
		mcp.WithString("code", mcp.Description("Code to execute.")),
		mcp.WithString("input", mcp.Description("Text to send to a waiting input prompt.")),
	)

	s.AddTool(runTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments from the request.
		// Go concept: type assertion. req.Params.Arguments is map[string]any.
		// We assert each value to string with `val, ok := x.(string)`.
		// If the assertion fails, ok is false and val is the zero value ("").
		code, _ := req.GetArguments()["code"].(string)
		input, _ := req.GetArguments()["input"].(string)

		if code == "" && input == "" {
			return mcp.NewToolResultError("provide code or input"), nil
		}

		if input != "" {
			if err := k.SendInput(input); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("send input: %v", err)), nil
			}
			return mcp.NewToolResultText("input sent"), nil
		}

		result := k.Run(code)
		return formatRunResult(result), nil
	})

	// ── look ─────────────────────────────────────────────────

	lookTool := mcp.NewTool("look",
		mcp.WithDescription(
			"See runtime state, inspect a symbol, or get completions. "+
				"No args: variable overview. "+
				"at='df': inspect df. "+
				"code='df.hea' cursor=6: completions.",
		),
		mcp.WithString("at", mcp.Description("Symbol to inspect.")),
		mcp.WithString("code", mcp.Description("Code buffer for completions.")),
		mcp.WithNumber("cursor", mcp.Description("Cursor position in code.")),
	)

	s.AddTool(lookTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		at, _ := args["at"].(string)
		code, _ := args["code"].(string)

		// JSON numbers come as float64 in Go.
		// Go concept: type switch — like isinstance() in Python.
		var cursor int
		switch v := args["cursor"].(type) {
		case float64:
			cursor = int(v)
		case int:
			cursor = v
		}

		result := k.Look(kernel.LookRequest{
			At:     at,
			Code:   code,
			Cursor: cursor,
		})

		return mcp.NewToolResultText(result.Text), nil
	})

	// ── ctl ──────────────────────────────────────────────────

	ctlTool := mcp.NewTool("ctl",
		mcp.WithDescription(
			"Control the runtime. "+
				"op='reset': clear namespace. "+
				"op='cancel': cancel execution. "+
				"op='restart': restart interpreter. "+
				"op='status': show health.",
		),
		mcp.WithString("op", mcp.Required(), mcp.Description("Operation: reset, cancel, restart, status.")),
		mcp.WithString("scope", mcp.Description("Reset scope: namespace (default), all.")),
	)

	s.AddTool(ctlTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		op, _ := req.GetArguments()["op"].(string)
		result := k.Ctl(op)
		return mcp.NewToolResultText(result.Text), nil
	})

	return s
}

// formatRunResult converts a kernel.RunResult to an MCP tool result.
func formatRunResult(r kernel.RunResult) *mcp.CallToolResult {
	if !r.Success {
		text := r.Error
		if text == "" {
			text = "execution failed"
		}
		text += "\n\n" + formatHint(false, r.Duration, r.Vars)
		return mcp.NewToolResultError(text)
	}

	text := cleanOutput(r.Output)
	text += "\n\n" + formatHint(true, r.Duration, r.Vars)
	return mcp.NewToolResultText(text)
}

func formatHint(ok bool, durationMs, vars int) string {
	mark := "✓"
	if !ok {
		mark = "✗"
	}
	var dur string
	if durationMs < 1000 {
		dur = fmt.Sprintf("%dms", durationMs)
	} else {
		dur = fmt.Sprintf("%.1fs", float64(durationMs)/1000)
	}
	if vars > 0 {
		noun := "var"
		if vars != 1 {
			noun = "vars"
		}
		return fmt.Sprintf("%s %s | %d %s", mark, dur, vars, noun)
	}
	return fmt.Sprintf("%s %s", mark, dur)
}

// cleanOutput strips ANSI escapes and processes \r so that
// progress-bar output (tqdm, etc.) shows only the final frame
// and terminal colours don't leak into MCP text results.
func cleanOutput(s string) string {
	s = stripANSI(s)
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.Contains(line, "\r") {
			parts := strings.Split(line, "\r")
			for j := len(parts) - 1; j >= 0; j-- {
				if parts[j] != "" {
					lines[i] = parts[j]
					break
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Z0-9]|[0-9A-Za-z=<>])`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}
