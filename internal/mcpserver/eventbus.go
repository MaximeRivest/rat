package mcpserver

import (
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
}

// NewEventBus returns an EventBus that broadcasts via the given server.
func NewEventBus(s *server.MCPServer) *EventBus {
	return &EventBus{
		s:         s,
		recentCap: 200,
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
	b.mu.Unlock()

	b.s.SendNotificationToAllClients("rat/event", payload)
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
