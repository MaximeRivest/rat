package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/state"
)

var stopAll bool

func init() {
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all kernels")

	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(cancelCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(updateCmd)
}

// store returns the default state store. Defined once so all commands share it.
func store() *state.Store {
	return state.DefaultStore()
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive setup wizard",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("setup not yet implemented")
	},
}

var (
	addVenv string
	addCwd  string
	addLang string
)

func init() {
	addCmd.Flags().StringVar(&addVenv, "venv", "", "Python venv path")
	addCmd.Flags().StringVar(&addCwd, "cwd", "", "Working directory (default: current)")
	addCmd.Flags().StringVar(&addLang, "lang", "", "Language (default: inferred from name prefix)")
}

var addCmd = &cobra.Command{
	Use:   "add <name> [<dir>] [--lang py] [--venv PATH] [--cwd PATH]",
	Short: "Register a named runtime",
	Long: `Register a named runtime with a specific venv and working directory.

The language is inferred from the name (pyauto → py, py-ml → py, r-stats → r)
or can be set explicitly with --lang.

The optional second argument sets the working directory (same as --cwd).
If a Python venv (.venv/) is found in the directory, it is used automatically.

Examples:
  rat add pyauto .                    # register for current dir, auto-detect venv
  rat add py-ml --venv ~/ml/.venv --cwd ~/ml
  rat add py-web ~/web               # auto-detect venv in ~/web
  rat add r-bio --lang r --cwd ~/bio`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		// Second positional arg is cwd (shorthand for --cwd).
		if len(args) > 1 && addCwd == "" {
			addCwd = args[1]
		}

		// Infer language from name prefix or --lang flag.
		lang := addLang
		if lang == "" {
			if inferred, ok := inferLangFromName(name); ok {
				lang = inferred
			} else {
				return fmt.Errorf("cannot infer language from %q — use --lang", name)
			}
		} else {
			var err error
			lang, err = resolveLang(lang)
			if err != nil {
				return err
			}
		}

		cwd := addCwd
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		cwd, _ = filepath.Abs(cwd)

		venv := addVenv
		if venv != "" {
			venv, _ = filepath.Abs(venv)
		}

		// Auto-detect venv for Python if not explicitly set.
		if venv == "" && lang == "py" {
			venv = findVenv(cwd)
		}

		if err := store().PutRuntime(state.Runtime{
			Name: name,
			Lang: lang,
			Cwd:  cwd,
			Venv: venv,
		}); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Added %s (lang=%s cwd=%s", name, lang, shortPath(cwd))
		if venv != "" {
			fmt.Fprintf(os.Stderr, " venv=%s", shortPath(venv))
		}
		fmt.Fprintln(os.Stderr, ")")
		fmt.Fprintf(os.Stderr, "Start it: rat start %s\n", name)
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Unregister a named runtime",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		s := store()

		// Stop the kernel if running.
		if k, _ := s.Get(name); k != nil {
			_ = daemon.Stop(s, name)
			fmt.Fprintf(os.Stderr, "%s stopped.\n", name)
		}

		// Remove the saved config.
		found, err := s.RemoveRuntime(name)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("runtime %q not found", name)
		}
		fmt.Fprintf(os.Stderr, "%s removed.\n", name)
		return nil
	},
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all runtimes and their state",
	RunE: func(cmd *cobra.Command, args []string) error {
		kernels, err := store().List()
		if err != nil {
			return err
		}
		if len(kernels) == 0 {
			fmt.Println("No running kernels.")
			fmt.Println("Start one: rat start sh")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tLANG\tPORT\tSTATE\tVENV\tCWD\tSTARTED")
		for _, k := range kernels {
			cwd := shortPath(k.Cwd)
			venv := shortPath(k.Venv)
			if venv == "" {
				venv = "—"
			}
			ago := timeAgo(k.Started)
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
				k.Name, k.Lang, k.Port, "running", venv, cwd, ago)
		}
		w.Flush()
		return nil
	},
}

var startCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a kernel explicitly",
	Long: `Start a kernel in the background.

The name can be:
  • A language:       py, r, jl, sh, js
  • A named runtime:  py-ml, r-stats (registered with 'rat add')

Auto-assigns a port and records in ~/.config/rat/state.yaml.
Auto-detects venv for Python projects.

Examples:
  rat start sh          Start a bash kernel
  rat start py          Start Python with auto-detected venv
  rat start py-ml       Start a named runtime (from 'rat add py-ml')`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("missing runtime name\n\nUsage: rat start <name>\n\nExamples:\n  rat start py\n  rat start sh\n  rat start py-ml\n\nSee 'rat start --help' for details.")
		}
		if len(args) > 1 {
			return fmt.Errorf("accepts 1 runtime name, got %d\n\nUsage: rat start <name>", len(args))
		}
		return nil
	},
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		completions := []string{"py", "r", "jl", "sh", "js"}
		for _, rt := range func() []state.Runtime { rts, _ := store().ListRuntimes(); return rts }() {
			completions = append(completions, rt.Name)
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		s := store()

		// Check for a saved runtime first.
		rt, _ := s.GetRuntime(name)
		var lang, cwd, venv string
		if rt != nil {
			lang = rt.Lang
			cwd = rt.Cwd
			venv = rt.Venv
		} else {
			var err error
			lang, err = resolveLang(name)
			if err != nil {
				return fmt.Errorf("unknown runtime %q — use a language (py, r, jl, sh, js) or a named runtime from 'rat add'\n\nFor help: rat start --help", name)
			}
		}

		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		cwd, _ = filepath.Abs(cwd)

		// Auto-detect venv for Python if not set.
		if venv == "" && lang == "py" {
			venv = findVenv(cwd)
		}

		k, err := daemon.Start(s, daemon.StartOpts{
			Name: name,
			Lang: lang,
			Cwd:  cwd,
			Venv: venv,
		})
		if err != nil {
			return err
		}

		venvMsg := ""
		if k.Venv != "" {
			venvMsg = fmt.Sprintf(" venv=%s", shortPath(k.Venv))
		}
		fmt.Fprintf(os.Stderr, "%s started on http://127.0.0.1:%d/mcp (PID %d)%s\n", k.Name, k.Port, k.PID, venvMsg)
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [<name>] [--all]",
	Short: "Stop a kernel",
	RunE: func(cmd *cobra.Command, args []string) error {
		s := store()

		if stopAll {
			n, err := daemon.StopAll(s)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Println("No kernels running.")
			} else {
				fmt.Fprintf(os.Stderr, "Stopped %d kernel(s).\n", n)
			}
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("specify a runtime name or use --all")
		}

		name := args[0]
		if err := daemon.Stop(s, name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s stopped.\n", name)
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a kernel (fresh namespace, same config)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		s := store()

		// Get current config before stopping
		existing, err := s.Get(name)
		if err != nil {
			return err
		}

		// Resolve lang — either from existing state or from the name
		lang := ""
		cwd := ""
		venv := ""
		if existing != nil {
			lang = existing.Lang
			cwd = existing.Cwd
			venv = existing.Venv

			// Stop the existing kernel
			if err := daemon.Stop(s, name); err != nil {
				return err
			}
		} else {
			// Not running — just start it
			lang, err = resolveLang(name)
			if err != nil {
				return err
			}
			cwd, _ = os.Getwd()
		}

		cwd, _ = filepath.Abs(cwd)

		k, err := daemon.Start(s, daemon.StartOpts{
			Name: name,
			Lang: lang,
			Cwd:  cwd,
			Venv: venv,
		})
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "%s restarted on http://127.0.0.1:%d/mcp (PID %d)\n", k.Name, k.Port, k.PID)
		return nil
	},
}

var resetCmd = &cobra.Command{
	Use:   "reset <name>",
	Short: "Clear the kernel namespace",
	Long: `Clear all variables in a kernel's namespace without restarting.

For full kernel restart, use: rat restart <name>
For direct MCP access:       mcp2cli rat-<name> ctl --op reset`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, args[0])
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Ctl(ctx, "reset")
		if err != nil {
			return err
		}
		fmt.Println(extractText(result))
		return nil
	},
}

var cancelCmd = &cobra.Command{
	Use:   "cancel <name>",
	Short: "Cancel running execution on a kernel",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, args[0])
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Ctl(ctx, "cancel")
		if err != nil {
			return err
		}
		fmt.Println(extractText(result))
		return nil
	},
}

var doctorCmd = &cobra.Command{
	Use:   "doctor [<lang>]",
	Short: "Run diagnostics",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			// Show all diagnostics.
			printShellDoctor(inspectShellEnv())
			fmt.Println("")
			printPythonDoctor(inspectPythonEnv())
			return nil
		}
		lang, err := resolveLang(args[0])
		if err != nil {
			return err
		}
		switch lang {
		case "sh":
			printShellDoctor(inspectShellEnv())
		case "py":
			printPythonDoctor(inspectPythonEnv())
		default:
			return fmt.Errorf("doctor not yet implemented for %s", lang)
		}
		return nil
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update rat binary and kernel dependencies",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("update not yet implemented")
	},
}

// ── helpers ─────────────────────────────────────────────────

func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && len("~/"+rel) < len(p) {
		return "~/" + rel
	}
	return p
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
