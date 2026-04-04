package pi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPiLiveOutputReadsStreamFile(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "run.stream")
	if err := os.WriteFile(streamPath, []byte("hello\nworld"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := &Pi{}
	p.setLiveStream(streamPath)
	defer p.clearLiveStream()

	if got := p.liveOutput(); got != "hello\nworld" {
		t.Fatalf("liveOutput() = %q, want %q", got, "hello\nworld")
	}
}

func TestPiCtlOutputReturnsLiveStream(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "run.stream")
	if err := os.WriteFile(streamPath, []byte("partial output"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	p := &Pi{}
	p.setLiveStream(streamPath)
	defer p.clearLiveStream()

	if got := p.Ctl("output").Text; got != "partial output" {
		t.Fatalf("Ctl(output) = %q, want %q", got, "partial output")
	}
}
