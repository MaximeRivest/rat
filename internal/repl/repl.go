// Package repl implements the interactive shell frontend for rat.
//
// For bash, the frontend is the user's real shell inside tmux (Pattern A).
// For Python, it's IPython with eval/completions hooked to MCP (Pattern B).
// For generic runtimes, it's an MCP-connected thin wrapper REPL (Pattern C).
//
// All patterns connect to the kernel — the namespace is shared between
// the REPL and any MCP clients (Claude, scripts, other terminals).
package repl

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/maximerivest/rat/internal/bash"
	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/python"
)

// Config for the REPL session.
type Config struct {
	Name        string // kernel name
	Lang        string // canonical language
	Port        int    // kernel MCP port
	Cwd         string // kernel working directory
	Venv        string // venv path (py only, may be empty)
	ActivityLog string // path to activity.jsonl for shared session visibility

	// Generic runtime config (nil for built-in sh/py).
	RuntimeConfig *generic.RuntimeConfig
	ConfigDir     string // directory containing runtime.yaml

	// Instance support: "rat py 2" creates a second kernel for the same project.
	Instance int   // 1-based instance number (0 or 1 = default)
	Siblings []int // all running instance numbers (including this one)
}

// Run starts an interactive REPL session connected to the kernel.
func Run(cfg Config) error {
	// Built-in kernels with hardcoded frontends.
	switch cfg.Lang {
	case "sh":
		return bash.Attach(cfg.Name)
	case "py":
		return python.RunFrontend(cfg.Name, cfg.Port, cfg.Cwd, cfg.Venv, "")
	}

	// Generic runtimes — dispatch on frontend type from runtime.yaml.
	if cfg.RuntimeConfig != nil {
		switch cfg.RuntimeConfig.FrontendType() {
		case "tmux":
			return generic.TmuxAttach(cfg.Name)
		case "native":
			return runNativeFrontend(cfg)
		}
	}

	// Default: generic MCP-connected REPL.
	return runGenericRepl(cfg)
}

// runNativeFrontend launches a runtime's native frontend command
// with template variables expanded.
func runNativeFrontend(cfg Config) error {
	rcfg := cfg.RuntimeConfig
	cmdTemplate := rcfg.Frontend.Command
	if cmdTemplate == "" {
		// No native frontend — fall back.
		if rcfg.Frontend.Fallback != nil && rcfg.Frontend.Fallback.Type == "repl" {
			return runGenericRepl(cfg)
		}
		return fmt.Errorf("no frontend configured for %s", cfg.Lang)
	}

	mcp := fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Port)

	// Expand template variables.
	cmd := cmdTemplate
	cmd = strings.ReplaceAll(cmd, "{mcp_url}", mcp)
	cmd = strings.ReplaceAll(cmd, "{name}", cfg.Name)
	cmd = strings.ReplaceAll(cmd, "{activity_log}", cfg.ActivityLog)
	cmd = strings.ReplaceAll(cmd, "{config_dir}", cfg.ConfigDir)
	cmd = strings.ReplaceAll(cmd, "{cwd}", cfg.Cwd)

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return fmt.Errorf("empty frontend command for %s", cfg.Lang)
	}

	// Check if the command exists.
	if _, err := exec.LookPath(parts[0]); err != nil {
		// Try fallback.
		if rcfg.Frontend.Fallback != nil {
			switch rcfg.Frontend.Fallback.Type {
			case "repl":
				return runGenericRepl(cfg)
			case "tmux":
				return generic.TmuxAttach(cfg.Name)
			}
		}
		return fmt.Errorf("frontend command %q not found: %w", parts[0], err)
	}

	proc := exec.Command(parts[0], parts[1:]...)
	proc.Dir = cfg.Cwd
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	return proc.Run()
}
