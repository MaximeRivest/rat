// Package commands defines the rat CLI using Cobra.
package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/repl"
)

// Version is set at build time via ldflags.
var Version = "0.1.0"

const rootHelp = `rat — Run AnyThing

Daily use:
  rat py                        Enter your Python world
  rat run py '…'                One-liner
  rat look py [--at x]          See what's inside
  rat cancel py                 Unstick
  rat restart py                Fresh start
  rat status                    What's running

Setup & management:
  rat install py                Project setup (runtime + deps)
  rat doctor [py]               Diagnostics
  rat start <name>              Start a kernel
  rat stop <name> [--all]       Stop a kernel
  rat add <name> [dir]          Register a named runtime
  rat remove <name> [--all]     Delete a runtime's state
  rat reset <name>              Clear namespace (keep process)
  rat serve <name> [--http]     MCP server (for app builders)

Every command accepts a language (py, sh, r, jl, js) or a full
kernel name (py@myproject, py-ml). Languages auto-resolve to the
kernel for your current project.

See: rat <command> --help`

var rootCmd = &cobra.Command{
	Use:           "rat",
	Short:         "Run AnyThing",
	Long:          rootHelp,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return handleREPL(args[0], args[1:])
	},
}

func init() {
	rootCmd.AddGroup(
		&cobra.Group{ID: "daily", Title: "Daily use"},
		&cobra.Group{ID: "setup", Title: "Setup & management"},
	)
}

// Execute runs the root command.
func Execute() error {
	if len(os.Args) > 1 {
		first := os.Args[1]
		if first != "" && first[0] != '-' && !isKnownCommand(first) {
			return handleREPL(first, os.Args[2:])
		}
	}
	return rootCmd.Execute()
}

// isKnownCommand returns true if name matches a registered subcommand
// or built-ins like "help" / "completion".
func isKnownCommand(name string) bool {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == name || cmd.HasAlias(name) {
			return true
		}
	}
	switch name {
	case "help", "completion":
		return true
	}
	return false
}

func handleREPL(input string, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments after %q: %s", input, strings.Join(args, " "))
	}

	k, action, err := ensureKernel(input)
	if err != nil {
		return err
	}
	printKernelAction(k, action)

	return repl.Run(repl.Config{
		Name: k.Name,
		Lang: k.Lang,
		Port: k.Port,
		Cwd:  k.Cwd,
		Venv: k.Venv,
	})
}
