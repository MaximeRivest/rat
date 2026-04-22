package userconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveMissingFileReturnsDefaults(t *testing.T) {
	cfg := ResolveFromPath(filepath.Join(t.TempDir(), "nope.yaml"), "py")
	if cfg.Activity.MaxCodeLines != DefaultActivityMaxCodeLines {
		t.Errorf("MaxCodeLines = %d, want %d", cfg.Activity.MaxCodeLines, DefaultActivityMaxCodeLines)
	}
	if cfg.Activity.MaxOutputLines != DefaultActivityMaxOutputLines {
		t.Errorf("MaxOutputLines = %d, want %d", cfg.Activity.MaxOutputLines, DefaultActivityMaxOutputLines)
	}
	if !cfg.History.SeedFromRuntime {
		t.Error("SeedFromRuntime should default to true")
	}
	if cfg.History.SeedLimit != DefaultHistorySeedLimit {
		t.Errorf("SeedLimit = %d, want %d", cfg.History.SeedLimit, DefaultHistorySeedLimit)
	}
}

func TestResolveGlobalOnly(t *testing.T) {
	path := writeConfig(t, `
repl:
  activity:
    max_code_lines: 10
    max_output_lines: 20
  history:
    seed_from_runtime: false
    seed_limit: 50
`)
	cfg := ResolveFromPath(path, "py")
	if cfg.Activity.MaxCodeLines != 10 {
		t.Errorf("MaxCodeLines = %d, want 10", cfg.Activity.MaxCodeLines)
	}
	if cfg.Activity.MaxOutputLines != 20 {
		t.Errorf("MaxOutputLines = %d, want 20", cfg.Activity.MaxOutputLines)
	}
	if cfg.History.SeedFromRuntime {
		t.Error("SeedFromRuntime should be false")
	}
	if cfg.History.SeedLimit != 50 {
		t.Errorf("SeedLimit = %d, want 50", cfg.History.SeedLimit)
	}
}

func TestResolvePerLanguageOverridesGlobal(t *testing.T) {
	path := writeConfig(t, `
repl:
  activity:
    max_code_lines: 5
    max_output_lines: 5
py:
  activity:
    max_output_lines: 99
`)
	// Python overrides output only.
	cfg := ResolveFromPath(path, "py")
	if cfg.Activity.MaxCodeLines != 5 {
		t.Errorf("MaxCodeLines = %d, want 5 (from global)", cfg.Activity.MaxCodeLines)
	}
	if cfg.Activity.MaxOutputLines != 99 {
		t.Errorf("MaxOutputLines = %d, want 99 (from py override)", cfg.Activity.MaxOutputLines)
	}
	// R inherits only the global settings.
	r := ResolveFromPath(path, "r")
	if r.Activity.MaxOutputLines != 5 {
		t.Errorf("R MaxOutputLines = %d, want 5 (from global)", r.Activity.MaxOutputLines)
	}
}

func TestResolveLanguageAliasPython(t *testing.T) {
	path := writeConfig(t, `
python:
  activity:
    max_code_lines: 42
`)
	cfg := ResolveFromPath(path, "py")
	if cfg.Activity.MaxCodeLines != 42 {
		t.Errorf("MaxCodeLines = %d, want 42 (via python alias)", cfg.Activity.MaxCodeLines)
	}
}

func TestResolveCanonicalBeatsAlias(t *testing.T) {
	path := writeConfig(t, `
python:
  activity:
    max_code_lines: 1
py:
  activity:
    max_code_lines: 2
`)
	cfg := ResolveFromPath(path, "py")
	if cfg.Activity.MaxCodeLines != 2 {
		t.Errorf("MaxCodeLines = %d, want 2 (canonical `py` overrides `python`)", cfg.Activity.MaxCodeLines)
	}
}

func TestResolveZeroMeansUnlimited(t *testing.T) {
	path := writeConfig(t, `
repl:
  activity:
    max_code_lines: 0
  history:
    seed_limit: 0
`)
	cfg := ResolveFromPath(path, "py")
	if cfg.Activity.MaxCodeLines != 0 {
		t.Errorf("MaxCodeLines = %d, want 0", cfg.Activity.MaxCodeLines)
	}
	if cfg.History.SeedLimit != 0 {
		t.Errorf("SeedLimit = %d, want 0", cfg.History.SeedLimit)
	}
}

func TestResolveMalformedYamlFallsBack(t *testing.T) {
	path := writeConfig(t, "repl: [not a map")
	cfg := ResolveFromPath(path, "py")
	if cfg.Activity.MaxOutputLines != DefaultActivityMaxOutputLines {
		t.Errorf("MaxOutputLines = %d, want default %d", cfg.Activity.MaxOutputLines, DefaultActivityMaxOutputLines)
	}
}
