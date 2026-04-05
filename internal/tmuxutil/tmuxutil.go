// Package tmuxutil provides shared tmux session configuration for all rat kernels.
//
// Every rat tmux session (bash, pi, generic runtimes) should call ConfigureSession
// after creating the session. This ensures consistent look-and-feel plus safe
// keybindings that prevent users from accidentally killing the managed process.
package tmuxutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SessionConfig describes how to style a rat tmux session.
type SessionConfig struct {
	TmuxPath    string // path to tmux binary
	SessionName string // tmux session name
	Display     string // runtime display name (e.g. "py", "pi", "sh")
	Name        string // kernel name (e.g. "py@myapp")
	CancelKey   string // e.g. "Escape", "Ctrl+C" (optional)
	Extra       string // extra status-right content before Ctrl+D hint (optional)
}

// ConfigureSession applies the standard ratmux look and keybindings to a tmux session.
//
// It sets the status bar, binds Ctrl+D and Ctrl+Z to detach-client (so users
// don't accidentally kill or suspend the managed process), and keeps a consistent
// appearance across all rat runtimes.
//
// Power users can set RAT_TMUX_RAW=1 to skip the protective keybindings.
func ConfigureSession(cfg SessionConfig) {
	run := func(args ...string) {
		cmd := exec.Command(cfg.TmuxPath, args...)
		_ = cmd.Run()
	}

	// Build unified status bar.
	left := fmt.Sprintf("#[fg=colour45,bold] rat #[default] %s #[fg=colour245]│#[default] %s ", cfg.Display, cfg.Name)

	var rightParts []string
	rightParts = append(rightParts, "#[fg=colour10]shared#[default]")
	if cfg.Extra != "" {
		rightParts = append(rightParts, cfg.Extra)
	}
	if cfg.CancelKey != "" {
		rightParts = append(rightParts, cfg.CancelKey+" cancel")
	}
	rightParts = append(rightParts, "Ctrl+D exit")
	right := strings.Join(rightParts, " #[fg=colour245]•#[default] ")

	s := cfg.SessionName
	run("set-option", "-t", s, "status", "on")
	run("set-option", "-t", s, "status-position", "bottom")
	run("set-option", "-t", s, "status-interval", "1")
	run("set-option", "-t", s, "status-style", "bg=colour235,fg=colour252")
	run("set-option", "-t", s, "message-style", "bg=colour45,fg=colour16")
	run("set-option", "-t", s, "status-left-length", "80")
	run("set-option", "-t", s, "status-right-length", "100")
	run("set-option", "-t", s, "status-left", left)
	run("set-option", "-t", s, "status-right", right)
	run("set-option", "-t", s, "window-status-format", "")
	run("set-option", "-t", s, "window-status-current-format", "")
	run("set-option", "-t", s, "window-status-separator", "")

	// Bind Ctrl+D and Ctrl+Z to detach instead of sending EOF/SIGTSTP.
	// This makes rat sessions feel like normal REPLs where Ctrl+D exits.
	// The managed process stays alive — rat owns its lifecycle.
	// Power users: set RAT_TMUX_RAW=1 to skip these bindings.
	if os.Getenv("RAT_TMUX_RAW") == "" {
		run("bind-key", "-n", "-T", "root", "C-d", "detach-client")
		run("bind-key", "-n", "-T", "root", "C-z", "detach-client")
	}
}

// Run executes a tmux command and returns any error.
func Run(tmuxPath string, args ...string) error {
	cmd := exec.Command(tmuxPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
