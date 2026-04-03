package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/state"
)

var (
	stopAll bool
	rmYes   bool
)

func init() {
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all running kernels")
	rmCmd.Flags().BoolVar(&rmYes, "yes", false, "Delete without confirmation")

	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(statusCmd)
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
	Use:     "setup",
	Short:   "Interactive setup wizard",
	GroupID: "setup",
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
	Use:     "add <name> [dir] [--lang LANG] [--venv PATH]",
	Short:   "Register a named runtime",
	GroupID: "setup",
	Long: `Register a named runtime with custom configuration.

The language is inferred from the name prefix (py-ml → py, r-stats → r)
or set explicitly with --lang.

The optional second argument sets the working directory (same as --cwd).
If a Python venv (.venv/) is found in the directory, it is auto-detected.

Examples:
  rat add py-ml ~/ml                  # auto-detect venv in ~/ml
  rat add py-web --venv ~/web/.venv --cwd ~/web
  rat add r-stats --lang r --cwd ~/stats`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if len(args) > 1 && addCwd == "" {
			addCwd = args[1]
		}

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
	Use:     "rm <name>",
	Short:   "Delete a runtime's state",
	GroupID: "setup",
	Long: `Stop the kernel if running and remove the state entry entirely.
Works on any runtime — custom (py-ml) or auto-generated (py@myproject).

This is the only way to fully erase a runtime from state.
'rat stop' marks it stopped; 'rat rm' deletes it.

By default, asks for confirmation. Use --yes to skip the prompt.

Examples:
  rat rm py-ml
  rat rm py@myproject`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := resolveInput(args[0])
		if err != nil {
			return err
		}
		if r.IsNew {
			return fmt.Errorf("runtime %q not found", r.Name)
		}
		if !rmYes {
			ok, err := confirmRemove(r.Name)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintln(os.Stderr, "Cancelled.")
				return nil
			}
		}

		s := store()
		found := false
		if k, _ := s.GetRunning(r.Name); k != nil {
			if err := daemon.Stop(s, r.Name); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s stopped.\n", r.Name)
			found = true
		}
		if removed, _ := s.Remove(r.Name); removed {
			found = true
		}
		if removed, _ := s.RemoveRuntime(r.Name); removed {
			found = true
		}
		if !found {
			return fmt.Errorf("runtime %q not found", r.Name)
		}

		fmt.Fprintf(os.Stderr, "%s removed.\n", r.Name)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show all runtimes and their state",
	GroupID: "daily",
	Long: `Show all known runtimes: running, stopped, and saved named runtimes.

Columns:
  NAME    Resolved runtime name
  STATUS  running or stopped
  CWD     Working directory
  VENV    Python virtual environment (if any)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rows, err := buildStatusRows(store())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("No runtimes.")
			fmt.Println("Start one: rat py")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tCWD\tVENV")
		for _, row := range rows {
			venv := shortPath(row.Venv)
			if venv == "" {
				venv = "—"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row.Name, row.Status, shortPath(row.Cwd), venv)
		}
		w.Flush()
		return nil
	},
}

var startCmd = &cobra.Command{
	Use:     "start <runtime>",
	Short:   "Start a kernel",
	GroupID: "setup",
	Long: `Resolve the name and start the kernel in the background.
If already running and healthy, reports it.

The runtime can be a language (py, sh, r, jl, js) which resolves
to your current project's kernel, or a full name (py@myproject, py-ml).

Examples:
  rat start py
  rat start py@myproject
  rat start py-ml`,
	Args: cobra.ExactArgs(1),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		completions := []string{"py", "r", "jl", "sh", "js"}
		for _, rt := range func() []state.Runtime { rts, _ := store().ListRuntimes(); return rts }() {
			completions = append(completions, rt.Name)
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := resolveInput(args[0])
		if err != nil {
			return err
		}
		k, action, err := ensureResolvedKernel(r)
		if err != nil {
			return err
		}
		if action == ensureNoop {
			fmt.Fprintf(os.Stderr, "%s already running on http://127.0.0.1:%d/mcp (PID %d)\n", k.Name, k.Port, k.PID)
			return nil
		}
		printKernelAction(k, action)
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:     "stop <runtime> [--all]",
	Short:   "Stop a kernel",
	GroupID: "setup",
	Long: `Stop a kernel. The state entry is preserved (marked stopped)
so the name remains resolvable. Use 'rat rm' to delete state entirely.

Examples:
  rat stop py
  rat stop py@myproject
  rat stop --all`,
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

		if len(args) != 1 {
			return fmt.Errorf("specify a runtime name or use --all")
		}

		r, err := resolveInput(args[0])
		if err != nil {
			return err
		}
		if err := daemon.Stop(s, r.Name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s stopped.\n", r.Name)
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:     "restart <runtime>",
	Short:   "Restart a kernel (fresh namespace)",
	GroupID: "daily",
	Long: `Kill the kernel process and start a new one. Fresh namespace,
fresh language subprocess. If no kernel is running, starts one.

Examples:
  rat restart py
  rat restart py@myproject`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := resolveInput(args[0])
		if err != nil {
			return err
		}

		s := store()
		if k, _ := s.GetRunning(r.Name); k != nil {
			if err := daemon.Stop(s, r.Name); err != nil {
				return err
			}
		}

		k, err := daemon.Start(s, daemon.StartOpts{
			Name: r.Name,
			Lang: r.Lang,
			Cwd:  r.Cwd,
			Venv: r.Venv,
		})
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "%s restarted on http://127.0.0.1:%d/mcp (PID %d)\n", k.Name, k.Port, k.PID)
		return nil
	},
}

var resetCmd = &cobra.Command{
	Use:     "reset <runtime>",
	Short:   "Clear namespace without restarting",
	GroupID: "setup",
	Long: `Clear the namespace in-process. Faster than restart but less
reliable. Does NOT auto-start — the kernel must be running.

For a full restart: rat restart <name>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToRunningKernel(ctx, args[0])
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
	Use:     "cancel <runtime>",
	Short:   "Cancel running execution",
	GroupID: "daily",
	Long: `Interrupt the current execution on a kernel (Ctrl-C equivalent).
Does NOT auto-start — if the kernel isn't running, reports it.

Example:
  rat cancel py`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToRunningKernel(ctx, args[0])
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
	Use:     "doctor [<lang>]",
	Short:   "Run diagnostics",
	GroupID: "setup",
	Long: `Run diagnostics for one language or for all implemented languages.

Checks runtime detection, environment/tooling, writable directories,
and the health of running kernels when applicable.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
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
	Use:     "update",
	Short:   "Update rat binary and kernel dependencies",
	GroupID: "setup",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("update not yet implemented")
	},
}

type statusRow struct {
	Name   string
	Status string
	Cwd    string
	Venv   string
}

func buildStatusRows(s *state.Store) ([]statusRow, error) {
	kernels, err := s.ListKnown()
	if err != nil {
		return nil, err
	}
	runtimes, err := s.ListRuntimes()
	if err != nil {
		return nil, err
	}

	rows := make(map[string]statusRow, len(kernels)+len(runtimes))
	for _, rt := range runtimes {
		rows[rt.Name] = statusRow{
			Name:   rt.Name,
			Status: state.StatusStopped,
			Cwd:    rt.Cwd,
			Venv:   rt.Venv,
		}
	}
	for _, k := range kernels {
		rows[k.Name] = statusRow{
			Name:   k.Name,
			Status: k.Status,
			Cwd:    k.Cwd,
			Venv:   k.Venv,
		}
	}

	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]statusRow, 0, len(names))
	for _, name := range names {
		out = append(out, rows[name])
	}
	return out, nil
}

func confirmRemove(name string) (bool, error) {
	if fi, err := os.Stdin.Stat(); err != nil {
		return false, err
	} else if (fi.Mode() & os.ModeCharDevice) == 0 {
		return false, fmt.Errorf("refusing to prompt for confirmation without a terminal; rerun with --yes")
	}

	fmt.Fprintf(os.Stderr, "Remove runtime %s? [y/N] ", name)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

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
