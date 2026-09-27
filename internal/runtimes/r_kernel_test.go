package runtimes_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/kernel"
)

// newRKernel starts the built-in R kernel. Skipped without R and jsonlite.
func newRKernel(t *testing.T) *generic.Kernel {
	t.Helper()
	rscript, err := exec.LookPath("Rscript")
	if err != nil {
		t.Skip("Rscript not available")
	}
	if exec.Command(rscript, "-e", "library(jsonlite)").Run() != nil {
		t.Skip("R package jsonlite not available")
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "r")
	cfg, err := generic.LoadConfig(filepath.Join(dir, "runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	k, err := generic.New("rtest-"+t.Name(), t.TempDir(), cfg, dir, "", nil)
	if err != nil {
		t.Fatalf("start R kernel: %v", err)
	}
	t.Cleanup(func() { _ = k.Shutdown() })
	return k
}

func run(t *testing.T, k *generic.Kernel, code string) kernel.RunResult {
	t.Helper()
	return k.Run(code)
}

func TestRKernelPrintsEveryVisibleValueInOrder(t *testing.T) {
	k := newRKernel(t)
	r := run(t, k, "x <- 1:3\nx\nmessage('note')\nsum(x); invisible(9)\nwarning('careful')\n'end'")
	want := "[1] 1 2 3\nnote\n[1] 6\nWarning message:\ncareful\n[1] \"end\""
	if !r.Success || r.Output != want {
		t.Fatalf("output:\n%s\nwant:\n%s\n(%+v)", r.Output, want, r)
	}
}

func TestRKernelErrorStopsTheCellWithItsCalls(t *testing.T) {
	k := newRKernel(t)
	r := run(t, k, "g <- function() h(); h <- function() log('a')\ncat('before\\n')\ng()\ncat('never\\n')")
	if r.Success || strings.Contains(r.Output, "never") || !strings.Contains(r.Output, "before") {
		t.Fatalf("run = %+v", r)
	}
	if !strings.Contains(r.Error, "Error in log(\"a\") : non-numeric argument") || !strings.Contains(r.Error, "Traceback:\n1. g()\n2. h()") {
		t.Fatalf("error = %q", r.Error)
	}
	if r := run(t, k, "stop('boom')"); r.Error != "Error: boom" {
		t.Fatalf("top-level stop = %q", r.Error)
	}
	if r := run(t, k, "x y"); r.Success || !strings.Contains(r.Error, "unexpected symbol") {
		t.Fatalf("parse error = %+v", r)
	}
}

func TestRKernelSurvivesClearingTheWorkspace(t *testing.T) {
	k := newRKernel(t)
	run(t, k, "a <- 1; .hidden <- 2")
	if r := run(t, k, "rm(list = ls(all.names = TRUE)); cat <- function(...) stop('mine'); 1 + 1"); r.Output != "[1] 2" {
		t.Fatalf("run = %+v", r)
	}
	if r := k.Look(kernel.LookRequest{}); !strings.HasPrefix(r.Text, "R idle | 1 vars") {
		t.Fatalf("look = %q", r.Text)
	}
}

func TestRKernelAnnouncesEachFinishedPlotPage(t *testing.T) {
	k := newRKernel(t)
	r := run(t, k, "plot(1:3)\nabline(h = 2)\ncat('between\\n')\nplot(3:1)\ncat('after\\n')")
	marks := regexp.MustCompile(`(?m)^__RAT_PLOT__:(.+\.png)$`).FindAllStringSubmatch(r.Output, -1)
	if !r.Success || len(marks) != 2 {
		t.Fatalf("want 2 plots (abline joins its plot): %+v", r)
	}
	// The first page ends when the second starts, after "between".
	if !regexp.MustCompile(`between\n__RAT_PLOT__:.*\nafter\n__RAT_PLOT__:`).MatchString(r.Output) {
		t.Fatalf("order:\n%s", r.Output)
	}
	if r := run(t, k, "1"); strings.Contains(r.Output, "__RAT_PLOT__") {
		t.Fatalf("a plot of the last cell came again: %q", r.Output)
	}
}

func TestRKernelCancelStopsCodeAndKeepsVariables(t *testing.T) {
	k := newRKernel(t)
	run(t, k, "keep <- 42")
	done := make(chan kernel.RunResult, 1)
	go func() { done <- k.Run("i <- 0; repeat i <- i + 1") }()
	deadline := time.Now().Add(10 * time.Second)
	for k.Ctl("status").Text != "busy" {
		if time.Now().After(deadline) {
			t.Fatal("never busy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	k.Ctl("cancel")
	select {
	case r := <-done:
		if r.Success || r.Error != "Interrupted" {
			t.Fatalf("cancelled run = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the loop")
	}
	if r := run(t, k, "keep"); r.Output != "[1] 42" {
		t.Fatalf("after cancel: %+v", r)
	}
}

func TestRKernelReadlineAsksThroughRat(t *testing.T) {
	k := newRKernel(t)
	done := make(chan kernel.RunResult, 1)
	go func() { done <- k.Run("name <- readline('Name? ')\ncat('hi', name, '\\n')") }()
	deadline := time.Now().Add(10 * time.Second)
	for !k.IsWaitingForInput() {
		if time.Now().After(deadline) {
			t.Fatal("no prompt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p := k.InputPrompt(); p.Text != "Name? " {
		t.Fatalf("prompt = %+v", p)
	}
	if err := k.SendInput("Ada\n"); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Output != "Name? Ada\nhi Ada" {
		t.Fatalf("run = %+v", r)
	}
}

func TestRKernelCompletesExactlyAndHarmlessly(t *testing.T) {
	k := newRKernel(t)
	run(t, k, "df <- data.frame(col1 = 1)")
	r := k.Look(kernel.LookRequest{Code: "x <- 1\ny <- df$co", Cursor: len("x <- 1\ny <- df$co")})
	if r.Completion == nil || r.Completion.Start != len("x <- 1\ny <- ") {
		t.Fatalf("completion = %+v", r)
	}
	if len(r.Completion.Matches) != 1 || r.Completion.Matches[0].Label != "df$col1" {
		t.Fatalf("matches = %+v", r.Completion.Matches)
	}
	// `names<-.POSIXlt` reads as an assignment: completing must not run it.
	k.Look(kernel.LookRequest{Code: "names", Cursor: 5})
	if r := k.Look(kernel.LookRequest{}); !strings.HasPrefix(r.Text, "R idle | 1 vars") {
		t.Fatalf("completion changed the workspace: %q", r.Text)
	}
	if r := k.Look(kernel.LookRequest{At: "file.remove('x')"}); !strings.HasSuffix(r.Text, "not found") {
		t.Fatalf("look ran a call: %q", r.Text)
	}
}

func TestRKernelWidgetIsAnInteractiveDisplay(t *testing.T) {
	k := newRKernel(t)
	if exec.Command("Rscript", "-e", "library(DT)").Run() != nil {
		t.Skip("R package DT not available")
	}
	r := run(t, k, "library(DT)\ncat('before\\n')\ndatatable(head(iris))\ncat('after\\n')")
	m := regexp.MustCompile(`(?s)^before\n__RAT_DISPLAY__:(\S+\.json)\nafter$`).FindStringSubmatch(r.Output)
	if !r.Success || m == nil {
		t.Fatalf("widget = %+v", r)
	}
	raw, _ := os.ReadFile(m[1])
	if !strings.Contains(string(raw), `<script>`) || !strings.Contains(string(raw), "datatables") {
		t.Fatalf("the widget page is not self-contained: %.300s", raw)
	}
}
