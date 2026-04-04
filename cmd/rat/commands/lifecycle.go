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
	"github.com/maximerivest/rat/internal/mcpclient"
	"github.com/maximerivest/rat/internal/state"
)

var (
	stopAll       bool
	rmYes         bool
	statusVerbose bool
)

func init() {
	statusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show details: URL, PID, memory, clients, runtime")
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
	Short:   "What's running",
	GroupID: "daily",
	Long: `Show all known runtimes and what they're up to.

For running kernels, queries each one live to report idle time,
memory usage, and connected clients. Kernels idle for more than
24 hours get a ⚠ nudge so you know what to clean up.

  NAME             STATUS   CWD                   VENV
  py@myproject     running  ~/Projects/myproject   .venv
  py@old-thing     running  ~/Projects/old-thing   .venv   ⚠ idle 3d
  sh@myproject     stopped  ~/Projects/myproject   —

  2 kernels using ~185MB. Stop idle ones? rat stop py@old-thing

Use -v for the full picture: runtime version, connected clients,
URL, PID, and memory.

  rat status -v`,
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

		enrichStatusRows(rows)

		if statusVerbose {
			printVerboseStatus(rows)
		} else {
			printCompactStatus(rows)
		}
		printStatusSummary(rows)
		return nil
	},
}

var startCmd = &cobra.Command{
	Use:     "start <runtime>",
	Short:   "Start a kernel",
	GroupID: "setup",
	Long: `Start a kernel.

Resolves the runtime name, starts the kernel in the background, and
reports where it is listening. If it is already running and healthy,
reports that instead.

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
	Long: `Stop a kernel.

The state entry is preserved and marked stopped, so the name remains
resolvable. Use 'rat rm' to delete the runtime from state entirely.

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
	Short:   "Fresh start",
	GroupID: "daily",
	Long: `Restart a kernel with a fresh namespace.

Kills the current kernel process and starts a new one. If no kernel is
running, starts a fresh one using the same resolution path as 'rat py'.

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
	Short:   "Clear namespace (keep process)",
	GroupID: "setup",
	Long: `Clear the namespace without restarting the process.

This is faster than restart but less reliable. It does NOT auto-start —
the kernel must already be running.

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
	Short:   "Unstick",
	GroupID: "daily",
	Long: `Interrupt the current execution on a kernel.

This is the Ctrl-C equivalent. It does NOT auto-start — if the kernel
isn't running, rat reports that and exits.

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
	Short:   "Diagnostics",
	GroupID: "setup",
	Long: `Run diagnostics.

Checks runtime detection, environment/tooling, writable directories,
and the health of running kernels when applicable. Use an optional
language argument to focus the report.`,
	Args: cobra.MaximumNArgs(1),
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
	Short:   "Update rat",
	GroupID: "setup",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("update not yet implemented")
	},
}

const idleWarningAfter = 24 * time.Hour

type statusRow struct {
	Name           string
	Status         string
	Cwd            string
	Venv           string
	Port           int
	PID            int
	RuntimeState   string
	IdleSeconds    int
	MemoryMB       int
	Clients        int
	ClientNames    string
	LastCaller     string
	RuntimeVersion string
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
			Port:   k.Port,
			PID:    k.PID,
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

func enrichStatusRows(rows []statusRow) {
	for i := range rows {
		if rows[i].Status != state.StatusRunning || rows[i].Port == 0 {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		session, err := mcpclient.Connect(ctx, rows[i].Port)
		if err != nil {
			cancel()
			continue
		}

		status, err := session.Status(ctx)
		_ = session.Close()
		cancel()
		if err != nil {
			continue
		}

		rows[i].RuntimeState = status.State
		rows[i].IdleSeconds = status.IdleSeconds
		rows[i].MemoryMB = status.MemoryMB
		rows[i].Clients = status.Clients
		rows[i].ClientNames = status.ClientNames
		rows[i].LastCaller = status.LastCaller
		rows[i].RuntimeVersion = status.RuntimeVersion
	}
}

func printCompactStatus(rows []statusRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tCWD\tVENV")
	for _, row := range rows {
		venv := shortPath(row.Venv)
		if venv == "" {
			venv = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\n", row.Name, row.Status, shortPath(row.Cwd), venv, formatStatusNote(row))
	}
	w.Flush()
}

func printVerboseStatus(rows []statusRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tRUNTIME\tCLIENTS\tMEM\tIDLE\tURL\tPID\tCWD\tVENV")
	for _, row := range rows {
		venv := shortPath(row.Venv)
		if venv == "" {
			venv = "—"
		}

		rt := "—"
		clients := "—"
		mem := "—"
		idle := "—"
		url := "—"
		pid := "—"

		if row.Status == state.StatusRunning {
			if row.RuntimeVersion != "" {
				rt = row.RuntimeVersion
			}
			if row.ClientNames != "" {
				clients = row.ClientNames
			} else if row.Clients > 0 {
				clients = fmt.Sprintf("%d", row.Clients)
			} else {
				clients = "0"
			}
			if row.MemoryMB > 0 {
				mem = fmt.Sprintf("%dMB", row.MemoryMB)
			}
			if row.IdleSeconds > 0 {
				idle = formatCompactDuration(time.Duration(row.IdleSeconds) * time.Second)
			} else {
				idle = "<1m"
			}
			if row.Port > 0 {
				url = fmt.Sprintf("http://127.0.0.1:%d/mcp", row.Port)
			}
			if row.PID > 0 {
				pid = fmt.Sprintf("%d", row.PID)
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Name, row.Status, rt, clients, mem, idle, url, pid, shortPath(row.Cwd), venv)
	}
	w.Flush()
}

func formatStatusNote(row statusRow) string {
	if row.Status != state.StatusRunning {
		return ""
	}
	if row.RuntimeState == "busy" {
		return "  busy"
	}
	if row.RuntimeState == "waiting_for_input" {
		return "  waiting for input"
	}
	if row.IdleSeconds >= int(idleWarningAfter.Seconds()) {
		return fmt.Sprintf("  ⚠ idle %s", formatCompactDuration(time.Duration(row.IdleSeconds)*time.Second))
	}
	return ""
}

func printStatusSummary(rows []statusRow) {
	running := 0
	totalMem := 0
	firstIdle := ""
	for _, row := range rows {
		if row.Status != state.StatusRunning {
			continue
		}
		running++
		totalMem += row.MemoryMB
		if firstIdle == "" && row.IdleSeconds >= int(idleWarningAfter.Seconds()) {
			firstIdle = row.Name
		}
	}
	if running == 0 {
		return
	}

	noun := "kernels"
	if running == 1 {
		noun = "kernel"
	}

	fmt.Println("")
	if totalMem > 0 {
		fmt.Printf("%d %s using ~%dMB.", running, noun, totalMem)
	} else {
		fmt.Printf("%d %s running.", running, noun)
	}
	if firstIdle != "" {
		fmt.Printf(" Stop idle ones? rat stop %s", firstIdle)
	}
	fmt.Println("")
}

func formatCompactDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d < 14*24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dw", int(d.Hours()/(24*7)))
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
