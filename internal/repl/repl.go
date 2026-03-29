// Package repl implements the interactive shell frontend for rat.
//
// For bash, the frontend is the user's real shell inside tmux. `rat sh`
// simply attaches the current terminal to the shared tmux session.
package repl

import (
	"fmt"

	"github.com/maximerivest/rat/internal/bash"
)

// Config for the REPL session.
type Config struct {
	Name string // kernel name
	Lang string // canonical language
	Port int    // kernel MCP port
	Cwd  string // kernel working directory
}

// Run starts an interactive REPL session.
func Run(cfg Config) error {
	switch cfg.Lang {
	case "sh":
		return bash.Attach(cfg.Name)
	default:
		return fmt.Errorf("interactive REPL not yet implemented for %s", cfg.Lang)
	}
}
