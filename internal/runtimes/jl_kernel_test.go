package runtimes_test

import (
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

// newJuliaKernel starts the built-in Julia kernel. Skipped without julia.
func newJuliaKernel(t *testing.T) *generic.Kernel {
	t.Helper()
	if _, err := exec.LookPath("julia"); err != nil {
		t.Skip("julia not available")
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "jl")
	cfg, err := generic.LoadConfig(filepath.Join(dir, "runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	k, err := generic.New("jltest-"+t.Name(), t.TempDir(), cfg, dir, "", nil)
	if err != nil {
		t.Fatalf("start Julia kernel: %v", err)
	}
	t.Cleanup(func() { _ = k.Shutdown() })
	return k
}

func TestJuliaKernelCellsBehaveAsTheREPL(t *testing.T) {
	k := newJuliaKernel(t)
	if r := k.Run("x = [1, 2, 3]\nprintln(\"sum \", sum(x))\nlength(x)"); !r.Success || r.Output != "sum 6\n3" {
		t.Fatalf("run = %+v", r)
	}
	if r := k.Run("y = 5;"); r.Output != "" {
		t.Fatalf("a cell ending with ; shows nothing: %+v", r)
	}
	if r := k.Run("ans + 1"); r.Output != "6" {
		t.Fatalf("ans = %+v", r)
	}
	if r := k.Run(`s = "héllo"`); r.Output != `"héllo"` {
		t.Fatalf("unicode = %+v", r)
	}
}

func TestJuliaKernelErrorsShowTheCellsCallsOnly(t *testing.T) {
	k := newJuliaKernel(t)
	r := k.Run("f(v) = g(v); g(v) = v + \"a\"\nf(1)")
	if r.Success || !strings.Contains(r.Error, "MethodError: no method matching +(::Int64, ::String)") {
		t.Fatalf("run = %+v", r)
	}
	if !regexp.MustCompile(`\[1\] g\(v::Int64\)[\s\S]*\[2\] f\(v::Int64\)[\s\S]*\[3\] top-level scope`).MatchString(r.Error) {
		t.Fatalf("trace = %s", r.Error)
	}
	for _, leak := range []string{"RatKernel", "include_string", "boot.jl", "WARNING"} {
		if strings.Contains(r.Error+r.Output, leak) {
			t.Fatalf("%s in %s", leak, r.Error+r.Output)
		}
	}
}

func TestJuliaKernelCancelKeepsVariables(t *testing.T) {
	k := newJuliaKernel(t)
	k.Run("keep = 42")
	done := make(chan kernel.RunResult, 1)
	go func() { done <- k.Run("v = Any[]; while true; push!(v, 1); length(v) > 10^6 && empty!(v); end") }()
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
		if r.Success || r.Error != "InterruptException" {
			t.Fatalf("cancelled = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the loop")
	}
	if r := k.Run("keep"); r.Output != "42" {
		t.Fatalf("after cancel = %+v", r)
	}
}

func TestJuliaKernelReadlineAndCompletionAndLook(t *testing.T) {
	k := newJuliaKernel(t)
	done := make(chan kernel.RunResult, 1)
	go func() { done <- k.Run(`print("Name? "); name = readline(); println("hi ", name)`) }()
	deadline := time.Now().Add(10 * time.Second)
	for !k.IsWaitingForInput() {
		if time.Now().After(deadline) {
			t.Fatal("no prompt")
		}
		time.Sleep(10 * time.Millisecond)
	}
	k.SendInput("Ada\n")
	if r := <-done; r.Output != "Name? Ada\nhi Ada" {
		t.Fatalf("readline = %+v", r)
	}
	k.Run("my_value = 1; d = Dict(\"a\" => 1)")
	code := "z = 1\nmy_va"
	c := k.Look(kernel.LookRequest{Code: code, Cursor: len(code)})
	if c.Completion == nil || c.Completion.Start != len("z = 1\n") || len(c.Completion.Matches) != 1 || c.Completion.Matches[0].Label != "my_value" {
		t.Fatalf("completion = %+v", c)
	}
	o := k.Look(kernel.LookRequest{})
	if !strings.HasPrefix(o.Text, "julia idle | 3 vars") || !strings.Contains(o.Text, "Dict{String,Int64}") {
		t.Fatalf("overview = %q", o.Text)
	}
	if r := k.Look(kernel.LookRequest{At: "rm(\"x\")"}); !strings.HasSuffix(r.Text, "not found") {
		t.Fatalf("look ran a call: %q", r.Text)
	}
}

func TestJuliaKernelInteractiveHTMLIsADisplay(t *testing.T) {
	k := newJuliaKernel(t)
	r := k.Run("struct Chart end\nBase.show(io::IO, ::MIME\"text/html\", ::Chart) = print(io, \"<div id=c></div><script>c.textContent=1</script>\")\nBase.show(io::IO, ::Chart) = print(io, \"Chart()\")\nprintln(\"before\"); display(Chart()); println(\"after\")\nChart()")
	if !regexp.MustCompile(`^before\n__RAT_DISPLAY__:\S+\.json\nafter\n__RAT_DISPLAY__:\S+\.json$`).MatchString(r.Output) {
		t.Fatalf("displays = %+v", r)
	}
	// Static HTML stays text.
	r = k.Run("struct Table end\nBase.show(io::IO, ::MIME\"text/html\", ::Table) = print(io, \"<table></table>\")\nBase.show(io::IO, ::Table) = print(io, \"Table()\")\nTable()")
	if r.Output != "Table()" {
		t.Fatalf("static html = %+v", r)
	}
}
