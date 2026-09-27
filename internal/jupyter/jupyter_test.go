package jupyter

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/kernel"
)

// The client against a real Jupyter kernel: ipykernel ("python3") by
// default, or RAT_TEST_KERNELSPEC (ark, ir, julia-1.12, …) with its
// language's snippets. Skipped when the kernelspec is not installed.

type snippets struct {
	stream, value, wantValue, plot, sleep, keep, readKeep, wantKeep, input, complete string
	completeStart                                                                    int
	completeWant                                                                     string
}

var byLanguage = map[string]snippets{
	"python": {
		stream: "import time\nfor i in range(1, 4):\n    print('tick', i, flush=True)\n    time.sleep(0.4)", value: "6 * 7", wantValue: "42",
		plot:  "import matplotlib.pyplot as plt\nplt.plot([1, 2])\nplt.show()",
		sleep: "import time\ntime.sleep(30)", keep: "keep = 42", readKeep: "keep", wantKeep: "42",
		input: "name = input('Name? ')\nprint('hi', name)", complete: "import os\nos.pa", completeStart: len("import os\nos."), completeWant: "path",
	},
	"R": {
		stream: "for (i in 1:3) { cat('tick', i, '\\n'); Sys.sleep(0.4) }", value: "6 * 7", wantValue: "[1] 42",
		plot:  "plot(1:10)",
		sleep: "Sys.sleep(30)", keep: "keep <- 42", readKeep: "keep", wantKeep: "[1] 42",
		input: "name <- readline('Name? ')\ncat('hi', name, '\\n')", complete: "x <- 1\ndata.fr", completeStart: len("x <- 1\n"), completeWant: "data.frame",
	},
}

func startKernel(t *testing.T) (*Kernel, snippets) {
	t.Helper()
	name := os.Getenv("RAT_TEST_KERNELSPEC")
	if name == "" {
		name = "python3"
	}
	spec, err := FindSpec(name)
	if err != nil {
		t.Skip(err)
	}
	sn, ok := byLanguage[spec.Language]
	if !ok {
		t.Skipf("no test snippets for %s", spec.Language)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t0 := time.Now()
	k, err := New("jtest-"+t.Name(), t.TempDir(), spec, Options{})
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Logf("%s ready in %s", name, time.Since(t0).Round(time.Millisecond))
	t.Cleanup(func() { _ = k.Shutdown() })
	return k, sn
}

func TestJupyterKernel(t *testing.T) {
	k, sn := startKernel(t)

	t0 := time.Now()
	if r := k.Run(sn.value); !r.Success || r.Output != sn.wantValue {
		t.Fatalf("value = %+v", r)
	}
	t.Logf("first cell %s", time.Since(t0).Round(time.Millisecond))

	done := make(chan kernel.RunResult, 1)
	go func() { done <- k.Run(sn.stream) }()
	streamed := false
	for i := 0; i < 200 && !streamed; i++ {
		time.Sleep(10 * time.Millisecond)
		streamed = strings.Contains(k.Ctl("output").Text, "tick 1")
		select {
		case r := <-done:
			t.Fatalf("ended before streaming: %+v", r)
		default:
		}
	}
	if !streamed {
		t.Fatal("no live output")
	}
	if r := <-done; !strings.Contains(r.Output, "tick 3") {
		t.Fatalf("stream = %+v", r)
	}

	if r := k.Run(sn.plot); !r.Success || !regexp.MustCompile(`__RAT_(PLOT|DISPLAY)__:\S+`).MatchString(r.Output) {
		t.Errorf("plot = %+v", r)
	}

	k.Run(sn.keep)
	go func() { done <- k.Run(sn.sleep) }()
	for k.Ctl("status").Text[:4] != "busy" {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	t0 = time.Now()
	k.Ctl("cancel")
	select {
	case r := <-done:
		if r.Success {
			t.Errorf("cancelled run succeeded: %+v", r)
		}
		t.Logf("interrupt took %s: %q", time.Since(t0).Round(time.Millisecond), firstLine(r.Error))
	case <-time.After(10 * time.Second):
		t.Fatal("interrupt did not stop the cell")
	}
	if r := k.Run(sn.readKeep); r.Output != sn.wantKeep {
		t.Errorf("variables after interrupt: %+v", r)
	}

	go func() { done <- k.Run(sn.input) }()
	deadline := time.Now().Add(10 * time.Second)
	for !k.IsWaitingForInput() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !k.IsWaitingForInput() {
		t.Error("no input request")
	} else {
		if p := k.InputPrompt(); p.Text != "Name? " {
			t.Errorf("prompt = %+v", p)
		}
		_ = k.SendInput("Ada\n")
	}
	select {
	case r := <-done:
		if !strings.Contains(r.Output, "hi Ada") {
			t.Errorf("input run = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Error("input run never ended")
	}

	c := k.Look(kernel.LookRequest{Code: sn.complete, Cursor: len(sn.complete)})
	found := false
	if c.Completion != nil {
		for _, m := range c.Completion.Matches {
			found = found || m.Label == sn.completeWant
		}
	}
	if c.Completion == nil || c.Completion.Start != sn.completeStart || !found {
		t.Errorf("completion = %q %+v", c.Text, c.Completion)
	}
	if r := k.Look(kernel.LookRequest{At: sn.readKeep}); !strings.Contains(r.Text, "42") {
		t.Logf("inspect %s = %q", sn.readKeep, r.Text)
	}
}

func firstLine(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }
