package mcpclient

import "testing"

func TestParseStatusFull(t *testing.T) {
	text := `idle
idle_seconds: 172800
memory_mb: 96
pid: 1234
clients: 2
client_names: claude-desktop, rat
last_caller: claude-desktop
runtime_version: Python 3.12.1`

	s := parseStatus(text)
	if s.State != "idle" {
		t.Fatalf("State = %q, want idle", s.State)
	}
	if s.IdleSeconds != 172800 {
		t.Fatalf("IdleSeconds = %d, want 172800", s.IdleSeconds)
	}
	if s.MemoryMB != 96 {
		t.Fatalf("MemoryMB = %d, want 96", s.MemoryMB)
	}
	if s.PID != 1234 {
		t.Fatalf("PID = %d, want 1234", s.PID)
	}
	if s.Clients != 2 {
		t.Fatalf("Clients = %d, want 2", s.Clients)
	}
	if s.ClientNames != "claude-desktop, rat" {
		t.Fatalf("ClientNames = %q, want %q", s.ClientNames, "claude-desktop, rat")
	}
	if s.LastCaller != "claude-desktop" {
		t.Fatalf("LastCaller = %q, want %q", s.LastCaller, "claude-desktop")
	}
	if s.RuntimeVersion != "Python 3.12.1" {
		t.Fatalf("RuntimeVersion = %q, want %q", s.RuntimeVersion, "Python 3.12.1")
	}
}

func TestParseStatusLegacy(t *testing.T) {
	s := parseStatus("waiting_for_input")
	if s.State != "waiting_for_input" {
		t.Fatalf("State = %q, want waiting_for_input", s.State)
	}
	if s.IdleSeconds != 0 || s.MemoryMB != 0 || s.Clients != 0 {
		t.Fatalf("unexpected values in legacy parse: %+v", s)
	}
}

func TestParseStatusEmpty(t *testing.T) {
	s := parseStatus("")
	if s.State != "" {
		t.Fatalf("State = %q, want empty", s.State)
	}
}

func TestParseStatusWithRuntimeVersion(t *testing.T) {
	text := "busy\nruntime_version: bash 5.2.15\nidle_seconds: 0\nmemory_mb: 12\npid: 5678"
	s := parseStatus(text)
	if s.State != "busy" {
		t.Fatalf("State = %q, want busy", s.State)
	}
	if s.RuntimeVersion != "bash 5.2.15" {
		t.Fatalf("RuntimeVersion = %q, want %q", s.RuntimeVersion, "bash 5.2.15")
	}
}
