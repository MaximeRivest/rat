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
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/maximerivest/rat/internal/idle"
	"github.com/maximerivest/rat/internal/kernel"
)

// New creates an MCP server wired to the given kernel.
//
// Go concept: functions are first-class. server.AddTool takes a function
// as its second argument — the handler. We create closures that capture
// the kernel variable.
func New(name string, k kernel.Kernel, tracker *idle.Tracker) *server.MCPServer {
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
			tracker.Touch()
			if err := k.SendInput(input); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("send input: %v", err)), nil
			}
			return mcp.NewToolResultText("input sent"), nil
		}

		tracker.Touch()
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

		tracker.Touch()
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
		if op != "status" {
			tracker.Touch()
		}
		result := k.Ctl(op)
		if op == "status" {
			result.Text = enrichStatusText(result.Text, tracker)
		}
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

func enrichStatusText(state string, tracker *idle.Tracker) string {
	state = strings.TrimSpace(state)
	if state == "" {
		state = "unknown"
	}
	idleSeconds := int(tracker.IdleFor().Seconds())
	return fmt.Sprintf("%s\nidle_seconds: %d\nmemory_mb: %d\npid: %d", state, idleSeconds, currentMemoryMB(), os.Getpid())
}

// currentMemoryMB returns the total RSS of this process and all its
// descendants (the Go server + the language subprocess tree).
// Works on Linux/WSL (/proc), macOS/BSDs (ps), and Windows (wmic).
// Returns 0 if measurement fails.
func currentMemoryMB() int {
	switch runtime.GOOS {
	case "linux":
		return processTreeMemoryLinux(os.Getpid())
	case "darwin", "freebsd", "openbsd", "netbsd":
		return processTreeMemoryPS(os.Getpid())
	case "windows":
		return processTreeMemoryWMIC(os.Getpid())
	default:
		return 0
	}
}

// ── Shared tree-walk logic ───────────────────────────────────

type procStat struct {
	PID      int
	ParentID int
	RSSKB    int
}

func sumTree(stats []procStat, rootPID int) int {
	children := make(map[int][]int, len(stats))
	memoryKB := make(map[int]int, len(stats))
	for _, stat := range stats {
		children[stat.ParentID] = append(children[stat.ParentID], stat.PID)
		memoryKB[stat.PID] = stat.RSSKB
	}

	seen := make(map[int]bool, len(stats))
	var walk func(int) int
	walk = func(pid int) int {
		if seen[pid] {
			return 0
		}
		seen[pid] = true
		total := memoryKB[pid]
		for _, child := range children[pid] {
			total += walk(child)
		}
		return total
	}

	return walk(rootPID) / 1024
}

// ── Linux: read /proc directly (fast, no exec) ──────────────

func processTreeMemoryLinux(rootPID int) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}

	stats := make([]procStat, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if stat, ok := readProcStatus(pid); ok {
			stats = append(stats, stat)
		}
	}
	return sumTree(stats, rootPID)
}

func readProcStatus(pid int) (procStat, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return procStat{}, false
	}

	stat := procStat{PID: pid}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "PPid:":
			stat.ParentID, _ = strconv.Atoi(fields[1])
		case "VmRSS:":
			stat.RSSKB, _ = strconv.Atoi(fields[1])
		}
	}
	return stat, true
}

// ── macOS / BSD: parse `ps` output ──────────────────────────

func processTreeMemoryPS(rootPID int) int {
	out, err := exec.Command("ps", "-ax", "-o", "pid,ppid,rss").Output()
	if err != nil {
		return 0
	}

	lines := strings.Split(string(out), "\n")
	stats := make([]procStat, 0, len(lines))
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		rss, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		stats = append(stats, procStat{PID: pid, ParentID: ppid, RSSKB: rss})
	}
	return sumTree(stats, rootPID)
}

// ── Windows: parse `wmic` output ────────────────────────────
//
// wmic outputs CSV with columns: Node, ParentProcessId, ProcessId, WorkingSetSize
// WorkingSetSize is in bytes (not KB), so we convert.

func processTreeMemoryWMIC(rootPID int) int {
	out, err := exec.Command("wmic", "process", "get",
		"ParentProcessId,ProcessId,WorkingSetSize", "/FORMAT:CSV").Output()
	if err != nil {
		return 0
	}

	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	stats := make([]procStat, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(strings.TrimSpace(line), ",")
		// CSV columns: Node, ParentProcessId, ProcessId, WorkingSetSize
		if len(fields) < 4 {
			continue
		}
		ppid, err1 := strconv.Atoi(fields[1])
		pid, err2 := strconv.Atoi(fields[2])
		bytes, err3 := strconv.Atoi(fields[3])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		stats = append(stats, procStat{PID: pid, ParentID: ppid, RSSKB: bytes / 1024})
	}
	return sumTree(stats, rootPID)
}
