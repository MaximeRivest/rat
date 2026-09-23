package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/mcpclient"
	"github.com/maximerivest/rat/internal/mcpserver"
	s "github.com/maximerivest/rat/internal/termstyle"
)

var eventsJSON bool

func init() {
	eventsCmd.Flags().BoolVar(&eventsJSON, "json", false, "one JSON object per line (for programs)")
	rootCmd.AddCommand(eventsCmd)
}

// Poll intervals: quick while a run is in progress (output should feel
// live), slower when the kernel is idle (a new run is still noticed in
// well under a second). Polling by sequence number is lossless: nothing
// between two polls is missed, and a reader that fell too far behind is
// told so ("gap") instead of silently missing events.
const (
	eventsPollBusy = 150 * time.Millisecond
	eventsPollIdle = 750 * time.Millisecond
	eventsWaitDown = time.Second
)

var eventsCmd = &cobra.Command{
	Use:     "events <runtime>",
	Short:   "Follow what happens on a kernel",
	GroupID: "daily",
	Long: `Follow a kernel's activity as it happens: every run, from every
client (terminals, agents, editors, notebooks), with who ran it, the
code, its output as it streams, input prompts, and the result.

Never starts a kernel. If it is not running, events says so and waits;
when it restarts, events says so and follows the new process. On
connecting, runs already in progress are reported first (marked
"replay"), then everything new, in order.

--json prints one object per line:

  {"event":"kernel","name":"py@proj","state":"running","pid":123,"boot":"…"}
  {"event":"kernel","name":"py@proj","state":"stopped"}
  {"event":"run_started","run_id":"…","caller":"Maxime (Chattering)","code":"…","seq":7}
  {"event":"run_output","run_id":"…","text":"…"}
  {"event":"run_waiting","run_id":"…","prompt":"Name: ","secret":false}
  {"event":"run_input_done","run_id":"…"}
  {"event":"run_ended","run_id":"…","ok":true,"duration_ms":12,"output":"…","error":""}
      run_output chunks are a live preview, sent every 50 ms: a quick run
      has none. run_ended carries the whole output (and the error): show
      what the chunks did not. run_input_done may be skipped when the run
      ends right after the answer: run_ended also ends any wait.
  {"event":"ctl_called","op":"reset",…}    {"event":"look_called",…}
  {"event":"gap"}   events were missed: resynchronise (the kernel's state is still true)
  {"event":"unsupported","message":"…"}   a kernel started by an older rat: restart it

Callers are named by RAT_CALLER in the environment of the process
that runs code ("Maxime (Chattering)", "Lilly's agent"); unnamed
clients show as "rat".

Examples:
  rat events py
  rat events --doc docs/analysis.md py --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		r, err := resolveInput(args[0])
		if err != nil {
			return err
		}
		var out eventSink = &humanEventSink{w: os.Stdout}
		if eventsJSON {
			out = &jsonEventSink{enc: json.NewEncoder(os.Stdout)}
		}
		followKernel(ctx, r.Name, out)
		return nil
	},
}

// followKernel runs until ctx ends.
func followKernel(ctx context.Context, name string, out eventSink) {
	lastState, lastPID := "", 0
	// A process this follower saw start (after seeing the kernel stopped,
	// or a restart) is followed from its first event: a quick run can
	// begin and end between two checks. A kernel that was already running
	// when the follower arrived is followed from now (runs in progress
	// included), not from its history.
	fromStart := false
	for ctx.Err() == nil {
		k, _ := store().GetRunning(name)
		if k == nil {
			if lastState != "stopped" {
				out.emit(map[string]any{"event": "kernel", "name": name, "state": "stopped"})
				lastState, lastPID = "stopped", 0
			}
			fromStart = true
			sleepCtx(ctx, eventsWaitDown)
			continue
		}
		if lastState != "running" || lastPID != k.PID {
			ev := map[string]any{"event": "kernel", "name": name, "state": "running", "pid": k.PID}
			if lastState == "running" {
				ev["restarted"] = true
				fromStart = true
			}
			out.emit(ev)
			lastState, lastPID = "running", k.PID
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		session, err := mcpclient.Connect(cctx, k.Port, mcpclient.ConnectOpts{ClientName: "rat events"})
		cancel()
		if err != nil {
			sleepCtx(ctx, eventsWaitDown)
			continue
		}
		pollKernel(ctx, session, out, fromStart)
		fromStart = false
		if ctx.Err() != nil {
			session.Close() // a clean exit ends the session; a vanished kernel has none to end
		}
		sleepCtx(ctx, 200*time.Millisecond)
	}
}

// pollKernel follows one kernel process until it goes away or ctx ends.
func pollKernel(ctx context.Context, session *mcpclient.Session, out eventSink, fromStart bool) {
	var since int64
	active := map[string]bool{}
	boot := ""
	withActive := true
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := session.Events(cctx, since, withActive)
		cancel()
		if err != nil {
			return // the process went away; the caller re-checks the kernel
		}
		var view mcpserver.EventsView
		if err := json.Unmarshal([]byte(raw), &view); err != nil || view.Boot == "" {
			// A kernel started by an older rat answers tail without the
			// event stream. Say so once, then wait for it to be restarted
			// (the caller sees the new process and follows it).
			out.emit(map[string]any{"event": "unsupported", "message": "this kernel was started by an older rat and cannot be followed; restart it (rat restart) to follow it"})
			for ctx.Err() == nil {
				sleepCtx(ctx, 2*time.Second)
				if _, err := session.Events(ctx, 0, false); err != nil {
					return // gone (restarted or stopped): the caller follows what comes next
				}
			}
			return
		}
		if boot != "" && view.Boot != boot {
			return // a new process behind the same port: start over
		}
		complete := len(view.Events) == 0 || view.Events[0]["seq"] == float64(1)
		if withActive && fromStart && complete {
			// A process we saw start, its whole history still buffered:
			// every event it has is news. (If the buffer already lost its
			// beginning, the snapshot below is the truthful start.)
			fromStart = false
			boot = view.Boot
			withActive = false
			since = 0
			continue
		}
		if withActive {
			// First look at this process (or after a gap): what runs now,
			// not the history before we came.
			boot = view.Boot
			active = map[string]bool{}
			for _, run := range view.Active {
				emitActiveRun(out, run)
				if id, _ := run["run_id"].(string); id != "" {
					active[id] = true
				}
			}
			since = view.Seq
			withActive = false
		} else {
			if view.Gap {
				out.emit(map[string]any{"event": "gap"})
				withActive = true
				since = 0
				continue
			}
			for _, ev := range view.Events {
				kind, _ := ev["kind"].(string)
				id, _ := ev["run_id"].(string)
				switch kind {
				case "run_started":
					active[id] = true
				case "run_ended":
					delete(active, id)
				}
				ev["event"] = kind
				delete(ev, "kind")
				out.emit(ev)
				if seq, ok := ev["seq"].(float64); ok && int64(seq) > since {
					since = int64(seq)
				}
			}
		}
		if len(active) > 0 {
			sleepCtx(ctx, eventsPollBusy)
		} else {
			sleepCtx(ctx, eventsPollIdle)
		}
	}
}

// emitActiveRun reports a run in progress as the events a reader would
// have seen had it been there from the start, marked "replay".
func emitActiveRun(out eventSink, run map[string]any) {
	started := map[string]any{}
	for k, v := range run {
		if k == "output" || k == "output_cut" || k == "waiting" || k == "kind" {
			continue
		}
		started[k] = v
	}
	started["event"] = "run_started"
	started["replay"] = true
	out.emit(started)
	id := run["run_id"]
	if text, _ := run["output"].(string); text != "" {
		ev := map[string]any{"event": "run_output", "run_id": id, "text": text, "replay": true}
		if cut, _ := run["output_cut"].(bool); cut {
			ev["output_cut"] = true
		}
		out.emit(ev)
	}
	if w, ok := run["waiting"].(map[string]any); ok {
		out.emit(map[string]any{"event": "run_waiting", "run_id": id, "prompt": w["prompt"], "secret": w["secret"], "replay": true})
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

type eventSink interface{ emit(map[string]any) }

type jsonEventSink struct{ enc *json.Encoder }

func (j *jsonEventSink) emit(ev map[string]any) { _ = j.enc.Encode(ev) }

// humanEventSink prints what a person following a kernel wants to read.
type humanEventSink struct {
	w       io.Writer
	midLine bool
	shown   map[string]string // run_id → output already printed
}

// printOutput prints text as indented lines, continuing a partial line.
func (h *humanEventSink) printOutput(text string) {
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if !h.midLine {
			fmt.Fprint(h.w, "    ")
		}
		fmt.Fprint(h.w, line)
		h.midLine = !strings.HasSuffix(line, "\n")
	}
}

func (h *humanEventSink) emit(ev map[string]any) {
	str := func(k string) string { v, _ := ev[k].(string); return v }
	clock := time.Now().Format("15:04:05")
	endLine := func() {
		if h.midLine {
			fmt.Fprintln(h.w)
			h.midLine = false
		}
	}
	switch ev["event"] {
	case "kernel":
		endLine()
		switch {
		case str("state") == "stopped":
			fmt.Fprintf(h.w, "%s %s %s\n", s.Dim(clock), str("name"), s.Dim("not running — waiting for it"))
		case ev["restarted"] == true:
			fmt.Fprintf(h.w, "%s %s %s\n", s.Dim(clock), str("name"), s.Yellow("restarted"))
		default:
			fmt.Fprintf(h.w, "%s %s %s\n", s.Dim(clock), str("name"), s.Green("running"))
		}
	case "run_started":
		endLine()
		code := strings.TrimSpace(str("code"))
		first := strings.SplitN(code, "\n", 2)[0]
		if len(first) > 70 {
			first = first[:67] + "…"
		}
		if strings.Contains(code, "\n") {
			first += s.Dim(fmt.Sprintf("  (+%d lines)", strings.Count(code, "\n")))
		}
		fmt.Fprintf(h.w, "%s %s %s %s\n", s.Dim(clock), s.Cyan(orDefault(str("caller"), "rat")), s.Cyan("▶"), first)
	case "run_output":
		if h.shown == nil {
			h.shown = map[string]string{}
		}
		h.shown[str("run_id")] += str("text")
		h.printOutput(str("text"))
	case "run_waiting":
		endLine()
		fmt.Fprintf(h.w, "%s %s\n", s.Dim(clock), s.Yellow("✋ waiting for input"))
	case "run_ended":
		// Live chunks come every 50 ms: a quick run has none, and the end
		// carries the whole output. Print what the chunks did not.
		full := str("output")
		if ok, _ := ev["ok"].(bool); !ok && str("error") != "" {
			full = str("error")
		}
		seen := h.shown[str("run_id")]
		delete(h.shown, str("run_id"))
		if rest := strings.TrimPrefix(full, strings.TrimRight(seen, "\n")); strings.TrimSpace(rest) != "" && strings.HasPrefix(full, strings.TrimRight(seen, "\n")) {
			h.printOutput(strings.TrimLeft(rest, "\n") + "\n")
		} else if seen == "" && strings.TrimSpace(full) != "" {
			h.printOutput(full + "\n")
		}
		endLine()
		ms, _ := ev["duration_ms"].(float64)
		mark := s.Green("✓")
		if ok, _ := ev["ok"].(bool); !ok {
			mark = s.Red("✗")
		}
		fmt.Fprintf(h.w, "%s %s %s\n", s.Dim(clock), mark, s.Dim(formatMillis(ms)))
	case "ctl_called":
		endLine()
		fmt.Fprintf(h.w, "%s %s %s\n", s.Dim(clock), orDefault(str("caller"), "rat"), s.Dim("ctl "+str("op")))
	case "gap":
		endLine()
		fmt.Fprintf(h.w, "%s %s\n", s.Dim(clock), s.Yellow("… some events were missed"))
	case "unsupported":
		endLine()
		fmt.Fprintf(h.w, "%s %s\n", s.Dim(clock), s.Yellow(str("message")))
	}
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func formatMillis(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.1fs", ms/1000)
}
