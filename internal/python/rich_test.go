package python

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/kernel"
)

func TestPythonRichDisplays(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := newPlainTestKernel(t, t.TempDir())
	// An interactive page (HTML with a script) is a display bundle.
	r := p.Run("class W:\n    def _repr_html_(self):\n        return '<div id=w></div><script>w.textContent=1</script>'\n    def __repr__(self):\n        return 'W()'\nW()")
	m := regexp.MustCompile(`__RAT_DISPLAY__:(\S+\.json)`).FindStringSubmatch(r.Output)
	if !r.Success || m == nil {
		t.Fatalf("interactive html = %+v", r)
	}
	var bundle struct {
		Data map[string]any `json:"data"`
	}
	raw, _ := os.ReadFile(m[1])
	if json.Unmarshal(raw, &bundle) != nil || !strings.Contains(bundle.Data["text/html"].(string), "<script>") || bundle.Data["text/plain"] != "W()" {
		t.Fatalf("bundle = %s", raw)
	}
	// Static HTML with a real text form stays text (a pandas-like table).
	r = p.Run("class T:\n    def _repr_html_(self):\n        return '<table><tr><td>1</td></tr></table>'\n    def __repr__(self):\n        return 'T(1)'\nT()")
	if r.Output != "T(1)" {
		t.Fatalf("static html = %+v", r)
	}
	// display() is there, as in Jupyter, and keeps the order of output.
	r = p.Run("print('before')\ndisplay(W())\nprint('after')")
	if !regexp.MustCompile(`^before\n__RAT_DISPLAY__:\S+\nafter$`).MatchString(r.Output) {
		t.Fatalf("display() = %+v", r)
	}
}

func TestPythonCompletionSaysWhatItReplaces(t *testing.T) {
	p := newPlainTestKernel(t, t.TempDir())
	p.Run("import os\nanswer_value = 1")
	code := "x = 1\nos.pa"
	r := p.Look(kernel.LookRequest{Code: code, Cursor: len(code)})
	if r.Completion == nil || r.Completion.Start != len("x = 1\nos.") {
		t.Fatalf("completion = %+v", r)
	}
	found := false
	for _, m := range r.Completion.Matches {
		found = found || m.Label == "path"
	}
	if !found {
		t.Fatalf("matches = %+v", r.Completion.Matches)
	}
}

func TestPythonInterruptByMessage(t *testing.T) {
	// Windows' path, exercised here: cancel asks the kernel's reader thread.
	t.Setenv("RAT_PY_INTERRUPT", "message")
	p := newPlainTestKernel(t, t.TempDir())
	p.Run("keep = 7")
	done := make(chan kernel.RunResult, 1)
	go func() { done <- p.Run("while True:\n    pass") }()
	time.Sleep(500 * time.Millisecond)
	p.Ctl("cancel")
	select {
	case r := <-done:
		if r.Success || !strings.Contains(r.Error, "KeyboardInterrupt") {
			t.Fatalf("run = %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the message did not interrupt")
	}
	if r := p.Run("keep"); r.Output != "7" {
		t.Fatalf("after = %+v", r)
	}
}
