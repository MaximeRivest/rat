// Package commands defines the rat CLI using Cobra.
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/repl"
	s "github.com/maximerivest/rat/internal/termstyle"
)

// Version is set at build time via ldflags.
var Version = "0.1.0"

func rootHelp() string {
	lines := []string{
		"🐀 " + s.Bold("rat") + " — " + s.Bold("R") + s.Dim("un") + s.Bold("A") + s.Dim("ny") + s.Bold("T") + s.Dim("hing"),
		"",
		s.Dim("Use, manage and install shareable repls."),
		"",
		s.Cyan("rat <lang>") + s.Dim(" drops you into a REPL. ") + s.Cyan("rat run <lang> 'code'") + s.Dim(" executes code"),
		s.Dim("and returns the result. Same kernel, same namespace — whether you're"),
		s.Dim("typing, or Claude/Cursor/a notebook is calling."),
		"",
		s.Bold("Daily use:") + "  " + s.Dim("<lang> = py, sh, r, jl, js or a kernel name"),
		helpLine("rat <lang>", "Drop into a REPL"),
		helpLine("rat run <lang> '…'", "One-liner"),
		helpLine("rat look <lang>", "See what's inside"),
		helpLine("rat tail <lang>", "See recent activity"),
		helpLine("rat cancel <lang>", "Unstick"),
		helpLine("rat restart <lang>", "Fresh start"),
		helpLine("rat status", "What's running"),
		"",
		s.Bold("Setup & management:"),
		helpLine("rat install <lang>", "Project setup (runtime + deps)"),
		helpLine("rat doctor [<lang>]", "Diagnostics"),
		helpLine("rat start <name>", "Start a kernel"),
		helpLine("rat stop <name> [--all]", "Stop a kernel"),
		helpLine("rat add <name> [dir]", "Register a named runtime"),
		helpLine("rat reset <name>", "Clear namespace (keep process)"),
		helpLine("rat serve <name> [--http]", "MCP server (for app builders)"),
		"",
		s.Bold("How naming works:"),
		s.Dim("  When you type a language, rat creates a kernel named <lang>@<project>"),
		s.Dim("  based on your current directory. Same directory = same kernel."),
		"",
		"  " + s.Cyan("rat py") + s.Dim("  from ~/Projects/myapp  →  ") + s.Bold("py@myapp"),
		"  " + s.Cyan("rat py") + s.Dim("  from ~/Projects/other  →  ") + s.Bold("py@other") + s.Dim("  (separate kernel)"),
		"  " + s.Cyan("rat py") + s.Dim("  from ~/Projects/myapp  →  ") + s.Bold("py@myapp") + s.Dim("  (same one as before)"),
		"",
		s.Dim("  Full names bypass resolution and are used as-is:"),
		"  " + s.Cyan("rat py-ml") + s.Dim("                       →  ") + s.Bold("py-ml"),
		"  " + s.Cyan("rat py@myapp") + s.Dim("                    →  ") + s.Bold("py@myapp"),
		"",
		s.Dim("  Want a custom kernel with its own venv, cwd, or runtime?"),
		s.Dim("  See: ") + s.Bold("rat add --help"),
		"",
		s.Dim("See: ") + s.Bold("rat <command> --help") + s.Dim("  ·  ") + s.Bold("rat version") + s.Dim(" for binary + runtime versions"),
	}
	return strings.Join(lines, "\n")
}

func helpLine(cmd, desc string) string {
	return fmt.Sprintf("  %-30s  %s", s.Cyan(cmd), s.Dim(desc))
}

var rootCmd = &cobra.Command{
	Use:           "rat",
	Short:         "Run AnyThing",
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, rootHelp())
			return nil
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

	// Activity log: check the cache dir for this kernel's log.
	activityLog := activityLogPath(k.Name)

	// Load runtime config for generic runtimes (nil for built-in sh/py).
	var rtCfg *generic.RuntimeConfig
	var configDir string
	if k.Lang != "sh" && k.Lang != "py" {
		if cfgPath, err := findRuntimeConfig(k.Lang); err == nil {
			if cfg, err := generic.LoadConfig(cfgPath); err == nil {
				rtCfg = cfg
				configDir = filepath.Dir(cfgPath)
			}
		}
	}

	return repl.Run(repl.Config{
		Name:          k.Name,
		Lang:          k.Lang,
		Port:          k.Port,
		Cwd:           k.Cwd,
		Venv:          k.Venv,
		ActivityLog:   activityLog,
		RuntimeConfig: rtCfg,
		ConfigDir:     configDir,
	})
}

// activityLogPath returns the expected activity log path for a kernel.
func activityLogPath(name string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(dir, "rat", "kernels", name, "activity.jsonl")
}
