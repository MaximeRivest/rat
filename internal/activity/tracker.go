// Package activity tracks server-side activity: idle time, connected
// clients, and the identity of the last caller.
//
// The MCP server owns one Tracker per kernel. Session hooks record
// connect/disconnect, and tool handlers call TouchFrom to record who
// made the last call. The status command reads all of this to build
// a rich `rat status -v` display.
package activity

import (
	"sort"
	"sync"
	"time"
)

// ClientInfo describes one connected MCP client session.
type ClientInfo struct {
	SessionID   string
	Name        string // from MCP clientInfo.name ("claude-desktop", "cursor", "rat", …)
	ConnectedAt time.Time
}

// Tracker records server-side activity. Safe for concurrent use.
type Tracker struct {
	mu         sync.Mutex
	lastCall   time.Time
	lastCaller string
	clients    map[string]ClientInfo // sessionID → info
}

// New creates a tracker initialized to "now" with no connected clients.
func New() *Tracker {
	return &Tracker{
		lastCall: time.Now(),
		clients:  make(map[string]ClientInfo),
	}
}

// Touch records activity without a caller name.
func (t *Tracker) Touch() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.lastCall = time.Now()
	t.mu.Unlock()
}

// TouchFrom records activity and who made the call.
func (t *Tracker) TouchFrom(clientName string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.lastCall = time.Now()
	if clientName != "" {
		t.lastCaller = clientName
	}
	t.mu.Unlock()
}

// IdleFor returns the duration since the last recorded activity.
func (t *Tracker) IdleFor() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.lastCall)
}

// LastCaller returns the name of the most recent caller.
func (t *Tracker) LastCaller() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastCaller
}

// AddClient records a new MCP session.
func (t *Tracker) AddClient(sessionID, name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.clients[sessionID] = ClientInfo{
		SessionID:   sessionID,
		Name:        name,
		ConnectedAt: time.Now(),
	}
	t.mu.Unlock()
}

// RemoveClient removes a disconnected MCP session.
func (t *Tracker) RemoveClient(sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.clients, sessionID)
	t.mu.Unlock()
}

// ClientCount returns the number of connected sessions.
func (t *Tracker) ClientCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.clients)
}

// ClientNames returns the unique client names, sorted alphabetically.
// Multiple sessions from the same client (e.g. two "rat" terminals)
// appear once with a count: "rat (2)".
func (t *Tracker) ClientNames() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	counts := make(map[string]int, len(t.clients))
	for _, c := range t.clients {
		name := c.Name
		if name == "" {
			name = "unknown"
		}
		counts[name]++
	}
	t.mu.Unlock()

	if len(counts) == 0 {
		return ""
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		if counts[name] > 1 {
			parts = append(parts, name+" ("+itoa(counts[name])+")")
		} else {
			parts = append(parts, name)
		}
	}
	return join(parts, ", ")
}

// Simple helpers to avoid importing fmt/strconv for trivial formatting.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 4)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

func join(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}
