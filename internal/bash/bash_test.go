package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximerivest/rat/internal/kernel"
)

func TestBashLiveOutputReturnsCleanedPartialOutput(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "run.out")
	if err := os.WriteFile(outPath, []byte("echo hi\nhi\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	b := &Bash{}
	b.setLiveOutput(outPath, "echo hi")
	defer b.clearLiveOutput()

	got := b.liveOutput()
	if got != "hi" {
		t.Fatalf("liveOutput() = %q, want %q", got, "hi")
	}
}

func TestBashCtlOutputReturnsLiveOutput(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "run.out")
	if err := os.WriteFile(outPath, []byte("for i in 1 2; do echo step $i; done\nstep 1\nstep 2\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	b := &Bash{}
	b.setLiveOutput(outPath, "for i in 1 2; do echo step $i; done")
	defer b.clearLiveOutput()

	got := b.Ctl("output").Text
	if !strings.Contains(got, "step 1") || !strings.Contains(got, "step 2") {
		t.Fatalf("Ctl(output) = %q, want streamed steps", got)
	}
}

func TestBashLogActivityAndPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")
	b := &Bash{activityPath: path}

	if got := b.ActivityLogPath(); got != path {
		t.Fatalf("ActivityLogPath() = %q, want %q", got, path)
	}

	b.logActivity("echo hello", kernel.RunResult{Success: true, Output: "hello", ExecCount: 3})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"n":3`) || !strings.Contains(text, `"code":"echo hello"`) || !strings.Contains(text, `"output":"hello"`) {
		t.Fatalf("activity log = %q", text)
	}
}
