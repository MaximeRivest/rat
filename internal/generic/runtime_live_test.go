package generic

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/kernel"
)

func newSocketKernel(t *testing.T) *Kernel {
	t.Helper()
	requirePython(t)
	configDir := filepath.Join(testConfigDir(t), "socket")
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

func runAsync(k *Kernel, code string) <-chan kernel.RunResult {
	ch := make(chan kernel.RunResult, 1)
	go func() { ch <- k.Run(code) }()
	return ch
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSocketKernelOutputIsTheUsersAndStreams(t *testing.T) {
	k := newSocketKernel(t)
	done := runAsync(k, "import time\nfor i in range(3):\n    print('line', i, flush=True)\n    time.sleep(0.3)")
	waitFor(t, "first line while the run goes on", func() bool {
		return strings.Contains(k.Ctl("output").Text, "line 0")
	})
	select {
	case r := <-done:
		t.Fatalf("run ended before its output was seen streaming: %+v", r)
	default:
	}
	r := <-done
	if !r.Success || r.Output != "line 0\nline 1\nline 2" {
		t.Fatalf("result = %+v", r)
	}
}

func TestSocketKernelStderrAndValue(t *testing.T) {
	k := newSocketKernel(t)
	r := k.Run("import sys; sys.stderr.write('warned\\n'); sys.stderr.flush()")
	if !r.Success || !strings.Contains(r.Output, "warned") {
		t.Fatalf("stderr not in output: %+v", r)
	}
	if r := k.Run("6 * 7"); r.Output != "42" {
		t.Fatalf("value = %+v", r)
	}
}

func TestSocketKernelAnswersPrompt(t *testing.T) {
	k := newSocketKernel(t)
	done := runAsync(k, "name = ask('Your name? ')")
	waitFor(t, "the prompt", k.IsWaitingForInput)
	if p := k.InputPrompt(); p.Text != "Your name? " || p.Seq == 0 {
		t.Fatalf("prompt = %+v", p)
	}
	if st := k.Ctl("status").Text; st != "waiting_for_input" {
		t.Fatalf("status = %q", st)
	}
	if err := k.SendInput("Ada\n"); err != nil {
		t.Fatal(err)
	}
	if r := <-done; !r.Success {
		t.Fatalf("run = %+v", r)
	}
	if k.IsWaitingForInput() {
		t.Fatal("still waiting after the run")
	}
	if r := k.Run("name"); r.Output != "'Ada'" {
		t.Fatalf("name = %+v", r)
	}
}

func TestSocketKernelCancelKeepsVariables(t *testing.T) {
	k := newSocketKernel(t)
	if r := k.Run("x = 7"); !r.Success {
		t.Fatal(r.Error)
	}
	done := runAsync(k, "import time\nwhile True: time.sleep(0.05)")
	waitFor(t, "the run", func() bool { return k.Ctl("status").Text == "busy" })
	start := time.Now()
	if c := k.Ctl("cancel").Text; c != "CANCELLED" {
		t.Fatalf("cancel = %q", c)
	}
	r := <-done
	if r.Success || !strings.Contains(r.Error, "KeyboardInterrupt") {
		t.Fatalf("cancelled run = %+v", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	if r := k.Run("x"); r.Output != "7" {
		t.Fatalf("variables lost after cancel: %+v", r)
	}
}

func TestCancelBetweenRunsKeepsKernel(t *testing.T) {
	for _, k := range []*Kernel{newMockKernel(t), newSocketKernel(t)} {
		if r := k.Run("y = 3"); !r.Success {
			t.Fatal(r.Error)
		}
		k.Ctl("cancel")
		if r := k.Run("y"); !strings.Contains(r.Output, "3") {
			t.Fatalf("%s: idle cancel lost the kernel: %+v", k.display, r)
		}
	}
}

func TestKillModeCancelStartsFreshNextRun(t *testing.T) {
	k := newMockKernel(t)
	done := runAsync(k, "import time\ntime.sleep(30)")
	waitFor(t, "the run", func() bool { return k.Ctl("status").Text == "busy" })
	if c := k.Ctl("cancel").Text; !strings.HasPrefix(c, "CANCELLED") {
		t.Fatalf("cancel = %q", c)
	}
	if r := <-done; r.Success {
		t.Fatalf("killed run succeeded: %+v", r)
	}
	if r := k.Run("1 + 1"); !r.Success || !strings.Contains(r.Output, "2") {
		t.Fatalf("run after kill = %+v", r)
	}
}

func TestRestartWhileRunning(t *testing.T) {
	k := newSocketKernel(t)
	done := runAsync(k, "import time\ntime.sleep(60)")
	waitFor(t, "the run", func() bool { return k.Ctl("status").Text == "busy" })
	res := make(chan string, 1)
	go func() { res <- k.Ctl("restart").Text }()
	select {
	case txt := <-res:
		if !strings.HasPrefix(txt, "RESTARTED") {
			t.Fatalf("restart = %q", txt)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restart waited for the run")
	}
	if r := <-done; r.Success {
		t.Fatalf("run survived restart: %+v", r)
	}
	if r := k.Run("2 + 2"); r.Output != "4" {
		t.Fatalf("after restart = %+v", r)
	}
}

func TestLateReplyGoesToNobody(t *testing.T) {
	old := lookTimeout
	lookTimeout = 100 * time.Millisecond
	defer func() { lookTimeout = old }()
	for _, k := range []*Kernel{newMockKernel(t), newSocketKernel(t)} {
		if r := k.Look(kernel.LookRequest{At: "__slow__"}); !strings.Contains(r.Text, "no reply") {
			t.Fatalf("%s: slow look = %q", k.display, r.Text)
		}
		lookTimeout = old
		r := k.Look(kernel.LookRequest{})
		if strings.Contains(r.Text, "slow reply") || !strings.Contains(r.Text, "idle") {
			t.Fatalf("%s: next look got %q", k.display, r.Text)
		}
		lookTimeout = 100 * time.Millisecond
	}
}

func TestStdioKernelAnswersPromptDuringRun(t *testing.T) {
	// SendInput used to wait for the run it was meant for.
	k := newMockKernel(t)
	done := runAsync(k, "__ask__")
	waitFor(t, "the prompt", k.IsWaitingForInput)
	if err := k.SendInput("Bo\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		// The kernel's {"ok": true} for the answer is not the run's reply.
		if !r.Success || r.Output != "hello Bo" {
			t.Fatalf("run = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run never ended")
	}
}

func TestSocketKernelThatDiesSaysWhy(t *testing.T) {
	k := newSocketKernel(t)
	r := k.Run("import sys, os\nsys.stderr.write('fatal: boom\\n'); sys.stderr.flush(); os._exit(3)")
	if r.Success || !strings.Contains(r.Error, "exited") {
		t.Fatalf("run = %+v", r)
	}
	if r := k.Run("1 + 2"); r.Output != "3" {
		t.Fatalf("no fresh kernel after a crash: %+v", r)
	}
}
