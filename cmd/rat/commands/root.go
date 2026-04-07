// Package commands defines the rat CLI using Cobra.
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/daemon"
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
		helpLine("rat <lang> [N]", "Drop into a REPL (N = instance number)"),
		helpLine("rat run <lang> '…'", "One-liner"),
		helpLine("rat look <lang>", "See what's inside"),
		helpLine("rat tail <lang>", "See recent activity"),
		helpLine("rat pick", "Switch between kernels"),
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

	// Wire up picker hooks so the repl package can discover kernels
	// and resolve picker selections without importing commands.
	repl.SetRunningKernelsFunc(func() ([]repl.KernelInfo, error) {
		kernels, err := store().ListRunning()
		if err != nil {
			return nil, err
		}
		var out []repl.KernelInfo
		for _, k := range kernels {
			out = append(out, repl.KernelInfo{Name: k.Name, Lang: k.Lang, Cwd: k.Cwd})
		}
		return out, nil
	})

	repl.SetAllKernelsFunc(func() ([]repl.KernelInfo, error) {
		kernels, err := store().ListKnown()
		if err != nil {
			return nil, err
		}
		var out []repl.KernelInfo
		for _, k := range kernels {
			out = append(out, repl.KernelInfo{
				Name:    k.Name,
				Lang:    k.Lang,
				Cwd:     k.Cwd,
				Started: k.Started.Unix(),
			})
		}
		return out, nil
	})

	repl.SetAllRuntimesFunc(func() ([]repl.RuntimeInfo, error) {
		runtimes, err := store().ListRuntimes()
		if err != nil {
			return nil, err
		}
		var out []repl.RuntimeInfo
		for _, rt := range runtimes {
			out = append(out, repl.RuntimeInfo{
				Name: rt.Name,
				Lang: rt.Lang,
				Cwd:  rt.Cwd,
			})
		}
		return out, nil
	})

	repl.SetStopKernelFunc(func(name string) {
		_ = daemon.Stop(store(), name)
	})

	repl.ResolvePickerFunc = func(lang string, instance int, name string) (*repl.Config, error) {
		// Use the kernel name directly if available (handles cross-project).
		input := name
		if input == "" {
			input = lang
			if instance >= 2 {
				input = fmt.Sprintf("%s.%d", lang, instance)
			}
		}
		k, _, err := ensureKernel(input)
		if err != nil {
			return nil, err
		}
		activityLog := activityLogPath(k.Name)
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
		return &repl.Config{
			Name:          k.Name,
			Lang:          k.Lang,
			Port:          k.Port,
			Cwd:           k.Cwd,
			Venv:          k.Venv,
			ActivityLog:   activityLog,
			RuntimeConfig: rtCfg,
			ConfigDir:     configDir,
			Instance:      instance,
		}, nil
	}
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
	instance := 0
	if len(args) == 1 {
		if n, err := strconv.Atoi(args[0]); err == nil && n >= 1 {
			instance = n
			args = nil
		}
	}
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments after %q: %s", input, strings.Join(args, " "))
	}

	k, action, err := ensureKernel(input)
	if err != nil {
		// Signpost: suggest rat setup for common missing-runtime errors.
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "not responding") {
			fmt.Fprintf(os.Stderr, "rat: %s\n", err)
			fmt.Fprintf(os.Stderr, "\nRun %s to install dependencies.\n", s.Bold("rat setup"))
			return err
		}
		return err
	}

	// Apply instance suffix: py@myproject.2, py@myproject.3, etc.
	baseName := k.Name
	if instance >= 2 {
		// Resolve via the instance-aware resolver path.
		instanceInput := fmt.Sprintf("%s.%d", input, instance)
		k, action, err = ensureKernel(instanceInput)
		if err != nil {
			return err
		}
	} else {
		instance = 1
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

	siblings := discoverSiblings(baseName, instance)

	return repl.Run(repl.Config{
		Name:          k.Name,
		Lang:          k.Lang,
		Port:          k.Port,
		Cwd:           k.Cwd,
		Venv:          k.Venv,
		ActivityLog:   activityLog,
		RuntimeConfig: rtCfg,
		ConfigDir:     configDir,
		Instance:      instance,
		Siblings:      siblings,
	})
}

// discoverSiblings returns the instance numbers of all running kernels
// that share the same base name (including the base itself as instance 1).
func discoverSiblings(baseName string, current int) []int {
	kernels, err := store().ListRunning()
	if err != nil {
		return []int{current}
	}
	prefix := baseName + "."
	var instances []int
	for _, k := range kernels {
		if k.Name == baseName {
			instances = append(instances, 1)
		} else if strings.HasPrefix(k.Name, prefix) {
			if n, err := strconv.Atoi(k.Name[len(prefix):]); err == nil {
				instances = append(instances, n)
			}
		}
	}
	// Ensure current is in the list (it may not be running yet).
	found := false
	for _, n := range instances {
		if n == current {
			found = true
			break
		}
	}
	if !found {
		instances = append(instances, current)
	}
	// Sort.
	for i := range instances {
		for j := i + 1; j < len(instances); j++ {
			if instances[j] < instances[i] {
				instances[i], instances[j] = instances[j], instances[i]
			}
		}
	}
	return instances
}

// activityLogPath returns the canonical activity log path for a kernel.
func activityLogPath(name string) string {
	kdir, err := cachedir.Kernels(name)
	if err != nil {
		return filepath.Join(os.Getenv("HOME"), ".cache", "rat", "kernels", name, "activity.jsonl")
	}
	return filepath.Join(kdir, "activity.jsonl")
}
