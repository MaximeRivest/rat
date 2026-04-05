package generic

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maximerivest/rat/internal/kernel"
)

func requirePython(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("python not available")
	return ""
}

func testConfigDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata")
}

func newMockKernel(t *testing.T) *Kernel {
	t.Helper()
	requirePython(t)

	configDir := testConfigDir(t)
	cfg, err := LoadConfig(filepath.Join(configDir, "runtime.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	k, err := New(t.Name(), t.TempDir(), cfg, configDir, "", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = k.Shutdown() })
	return k
}

// ── Run basics ──────────────────────────────────────────────

func TestGenericKernelRunBasic(t *testing.T) {
	k := newMockKernel(t)

	r := k.Run("1 + 1")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	if !strings.Contains(r.Output, "2") {
		t.Fatalf("output = %q, want 2", r.Output)
	}
}

func TestGenericKernelRunStatePersists(t *testing.T) {
	k := newMockKernel(t)

	r1 := k.Run("x = 42")
	if !r1.Success {
		t.Fatalf("set x: %s", r1.Error)
	}
	r2 := k.Run("x + 8")
	if !r2.Success {
		t.Fatalf("read x: %s", r2.Error)
	}
	if !strings.Contains(r2.Output, "50") {
		t.Fatalf("output = %q, want 50", r2.Output)
	}
}

func TestGenericKernelRunError(t *testing.T) {
	k := newMockKernel(t)

	r := k.Run("1/0")
	if r.Success {
		t.Fatal("expected error")
	}
	if !strings.Contains(r.Error, "ZeroDivisionError") {
		t.Fatalf("error = %q, want ZeroDivisionError", r.Error)
	}
}

func TestGenericKernelRunSyntaxError(t *testing.T) {
	k := newMockKernel(t)

	r := k.Run("def")
	if r.Success {
		t.Fatal("expected syntax error")
	}
	if !strings.Contains(r.Error, "SyntaxError") {
		t.Fatalf("error = %q, want SyntaxError", r.Error)
	}
}

func TestGenericKernelRunExecCount(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("1")
	r := k.Run("2")
	if r.ExecCount < 2 {
		t.Fatalf("ExecCount = %d, want >= 2", r.ExecCount)
	}
}

func TestGenericKernelRunVars(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("a = 1")
	r := k.Run("b = 2")
	if r.Vars < 2 {
		t.Fatalf("Vars = %d, want >= 2", r.Vars)
	}
}

// ── Streaming (output_chunk) ────────────────────────────────

func TestGenericKernelRunStreaming(t *testing.T) {
	k := newMockKernel(t)

	r := k.Run("__stream__")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	if !strings.Contains(r.Output, "chunk1") {
		t.Fatalf("output = %q, want chunk1", r.Output)
	}
}

// ── Events ──────────────────────────────────────────────────

func TestGenericKernelEventDispatch(t *testing.T) {
	k := newMockKernel(t)

	// Trigger an event via the mock kernel.
	r := k.Run("__emit_event__")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}

	// The event should be written to the activity log.
	data, err := os.ReadFile(k.ActivityLogPath())
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	foundEvent := false
	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["event"] == "test_event" {
			foundEvent = true
			dataMap, ok := entry["data"].(map[string]interface{})
			if !ok || dataMap["msg"] != "hello" {
				t.Fatalf("event data = %v, want {msg: hello}", entry["data"])
			}
		}
	}
	if !foundEvent {
		t.Fatalf("expected test_event in activity log, got:\n%s", string(data))
	}
}

// ── Input request/delivered (skip in streaming) ─────────────

func TestGenericKernelRunSkipsInputMessages(t *testing.T) {
	k := newMockKernel(t)

	r := k.Run("__input_test__")
	if !r.Success {
		t.Fatalf("Run failed: %s", r.Error)
	}
	if !strings.Contains(r.Output, "got input") {
		t.Fatalf("output = %q, want 'got input'", r.Output)
	}
}

// ── Look ────────────────────────────────────────────────────

func TestGenericKernelLookOverviewEmpty(t *testing.T) {
	k := newMockKernel(t)

	look := k.Look(kernel.LookRequest{})
	if !strings.Contains(look.Text, "0 vars") {
		t.Fatalf("overview = %q, want 0 vars", look.Text)
	}
}

func TestGenericKernelLookOverviewWithVars(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("x = 42")
	look := k.Look(kernel.LookRequest{})
	if !strings.Contains(look.Text, "x") {
		t.Fatalf("overview = %q, want x", look.Text)
	}
}

func TestGenericKernelLookAt(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("x = [1, 2, 3]")
	look := k.Look(kernel.LookRequest{At: "x"})
	if !strings.Contains(look.Text, "list") {
		t.Fatalf("look at x = %q, want list", look.Text)
	}
}

func TestGenericKernelLookAtNotFound(t *testing.T) {
	k := newMockKernel(t)

	look := k.Look(kernel.LookRequest{At: "nope"})
	if !strings.Contains(look.Text, "not found") {
		t.Fatalf("look = %q, want not found", look.Text)
	}
}

func TestGenericKernelLookComplete(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("foobar = 1")
	look := k.Look(kernel.LookRequest{Code: "foo", Cursor: 3})
	if !strings.Contains(look.Text, "foobar") {
		t.Fatalf("completions = %q, want foobar", look.Text)
	}
}

func TestGenericKernelLookCompleteNone(t *testing.T) {
	k := newMockKernel(t)

	look := k.Look(kernel.LookRequest{Code: "zzz", Cursor: 3})
	if !strings.Contains(look.Text, "No completions") {
		t.Fatalf("completions = %q, want No completions", look.Text)
	}
}

// ── Ctl ─────────────────────────────────────────────────────

func TestGenericKernelCtlStatus(t *testing.T) {
	k := newMockKernel(t)

	r := k.Ctl("status")
	if !strings.Contains(r.Text, "idle") {
		t.Fatalf("status = %q, want idle", r.Text)
	}
}

func TestGenericKernelCtlReset(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("x = 42")
	r := k.Ctl("reset")
	if !strings.Contains(r.Text, "RESET") {
		t.Fatalf("reset = %q, want RESET", r.Text)
	}

	// State should be cleared after reset.
	r2 := k.Run("x")
	if r2.Success {
		t.Fatal("expected NameError after reset")
	}
}

func TestGenericKernelCtlResetClearsExecCount(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("1")
	_ = k.Run("2")
	k.Ctl("reset")
	r := k.Run("3")
	if r.ExecCount != 1 {
		t.Fatalf("ExecCount after reset = %d, want 1", r.ExecCount)
	}
}

func TestGenericKernelCtlRestart(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("y = 99")
	r := k.Ctl("restart")
	if !strings.Contains(r.Text, "RESTARTED") {
		t.Fatalf("restart = %q, want RESTARTED", r.Text)
	}

	r2 := k.Run("y")
	if r2.Success {
		t.Fatal("expected NameError after restart")
	}
}

func TestGenericKernelCtlUnknownOp(t *testing.T) {
	k := newMockKernel(t)

	r := k.Ctl("bogus")
	if !strings.Contains(r.Text, "ERROR") {
		t.Fatalf("ctl bogus = %q, want ERROR", r.Text)
	}
}

func TestGenericKernelCtlOutput(t *testing.T) {
	k := newMockKernel(t)

	r := k.Ctl("output")
	if r.Text != "" {
		t.Fatalf("ctl output = %q, want empty", r.Text)
	}
}

// ── Activity logging ────────────────────────────────────────

func TestGenericKernelActivityLog(t *testing.T) {
	k := newMockKernel(t)

	path := k.ActivityLogPath()
	if path == "" {
		t.Fatal("ActivityLogPath is empty")
	}

	_ = k.Run("print('hi')")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("activity log is empty")
	}

	var entry activityEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("parse entry: %v", err)
	}
	if !entry.OK {
		t.Fatal("entry.OK = false")
	}
	if !strings.Contains(entry.Code, "print") {
		t.Fatalf("entry.Code = %q, want print", entry.Code)
	}
}

func TestGenericKernelActivityLogClearedOnReset(t *testing.T) {
	k := newMockKernel(t)

	_ = k.Run("1 + 1")
	path := k.ActivityLogPath()

	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		t.Fatal("expected activity log to have content")
	}

	k.Ctl("reset")

	if _, err := os.Stat(path); err == nil {
		data, _ = os.ReadFile(path)
		if len(data) != 0 {
			t.Fatalf("activity log not cleared: %d bytes", len(data))
		}
	}
	// File removed or empty — both fine.
}

// ── Shutdown ────────────────────────────────────────────────

func TestGenericKernelShutdown(t *testing.T) {
	py := requirePython(t)
	_ = py

	configDir := testConfigDir(t)
	cfg, err := LoadConfig(filepath.Join(configDir, "runtime.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	k, err := New(t.Name(), t.TempDir(), cfg, configDir, "", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := k.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// After shutdown, the process should be gone.
	if k.cmd != nil {
		t.Fatal("cmd should be nil after shutdown")
	}
}

// ── Config helpers ──────────────────────────────────────────

func TestLoadConfigFromTestdata(t *testing.T) {
	configDir := testConfigDir(t)
	cfg, err := LoadConfig(filepath.Join(configDir, "runtime.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Name != "mock" {
		t.Fatalf("name = %q, want mock", cfg.Name)
	}
	if cfg.KernelType() != "json" {
		t.Fatalf("KernelType = %q, want json", cfg.KernelType())
	}
	if cfg.FrontendType() != "repl" {
		t.Fatalf("FrontendType = %q, want repl", cfg.FrontendType())
	}
}

func TestKernelScriptPathResolution(t *testing.T) {
	cfg := &RuntimeConfig{}
	cfg.Kernel.Script = "kernel.py"

	got := cfg.KernelScriptPath("/opt/runtimes/r")
	if got != "/opt/runtimes/r/kernel.py" {
		t.Fatalf("KernelScriptPath = %q, want /opt/runtimes/r/kernel.py", got)
	}

	cfg.Kernel.Script = "/abs/path/kernel.py"
	got = cfg.KernelScriptPath("/opt/runtimes/r")
	if got != "/abs/path/kernel.py" {
		t.Fatalf("KernelScriptPath = %q, want /abs/path/kernel.py", got)
	}
}

func TestBridgePathResolution(t *testing.T) {
	cfg := &RuntimeConfig{}
	cfg.Kernel.Bridge = "bridge.sh"

	got := cfg.BridgePath("/opt/runtimes/sh")
	if got != "/opt/runtimes/sh/bridge.sh" {
		t.Fatalf("BridgePath = %q", got)
	}

	cfg.Kernel.Bridge = ""
	if got := cfg.BridgePath("/x"); got != "" {
		t.Fatalf("BridgePath for empty = %q", got)
	}
}

func TestDefaultKeys(t *testing.T) {
	cfg := &RuntimeConfig{}
	if cfg.SubmitKey() != "Enter" {
		t.Fatalf("SubmitKey = %q, want Enter", cfg.SubmitKey())
	}
	if cfg.CancelKey() != "C-c" {
		t.Fatalf("CancelKey = %q, want C-c", cfg.CancelKey())
	}

	cfg.Kernel.Submit = "Return"
	cfg.Kernel.Cancel = "C-g"
	if cfg.SubmitKey() != "Return" {
		t.Fatalf("SubmitKey = %q, want Return", cfg.SubmitKey())
	}
	if cfg.CancelKey() != "C-g" {
		t.Fatalf("CancelKey = %q, want C-g", cfg.CancelKey())
	}
}

func TestDetectBinaryWithEnvOverride(t *testing.T) {
	py := requirePython(t)

	cfg := &RuntimeConfig{
		Display: "Test",
	}
	cfg.Detect.Env = "RAT_TEST_BINARY"
	cfg.Detect.Commands = []string{"nonexistent-binary-xyz"}

	t.Setenv("RAT_TEST_BINARY", py)
	got, err := cfg.DetectBinary()
	if err != nil {
		t.Fatalf("DetectBinary: %v", err)
	}
	if got != py {
		t.Fatalf("got %q, want %q", got, py)
	}
}

func TestDetectBinaryNotFound(t *testing.T) {
	cfg := &RuntimeConfig{
		Display: "FakeLang",
	}
	cfg.Detect.Commands = []string{"nonexistent-binary-xyz"}

	_, err := cfg.DetectBinary()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "FakeLang") {
		t.Fatalf("error = %q, want FakeLang", err.Error())
	}
}
