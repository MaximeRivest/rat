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
	"path/filepath"
	"runtime"
	"strings"

	"github.com/maximerivest/rat/internal/bash"
	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/python"
	"github.com/maximerivest/rat/internal/runtimes"
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
// On Ctrl-D (normal exit), shows a picker to switch between kernels.
// On Ctrl-Z (exit code 2), returns directly to the shell.
func Run(cfg Config) error {
	for {
		exitCode := RunOnce(cfg)

		// Exit code 2 = Ctrl-Z / explicit quit → straight to shell.
		// Any non-zero exit = error → stay in terminal so the user sees the message.
		if exitCode != 0 {
			return nil
		}

		// Show picker if there are multiple kernels in this project.
		baseName := cfg.Name
		// Strip instance suffix to get base name.
		if dot := strings.LastIndex(baseName, "."); dot > 0 {
			rest := baseName[dot+1:]
			allDigits := len(rest) > 0
			for _, c := range rest {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				baseName = baseName[:dot]
			}
		}

		items := DiscoverPickerItems(baseName, cfg.Cwd)

		// Always show picker on Ctrl-D so the user can switch kernels,
		// start new ones, or quit to shell from there.

		// Clear screen for a clean picker.
		fmt.Fprint(os.Stdout, "\033[2J\033[H")

		result := ShowPicker(items, cfg.Lang, cfg.Instance, cfg.Name)
		if result.Quit {
			return nil
		}

		// Reconnect with selected target.
		newCfg, err := resolvePickerResult(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[rat] switch error: %v\n", err)
			return nil
		}
		cfg = *newCfg
	}
}

// RunOnce runs the REPL frontend once and returns the exit code.
func RunOnce(cfg Config) int {
	var err error

	// Built-in kernels with hardcoded frontends.
	switch cfg.Lang {
	case "sh":
		if runtime.GOOS == "windows" {
			err = runSharedFrontend(cfg)
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return exitErr.ExitCode()
				}
				_ = runGenericRepl(cfg)
				return 0
			}
			return 0
		}
		err = bash.Attach(cfg.Name)
		return exitCodeFromError(err)
	case "py":
		err = python.RunFrontend(cfg.Name, cfg.Port, cfg.Cwd, cfg.Venv, "")
		return exitCodeFromError(err)
	}

	// Generic runtimes — dispatch on frontend type from runtime.yaml.
	if cfg.RuntimeConfig != nil {
		switch cfg.RuntimeConfig.FrontendType() {
		case "tmux":
			err = generic.TmuxAttach(cfg.Name)
			return exitCodeFromError(err)
		case "native":
			err = runNativeFrontend(cfg)
			return exitCodeFromError(err)
		}
	}

	// Default: try the shared prompt_toolkit frontend, fall back to bare Go REPL.
	err = runSharedFrontend(cfg)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		// Python or prompt_toolkit not available — use bare Go REPL.
		_ = runGenericRepl(cfg)
		return 0
	}
	return 0
}

// exitCodeFromError extracts the exit code from an exec.ExitError.
func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

// ResolvePickerFunc is set by the caller to resolve a picker selection
// into a full Config. This avoids circular imports with the commands package.
// name is the kernel name (may be empty for new kernels).
var ResolvePickerFunc func(lang string, instance int, name string) (*Config, error)

func resolvePickerResult(result pickerResult) (*Config, error) {
	if ResolvePickerFunc == nil {
		return nil, fmt.Errorf("picker resolve not configured")
	}
	return ResolvePickerFunc(result.Lang, result.Instance, result.Name)
}

// runSharedFrontend tries to launch the shared prompt_toolkit frontend.
// Returns an error if Python isn't available (caller should fall back).
func runSharedFrontend(cfg Config) error {
	python, err := findFrontendPython(cfg.Venv)
	if err != nil {
		return err
	}

	frontendPath, err := runtimes.ExtractFrontend()
	if err != nil {
		return err
	}

	mcpURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Port)
	args := []string{
		frontendPath,
		"--server", mcpURL,
		"--name", cfg.Name,
		"--lang", cfg.Lang,
	}
	if cfg.ActivityLog != "" {
		args = append(args, "--activity-log", cfg.ActivityLog)
	}

	proc := exec.Command(python, args...)
	proc.Dir = cfg.Cwd
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	return proc.Run()
}

func findFrontendPython(venv string) (string, error) {
	if venv != "" {
		path := filepath.Join(venv, "bin", "python")
		if runtime.GOOS == "windows" {
			path = filepath.Join(venv, "Scripts", "python.exe")
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	for _, candidate := range []string{"python3", "python"} {
		path, err := exec.LookPath(candidate)
		if err == nil && !python.IsWindowsStoreAlias(path) {
			return path, nil
		}
	}
	if path, err := exec.LookPath("py"); err == nil {
		return path, nil
	}
	if runtime.GOOS == "windows" {
		if path := python.FindWindowsPython(); path != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("python not found")
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
