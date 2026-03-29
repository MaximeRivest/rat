// Package commands defines the rat CLI using Cobra.
//
// Command hierarchy (from README):
//
//	rat version                         version info
//	rat serve <name> [--http] [--port]  MCP server (stdio or HTTP)
//	rat run <name> '<code>'             run code on a kernel
//	rat look <name> [--at <sym>]        inspect variables
//	rat ctl <name> --op <op>            control: reset, cancel, restart
//	rat install <lang>                  install a runtime
//	rat setup                           interactive wizard
//	rat add <name> [--venv] [--cwd]     register a named runtime
//	rat rm <name>                       unregister a runtime
//	rat ls                              list runtimes
//	rat start <name>                    start a kernel
//	rat stop <name> [--all]             stop a kernel
//	rat restart <name>                  restart a kernel
//	rat doctor                          diagnostics
//	rat update                          update rat
//	rat <name>                          REPL (handled as unknown command)
package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/repl"
)

// Version is set at build time via ldflags.
var Version = "0.1.0"

// ANSI escape helpers
const (
	bold  = "\033[1m"
	reset = "\033[0m"
	cyan  = "\033[36m"
	green = "\033[32m"
)

var rootCmd = &cobra.Command{
	Use:   "rat",
	Short: bold + "R" + reset + "un " + bold + "A" + reset + "ny" + bold + "T" + reset + "hing — one binary, every REPL language",
	Long: bold + "r" + reset + "un " + bold + "a" + reset + "ny" + bold + "t" + reset + "hing" + ` — one binary, every REPL language

` + green + `  rat serve sh --http` + reset + `     Start an MCP HTTP server for bash
` + green + `  rat run py 'x = 42'` + reset + `    Run code on the Python kernel
` + green + `  rat py` + reset + `                  Drop into IPython connected to the shared kernel

The protocol is MCP. Any MCP client can connect.`,

	// When someone types "rat py" or "rat r", cobra won't find a
	// matching subcommand. We handle that here to launch the REPL.
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return fmt.Errorf("unknown command %q — did you mean 'rat serve %s' or 'rat run %s'?", args[0], args[0], args[0])
	},

	// Don't show "unknown command" errors from cobra — we handle them.
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	// Future: "rat <name>" as a REPL shorthand will be handled
	// via rootCmd's RunE or a custom unknown-command hook.
	// For now, all known subcommands are registered below.
}

// Execute runs the root command.
func Execute() error {
	// Handle "rat <name>" as REPL shorthand: if the first arg is a
	// language alias (py, python, sh, bash, etc.) and not a registered
	// subcommand, treat it as a REPL launch.
	// This must happen before cobra parses, because cobra would reject
	// unknown subcommands.
	if len(os.Args) > 1 {
		first := os.Args[1]
		if !isKnownCommand(first) && first[0] != '-' && isLangAlias(first) {
			return handleREPL(first, os.Args[2:])
		}
	}

	return rootCmd.Execute()
}

// isKnownCommand returns true if name matches a registered subcommand
// or built-in like "help" / "completion".
func isKnownCommand(name string) bool {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == name || cmd.HasAlias(name) {
			return true
		}
	}
	// Cobra built-ins
	switch name {
	case "help", "completion":
		return true
	}
	return false
}

// handleREPL launches the REPL for a given runtime name.
// Ensures the kernel is running (auto-starts if needed), then
// drops into an interactive session.
func handleREPL(name string, args []string) error {
	lang, err := resolveLang(name)
	if err != nil {
		return err
	}

	ctx := context.Background()
	session, err := connectToKernel(ctx, name)
	if err != nil {
		return err
	}
	session.Close() // we just needed to ensure it's running + get the port

	// Get the kernel info from state
	k, err := store().Get(name)
	if err != nil || k == nil {
		return fmt.Errorf("kernel %s not found in state after connect", name)
	}

	return repl.Run(repl.Config{
		Name: k.Name,
		Lang: lang,
		Port: k.Port,
		Cwd:  k.Cwd,
	})
}
