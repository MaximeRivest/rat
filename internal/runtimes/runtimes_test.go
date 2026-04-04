package runtimes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("r") {
		t.Fatal("r should be built-in")
	}
	if !IsBuiltin("pi") {
		t.Fatal("pi should be built-in")
	}
	if IsBuiltin("fortran") {
		t.Fatal("fortran should not be built-in")
	}
}

func TestExtractR(t *testing.T) {
	// Override cache dir
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	path, err := Extract("r")
	if err != nil {
		t.Fatalf("Extract(r): %v", err)
	}
	if filepath.Base(path) != "runtime.yaml" {
		t.Fatalf("unexpected path: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("runtime.yaml not created: %v", err)
	}
	kernelPath := filepath.Join(filepath.Dir(path), "kernel.R")
	if _, err := os.Stat(kernelPath); err != nil {
		t.Fatalf("kernel.R not created: %v", err)
	}
}

func TestExtractPi(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)

	path, err := Extract("pi")
	if err != nil {
		t.Fatalf("Extract(pi): %v", err)
	}
	bridgePath := filepath.Join(filepath.Dir(path), "bridge.ts")
	if _, err := os.Stat(bridgePath); err != nil {
		t.Fatalf("bridge.ts not created: %v", err)
	}
}
