package python

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximerivest/rat/internal/kernel"
)

func requirePython(t *testing.T) {
	t.Helper()
	_, _, err := detectPythonCommand("")
	if err != nil {
		t.Skipf("python not available: %v", err)
	}
}

func newPlainTestKernel(t *testing.T, cwd string) *Python {
	t.Helper()
	requirePython(t)
	p, err := New(t.Name(), cwd, "", "")
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() {
		_ = p.Shutdown()
	})
	return p
}

// Top-level await requires IPython's AST transformer. The kernel uses
// plain ast.parse, so top-level await is a syntax error at the kernel
// level. This test verifies the kernel rejects it cleanly.
func TestPythonKernelTopLevelAwaitIsSyntaxError(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	result := p.Run("import asyncio\nawait asyncio.sleep(0)\n42")
	if result.Success {
		t.Fatal("expected syntax error for top-level await")
	}
	if !strings.Contains(result.Error, "SyntaxError") {
		t.Fatalf("error = %q, want SyntaxError", result.Error)
	}
}

func TestPythonKernelAsyncioBehaviors(t *testing.T) {
	t.Run("asyncio.run works from kernel", func(t *testing.T) {
		p := newPlainTestKernel(t, t.TempDir())

		result := p.Run("import asyncio\nasync def f():\n    return 42\nasyncio.run(f())")
		if !result.Success {
			t.Fatalf("Run() failed: %s", result.Error)
		}
		if !strings.Contains(result.Output, "42") {
			t.Fatalf("output = %q, want 42", result.Output)
		}
	})

	t.Run("create_task without running loop fails clearly", func(t *testing.T) {
		p := newPlainTestKernel(t, t.TempDir())

		result := p.Run("import asyncio\nasync def sleeper():\n    await asyncio.sleep(0)\n    return 7\nt = asyncio.create_task(sleeper())")
		if result.Success {
			t.Fatalf("expected create_task without running loop to fail")
		}
		if !strings.Contains(result.Error, "no running event loop") {
			t.Fatalf("error = %q, want no running event loop", result.Error)
		}
	})
}

// IPython magics (%pwd, %timeit, !shell) are handled by the frontend,
// not the kernel. The kernel uses plain ast.parse, so these are syntax
// errors at the kernel level. Frontend tests belong in an integration
// test that exercises the IPython frontend → MCP → kernel path.

func TestPythonKernelStatus(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	status := p.Ctl("status")
	if !strings.Contains(status.Text, "idle") {
		t.Fatalf("status = %q, want idle", status.Text)
	}
	if !strings.Contains(status.Text, "runtime_version: Python ") {
		t.Fatalf("status = %q, want runtime version", status.Text)
	}
}

func TestPythonFrontendRoutesAllMagicsLocally(t *testing.T) {
	// The frontend routes all % and ! prefixed lines to IPython locally,
	// not through the kernel. Verify the routing logic is present.
	if !strings.Contains(frontendScript, `raw_cell.startswith("%")`) {
		t.Fatal("frontend should route %magics locally")
	}
	if !strings.Contains(frontendScript, `raw_cell.startswith("!")`) {
		t.Fatal("frontend should route !shell locally")
	}
}

func TestPythonLookOverviewStillWorksWithIPythonNamespace(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("x = 42")
	look := p.Look(kernel.LookRequest{})
	if !strings.Contains(look.Text, "x") {
		t.Fatalf("overview = %q, want variable x", look.Text)
	}
}

// ── State persistence ──────────────────────────────────────────

func TestPythonStatePersistsAcrossRuns(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	r1 := p.Run("x = 42")
	if !r1.Success {
		t.Fatalf("set x failed: %s", r1.Error)
	}

	r2 := p.Run("x + 8")
	if !r2.Success {
		t.Fatalf("read x failed: %s", r2.Error)
	}
	if !strings.Contains(r2.Output, "50") {
		t.Fatalf("output = %q, want 50", r2.Output)
	}
}

// ── RunResult fields ───────────────────────────────────────────

func TestPythonRunResultFields(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("a = 1")
	r := p.Run("b = 2")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	if r.ExecCount < 2 {
		t.Errorf("ExecCount = %d, want >= 2", r.ExecCount)
	}
	if r.Duration < 0 {
		t.Errorf("Duration = %d, want >= 0", r.Duration)
	}
	if r.Vars < 2 {
		t.Errorf("Vars = %d, want >= 2", r.Vars)
	}
}

// ── Run error cases ────────────────────────────────────────────

func TestPythonRunSyntaxError(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	r := p.Run("def")
	if r.Success {
		t.Fatal("expected syntax error")
	}
	if !strings.Contains(r.Error, "SyntaxError") {
		t.Fatalf("error = %q, want SyntaxError", r.Error)
	}
}

func TestPythonRunRuntimeError(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	r := p.Run("1 / 0")
	if r.Success {
		t.Fatal("expected ZeroDivisionError")
	}
	if !strings.Contains(r.Error, "ZeroDivisionError") {
		t.Fatalf("error = %q, want ZeroDivisionError", r.Error)
	}
}

func TestPythonRunNameError(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	r := p.Run("undefined_variable")
	if r.Success {
		t.Fatal("expected NameError")
	}
	if !strings.Contains(r.Error, "NameError") {
		t.Fatalf("error = %q, want NameError", r.Error)
	}
}

// ── Ctl operations ─────────────────────────────────────────────

func TestPythonCtlReset(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("x = 42")
	result := p.Ctl("reset")
	if !strings.Contains(result.Text, "RESET") {
		t.Fatalf("ctl reset = %q, want RESET", result.Text)
	}

	// Variable should be gone after reset.
	r := p.Run("x")
	if r.Success {
		t.Fatal("expected NameError after reset, got success")
	}
	if !strings.Contains(r.Error, "NameError") {
		t.Fatalf("error = %q, want NameError", r.Error)
	}
}

func TestPythonCtlResetClearsExecCount(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("1")
	_ = p.Run("2")
	p.Ctl("reset")

	r := p.Run("3")
	if r.ExecCount != 1 {
		t.Fatalf("ExecCount after reset = %d, want 1", r.ExecCount)
	}
}

func TestPythonCtlRestart(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("y = 99")
	result := p.Ctl("restart")
	if !strings.Contains(result.Text, "RESTARTED") {
		t.Fatalf("ctl restart = %q, want RESTARTED", result.Text)
	}

	// Variable should be gone after restart.
	r := p.Run("y")
	if r.Success {
		t.Fatal("expected NameError after restart, got success")
	}
}

func TestPythonCtlUnknownOp(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	result := p.Ctl("bogus")
	if !strings.Contains(result.Text, "ERROR") {
		t.Fatalf("ctl bogus = %q, want ERROR", result.Text)
	}
	if !strings.Contains(result.Text, "bogus") {
		t.Fatalf("ctl bogus = %q, want mention of bogus", result.Text)
	}
}

func TestPythonCtlOutput(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	// When idle, partial output should be empty.
	result := p.Ctl("output")
	if result.Text != "" {
		t.Fatalf("ctl output when idle = %q, want empty", result.Text)
	}
}

// ── Look: inspect and completions ──────────────────────────────

func TestPythonLookAt(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("x = [1, 2, 3]")
	look := p.Look(kernel.LookRequest{At: "x"})
	if !strings.Contains(look.Text, "list") {
		t.Fatalf("look at x = %q, want list", look.Text)
	}
	if !strings.Contains(look.Text, "3 items") {
		t.Fatalf("look at x = %q, want 3 items", look.Text)
	}
}

func TestPythonLookAtNotFound(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	look := p.Look(kernel.LookRequest{At: "nonexistent"})
	if !strings.Contains(look.Text, "not found") {
		t.Fatalf("look at nonexistent = %q, want not found", look.Text)
	}
}

func TestPythonLookComplete(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("import os")
	look := p.Look(kernel.LookRequest{Code: "os.pa", Cursor: 5})
	if !strings.Contains(look.Text, "path") {
		t.Fatalf("completions = %q, want os.path", look.Text)
	}
}

func TestPythonLookCompleteNoMatch(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	look := p.Look(kernel.LookRequest{Code: "zzz_no_such_", Cursor: 12})
	if !strings.Contains(look.Text, "No completions") && look.Text != "" {
		t.Fatalf("completions = %q, want No completions or empty", look.Text)
	}
}

func TestPythonLookOverviewEmpty(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	look := p.Look(kernel.LookRequest{})
	if !strings.Contains(look.Text, "0 vars") {
		t.Fatalf("overview = %q, want 0 vars", look.Text)
	}
}

// ── Activity logging ───────────────────────────────────────────

func TestPythonActivityLog(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	logPath := p.ActivityLogPath()
	if logPath == "" {
		t.Fatal("ActivityLogPath() is empty")
	}

	_ = p.Run("print('hello')")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("activity log is empty")
	}

	var entry activityEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("parse activity entry: %v", err)
	}
	if !entry.OK {
		t.Fatalf("entry.OK = false, want true")
	}
	if !strings.Contains(entry.Code, "print") {
		t.Fatalf("entry.Code = %q, want print", entry.Code)
	}
	if !strings.Contains(entry.Output, "hello") {
		t.Fatalf("entry.Output = %q, want hello", entry.Output)
	}
}

func TestPythonActivityLogClearedOnReset(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	_ = p.Run("1 + 1")
	logPath := p.ActivityLogPath()

	// Verify log has content.
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		t.Fatal("expected activity log to have content before reset")
	}

	p.Ctl("reset")

	data, err = os.ReadFile(logPath)
	if err != nil {
		// File might be truncated to 0 or removed — both fine.
		return
	}
	if len(data) != 0 {
		t.Fatalf("activity log not cleared on reset: %d bytes remain", len(data))
	}
}

// ── Shutdown ───────────────────────────────────────────────────

func TestPythonShutdownCleansUp(t *testing.T) {
	requirePython(t)
	p, err := New(t.Name(), t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	scriptPath := p.scriptPath
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("script should exist before shutdown: %v", err)
	}

	if err := p.Shutdown(); err != nil {
		t.Fatalf("Shutdown(): %v", err)
	}

	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("script should be removed after shutdown")
	}
}

// ── Multiline & stdout capture ─────────────────────────────────

func TestPythonPrintCapture(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	r := p.Run("for i in range(3):\n    print(i)")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	for _, want := range []string{"0", "1", "2"} {
		if !strings.Contains(r.Output, want) {
			t.Fatalf("output = %q, want %s", r.Output, want)
		}
	}
}

func TestPythonExprLastLineReturned(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())

	r := p.Run("x = 10\nx * 3")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	if !strings.Contains(r.Output, "30") {
		t.Fatalf("output = %q, want 30", r.Output)
	}
}

// ── CWD ────────────────────────────────────────────────────────

func TestPythonKernelCwd(t *testing.T) {
	cwd := t.TempDir()
	p := newPlainTestKernel(t, cwd)

	r := p.Run("import os; os.getcwd()")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	// Resolve symlinks for macOS /tmp -> /private/tmp etc.
	resolved, _ := filepath.EvalSymlinks(cwd)
	if !strings.Contains(r.Output, cwd) && !strings.Contains(r.Output, resolved) {
		t.Fatalf("output = %q, want cwd %q", r.Output, cwd)
	}
}

// ── detectPythonCommand ────────────────────────────────────────

func TestDetectPythonCommandWithRAT_PYTHON(t *testing.T) {
	py, _, err := detectPythonCommand("")
	if err != nil {
		t.Skip("python not available")
	}

	t.Setenv("RAT_PYTHON", py)
	got, args, err := detectPythonCommand("")
	if err != nil {
		t.Fatalf("detectPythonCommand with RAT_PYTHON: %v", err)
	}
	if got != py {
		t.Fatalf("got %q, want %q", got, py)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want empty", args)
	}
}

func TestDetectPythonCommandWithVenv(t *testing.T) {
	// Create a fake venv with a python binary.
	venv := t.TempDir()
	binDir := filepath.Join(venv, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakePy := filepath.Join(binDir, "python")
	if err := os.WriteFile(fakePy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, _, err := detectPythonCommand(venv)
	if err != nil {
		t.Fatalf("detectPythonCommand with venv: %v", err)
	}
	if got != fakePy {
		t.Fatalf("got %q, want %q", got, fakePy)
	}
}
