//go:build !windows

package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDirUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secure")
	if err := EnsurePrivateDir(path); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %#o, want 0o700", got)
	}
}

func TestOpenPrivateAppendUnix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secure", "kernel.log")
	f, err := OpenPrivateAppend(path)
	if err != nil {
		t.Fatalf("OpenPrivateAppend: %v", err)
	}
	if _, err := f.WriteString("hello\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %#o, want 0o600", got)
	}
}
