package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// EventBus fans out kernel events (run_started, run_output, run_ended,
// look_called, ctl_called) to every connected MCP session. It also
// keeps a bounded replay buffer so newly-connected clients can catch
// up on recent activity.
//
// Events are sent as MCP JSON-RPC notifications with method
// "rat/event". Every event carries:
//
//	{ "kind":      "run_started" | "run_output" | "run_ended" | ... ,
//	  "caller":    "rat-py-repl@pts/2" | "rat-vscode" | ...,
//	  "caller_id": "<mcp session id>",
//	  "seq":       <monotonic int>,
//	  "run_id":    "<uuid per run>",        // for run_* kinds
//	  ...payload }
//
// Recipients dedupe by (caller_id, run_id) so the caller's own
// broadcast doesn't get rendered twice.
type EventBus struct {
	s           *server.MCPServer
	mu          sync.Mutex
	nextSeq     int64
	recent      []map[string]any
	recentCap   int

	// boot names this server process: seq restarts at 1 in a new process,
	// so a reader that sees a new boot starts over instead of waiting for
	// numbers it already passed.
	boot string
	// active holds the runs in progress, so a reader that arrives in the
	// middle of a run learns that it runs, what it ran, what it printed so
	// far and whether it waits for input — however long ago it started.
	active map[string]*activeRun
}

// activeRun is one run in progress, as a late reader needs it.
type activeRun struct {
	started map[string]any // the run_started event
	output  []byte         // what it printed so far (the last activeOutputCap bytes)
	cut     bool           // true when earlier output was dropped
	waiting map[string]any // the run_waiting event while it waits for input
}

// activeOutputCap bounds the output kept per run in progress.
const activeOutputCap = 256 * 1024

func newBootID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewEventBus returns an EventBus that broadcasts via the given server.
func NewEventBus(s *server.MCPServer) *EventBus {
	return &EventBus{
		s:         s,
		recentCap: 200,
		boot:      newBootID(),
		active:    map[string]*activeRun{},
	}
}

// Publish broadcasts one event to every connected session and
// appends it to the replay buffer.
func (b *EventBus) Publish(payload map[string]any) {
	if payload == nil {
		return
	}
	b.mu.Lock()
	b.nextSeq++
	payload["seq"] = b.nextSeq
	if _, ok := payload["ts"]; !ok {
		payload["ts"] = time.Now().UnixMilli()
	}
	// Take a shallow copy for the replay buffer so later mutation
	// of `payload` by the caller wouldn't leak in.
	snapshot := make(map[string]any, len(payload))
	for k, v := range payload {
		snapshot[k] = v
	}
	b.recent = append(b.recent, snapshot)
	if len(b.recent) > b.recentCap {
		b.recent = b.recent[len(b.recent)-b.recentCap:]
	}
	b.trackLocked(snapshot)
	b.mu.Unlock()

	b.s.SendNotificationToAllClients("rat/event", payload)
}

// trackLocked keeps the runs-in-progress table current. Caller holds mu.
func (b *EventBus) trackLocked(ev map[string]any) {
	runID, _ := ev["run_id"].(string)
	if runID == "" {
		return
	}
	switch ev["kind"] {
	case "run_started":
		b.active[runID] = &activeRun{started: ev}
	case "run_output":
		if r := b.active[runID]; r != nil {
			text, _ := ev["text"].(string)
			r.output = append(r.output, text...)
			if len(r.output) > activeOutputCap {
				r.output = append([]byte(nil), r.output[len(r.output)-activeOutputCap:]...)
				r.cut = true
			}
		}
	case "run_waiting":
		if r := b.active[runID]; r != nil {
			r.waiting = ev
		}
	case "run_input_done":
		if r := b.active[runID]; r != nil {
			r.waiting = nil
		}
	case "run_ended":
		delete(b.active, runID)
	}
}

// EventsView is what a reader polls: the events after its last seq, the
// runs in progress, and enough to notice a restart or a gap.
type EventsView struct {
	Boot   string           `json:"boot"`
	Seq    int64            `json:"seq"`              // the newest seq in this process (0: none yet)
	Gap    bool             `json:"gap,omitempty"`    // events after `since` were dropped from the buffer
	Events []map[string]any `json:"events"`           // seq > since, oldest first
	Active []map[string]any `json:"active,omitempty"` // runs in progress (only when asked)
}

// View returns the events after sinceSeq and, when withActive, the runs
// in progress (each: its run_started event plus "output", "output_cut"
// and "waiting").
func (b *EventBus) View(sinceSeq int64, withActive bool) EventsView {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := EventsView{Boot: b.boot, Seq: b.nextSeq, Events: []map[string]any{}}
	for _, e := range b.recent {
		if seq, ok := e["seq"].(int64); ok && seq > sinceSeq {
			v.Events = append(v.Events, e)
		}
	}
	if sinceSeq > 0 && sinceSeq < b.nextSeq {
		if len(v.Events) == 0 {
			v.Gap = true
		} else if first, _ := v.Events[0]["seq"].(int64); first > sinceSeq+1 {
			v.Gap = true
		}
	}
	if withActive {
		for _, r := range b.active {
			run := make(map[string]any, len(r.started)+3)
			for k, val := range r.started {
				run[k] = val
			}
			run["output"] = string(r.output)
			if r.cut {
				run["output_cut"] = true
			}
			if r.waiting != nil {
				run["waiting"] = map[string]any{"prompt": r.waiting["prompt"], "secret": r.waiting["secret"]}
			}
			v.Active = append(v.Active, run)
		}
	}
	return v
}

// Replay returns every event whose seq is strictly greater than
// `sinceSeq`. sinceSeq == 0 returns the whole buffer.
func (b *EventBus) Replay(sinceSeq int64) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]map[string]any, 0, len(b.recent))
	for _, e := range b.recent {
		if seq, ok := e["seq"].(int64); ok && seq > sinceSeq {
			out = append(out, e)
		}
	}
	return out
}

// sendNotificationToSpecificSession is a narrow helper used when a
// tool handler wants to push a server-originated notification down
// the caller's own SSE response (e.g. legacy rat/output path).
func sendNotificationToSession(
	ch chan<- mcp.JSONRPCNotification,
	method string,
	fields map[string]any,
) {
	if fields == nil {
		fields = map[string]any{}
	}
	select {
	case ch <- mcp.JSONRPCNotification{
		JSONRPC: mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{
			Method: method,
			Params: mcp.NotificationParams{AdditionalFields: fields},
		},
	}:
	default:
	}
}
