package activity

import (
	"testing"
	"time"
)

func TestIdleTracking(t *testing.T) {
	tr := New()
	if d := tr.IdleFor(); d > time.Second {
		t.Fatalf("fresh tracker should not be idle, got %v", d)
	}
}

func TestTouchFrom(t *testing.T) {
	tr := New()
	tr.TouchFrom("claude-desktop")
	if got := tr.LastCaller(); got != "claude-desktop" {
		t.Fatalf("LastCaller = %q, want claude-desktop", got)
	}
}

func TestClientTracking(t *testing.T) {
	tr := New()
	tr.AddClient("s1", "claude-desktop")
	tr.AddClient("s2", "rat")
	tr.AddClient("s3", "rat")

	if tr.ClientCount() != 3 {
		t.Fatalf("ClientCount = %d, want 3", tr.ClientCount())
	}
	if got := tr.ClientNames(); got != "claude-desktop, rat (2)" {
		t.Fatalf("ClientNames = %q, want %q", got, "claude-desktop, rat (2)")
	}

	tr.RemoveClient("s2")
	if tr.ClientCount() != 2 {
		t.Fatalf("ClientCount after remove = %d, want 2", tr.ClientCount())
	}
	if got := tr.ClientNames(); got != "claude-desktop, rat" {
		t.Fatalf("ClientNames after remove = %q, want %q", got, "claude-desktop, rat")
	}

	tr.RemoveClient("s1")
	tr.RemoveClient("s3")
	if tr.ClientCount() != 0 {
		t.Fatalf("ClientCount after remove all = %d, want 0", tr.ClientCount())
	}
	if got := tr.ClientNames(); got != "" {
		t.Fatalf("ClientNames after remove all = %q, want empty", got)
	}
}

func TestNilTracker(t *testing.T) {
	var tr *Tracker
	// None of these should panic.
	tr.Touch()
	tr.TouchFrom("test")
	tr.AddClient("s1", "test")
	tr.RemoveClient("s1")
	_ = tr.IdleFor()
	_ = tr.LastCaller()
	_ = tr.ClientCount()
	_ = tr.ClientNames()
}
