package mcpclient

import "testing"

func TestParseStatusStructured(t *testing.T) {
	status := parseStatus("idle\nidle_seconds: 172800\nmemory_mb: 96\npid: 1234")
	if status.State != "idle" {
		t.Fatalf("State = %q, want idle", status.State)
	}
	if status.IdleSeconds != 172800 {
		t.Fatalf("IdleSeconds = %d, want 172800", status.IdleSeconds)
	}
	if status.MemoryMB != 96 {
		t.Fatalf("MemoryMB = %d, want 96", status.MemoryMB)
	}
	if status.PID != 1234 {
		t.Fatalf("PID = %d, want 1234", status.PID)
	}
}

func TestParseStatusLegacy(t *testing.T) {
	status := parseStatus("waiting_for_input")
	if status.State != "waiting_for_input" {
		t.Fatalf("State = %q, want waiting_for_input", status.State)
	}
	if status.IdleSeconds != 0 || status.MemoryMB != 0 || status.PID != 0 {
		t.Fatalf("unexpected legacy status parse: %+v", status)
	}
}
