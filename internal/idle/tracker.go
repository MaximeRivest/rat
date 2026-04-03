// Package idle tracks the time since the last meaningful kernel activity.
package idle

import (
	"sync"
	"time"
)

// Tracker records the timestamp of the most recent user-visible activity.
// It is safe for concurrent use by MCP tool handlers.
type Tracker struct {
	mu       sync.Mutex
	lastCall time.Time
}

// New creates a tracker initialized to "now".
func New() *Tracker {
	return &Tracker{lastCall: time.Now()}
}

// Touch records a new activity timestamp.
func (t *Tracker) Touch() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.lastCall = time.Now()
	t.mu.Unlock()
}

// IdleFor returns how long it has been since the last recorded activity.
func (t *Tracker) IdleFor() time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Since(t.lastCall)
}
