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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/mcpclient"
	"github.com/maximerivest/rat/internal/repl"
	"github.com/maximerivest/rat/internal/state"
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
		if !isKnownCommand(first) && first[0] != '-' {
			// Check if it's a language alias, saved runtime, or running kernel.
			if isLangAlias(first) || isSavedRuntime(first) || isRunningKernel(first) {
				return handleREPL(first, os.Args[2:])
			}
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

// isSavedRuntime checks if name is a registered named runtime.
func isSavedRuntime(name string) bool {
	rt, _ := store().GetRuntime(name)
	return rt != nil
}

// isRunningKernel checks if name is a currently running kernel.
func isRunningKernel(name string) bool {
	k, _ := store().Get(name)
	return k != nil
}

func handleREPL(name string, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments after %q: %s\nDid you mean 'rat %s' or 'rat %s %s'?", name, strings.Join(args, " "), name, args[0], name)
	}

	s := store()

	// Named runtime (from `rat add`): connect directly, no project magic.
	if rt, _ := s.GetRuntime(name); rt != nil {
		return handleNamedREPL(s, name, rt.Lang)
	}

	// Language alias (py, sh, etc.): project-aware resolution.
	if isLangAlias(name) {
		lang, _ := resolveLang(name)
		return handleProjectREPL(s, lang)
	}

	// Running kernel by exact name (e.g., "py@autoprogramming").
	if k, _ := s.Get(name); k != nil {
		return launchREPL(s, k, k.Lang)
	}

	return fmt.Errorf("unknown runtime %q — use a language name (py, sh, r, ju, js) or 'rat add %s' to register it", name, name)
}

// handleNamedREPL connects to a named runtime (from `rat add`).
func handleNamedREPL(s *state.Store, name, lang string) error {
	ctx := context.Background()
	session, err := connectToKernel(ctx, name)
	if err != nil {
		return err
	}
	_, _ = session.Ctl(ctx, "status")
	session.Close()

	k, err := s.Get(name)
	if err != nil || k == nil {
		return fmt.Errorf("kernel %s not found in state after connect", name)
	}

	return repl.Run(repl.Config{
		Name: k.Name,
		Lang: lang,
		Port: k.Port,
		Cwd:  k.Cwd,
		Venv: k.Venv,
	})
}

// handleProjectREPL implements project-aware kernel resolution.
// When `rat py` is run from a project directory, it finds or creates
// a kernel specific to that project (detecting venv automatically).
func handleProjectREPL(s *state.Store, lang string) error {
	cwd, _ := os.Getwd()
	cwd, _ = filepath.Abs(cwd)

	root, _ := findProjectRoot(cwd)
	kernelName := resolveProjectKernelName(s, lang, cwd)

	// If an existing kernel matches, connect directly.
	if k, _ := s.Get(kernelName); k != nil {
		return launchREPL(s, k, lang)
	}

	// Inform user if bare name is taken by another project.
	if kernelName != lang {
		if existing, _ := s.Get(lang); existing != nil {
			fmt.Fprintf(os.Stderr, "%s running at %s — starting %s for this project.\n",
				lang, shortPath(existing.Cwd), kernelName)
		}
	}

	// Auto-detect venv for Python.
	venv := ""
	if lang == "py" {
		venv = findVenv(cwd)
	}

	k, err := daemon.Start(s, daemon.StartOpts{
		Name: kernelName,
		Lang: lang,
		Cwd:  root,
		Venv: venv,
	})
	if err != nil {
		return err
	}
	venvMsg := ""
	if k.Venv != "" {
		venvMsg = fmt.Sprintf(" venv=%s", shortPath(k.Venv))
	}
	fmt.Fprintf(os.Stderr, "%s started on http://127.0.0.1:%d/mcp (PID %d)%s\n",
		k.Name, k.Port, k.PID, venvMsg)

	return launchREPL(s, k, lang)
}

// launchREPL opens the interactive REPL for a running kernel.
func launchREPL(s *state.Store, k *state.Kernel, lang string) error {
	ctx := context.Background()
	session, err := mcpclient.Connect(ctx, k.Port)
	if err != nil {
		return err
	}
	_, _ = session.Ctl(ctx, "status")
	session.Close()

	return repl.Run(repl.Config{
		Name: k.Name,
		Lang: lang,
		Port: k.Port,
		Cwd:  k.Cwd,
		Venv: k.Venv,
	})
}
