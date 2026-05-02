package commands

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/maximerivest/rat/internal/runtimeid"
	"github.com/maximerivest/rat/internal/state"
	"github.com/maximerivest/rat/internal/termstyle"
)

var (
	stopAll       bool
	removeAll     bool
	rmYes         bool
	statusVerbose bool
	statusJSON    bool
)

func init() {
	statusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show details: URL, PID, memory, clients, runtime")
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Print status rows as JSON")
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all running kernels")
	removeCmd.Flags().BoolVar(&removeAll, "all", false, "Delete all runtime state entries")
	removeCmd.Flags().BoolVar(&rmYes, "yes", false, "Delete without confirmation")

	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(removeCmd)
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
		return runSetup()
	},
}

var (
	addVenv    string
	addCwd     string
	addLang    string
	addRuntime string
	addOpt     []string
	addEnv     []string
)

func init() {
	addCmd.Flags().StringVar(&addVenv, "venv", "", "Python venv path")
	addCmd.Flags().StringVar(&addCwd, "cwd", "", "Working directory (default: current)")
	addCmd.Flags().StringVar(&addLang, "lang", "", "Language (default: inferred from name prefix)")
	addCmd.Flags().StringVar(&addRuntime, "runtime", "", "Path to language binary (e.g. /opt/python3.11/bin/python3)")
	addCmd.Flags().StringArrayVar(&addOpt, "opt", nil, "Structured runtime options (KEY=VALUE, repeatable)")
	addCmd.Flags().StringArrayVar(&addEnv, "env", nil, "Extra env vars (KEY=VALUE, repeatable)")
}

var addCmd = &cobra.Command{
	Use:     "add <name> [dir]",
	Short:   "Register a named runtime",
	GroupID: "setup",
	Long: `Register a named runtime with custom configuration.

The language is inferred from the name prefix (py-ml → py, r-stats → r)
or set explicitly with --lang.

The optional second argument sets the working directory (same as --cwd).
If a Python venv (.venv/) is found in the directory, it is auto-detected.

Use --runtime to point at a specific binary. This overrides auto-detection
so rat uses exactly the interpreter you want.

Examples:
  rat add py-ml ~/ml                  # auto-detect venv in ~/ml
  rat add py-web --venv ~/web/.venv --cwd ~/web
  rat add r-stats --lang r --cwd ~/stats
  rat add py-311 --runtime /opt/python3.11/bin/python3
  rat add jl-gpu --runtime ~/julia-nightly/bin/julia --cwd ~/gpu
  rat add pi-sonnet --lang pi --opt model=claude-sonnet-4-5 --opt thinking=high`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := runtimeid.ValidateName(name); err != nil {
			return err
		}

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

		runtimePath := addRuntime
		if runtimePath != "" {
			runtimePath, _ = filepath.Abs(runtimePath)
		}

		optionsMap := parseKVFlags(addOpt)
		envMap := parseKVFlags(addEnv)

		if err := store().PutRuntime(state.Runtime{
			Name:        name,
			Lang:        lang,
			Cwd:         cwd,
			Venv:        venv,
			RuntimePath: runtimePath,
			Options:     optionsMap,
			Env:         envMap,
		}); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Added %s (lang=%s cwd=%s", name, lang, shortPath(cwd))
		if venv != "" {
			fmt.Fprintf(os.Stderr, " venv=%s", shortPath(venv))
		}
		if runtimePath != "" {
			fmt.Fprintf(os.Stderr, " runtime=%s", runtimePath)
		}
		for k, v := range optionsMap {
			fmt.Fprintf(os.Stderr, " opt:%s=%s", k, displayOptionValue(k, v))
		}
		for k, v := range envMap {
			fmt.Fprintf(os.Stderr, " env:%s=%s", k, displayEnvValue(k, v))
		}
		fmt.Fprintln(os.Stderr, ")")
		fmt.Fprintf(os.Stderr, "Start it: rat start %s\n", name)
		return nil
	},
}

var removeCmd = &cobra.Command{
	Use:     "remove <name> [--all]",
	Aliases: []string{"rm"},
	Short:   "Delete a runtime's state",
	GroupID: "setup",
	Long: `Stop the kernel if running and remove the state entry entirely.
Works on any runtime — custom (py-ml) or auto-generated (py@myproject).

This is the only way to fully erase a runtime from state.
'rat stop' marks it stopped; 'rat remove' deletes it.

By default, asks for confirmation. Use --yes to skip the prompt.

Examples:
  rat remove py-ml
  rat remove py@myproject
  rat remove --all`,
	Args: func(cmd *cobra.Command, args []string) error {
		if removeAll {
			if len(args) != 0 {
				return fmt.Errorf("cannot pass a name with --all")
			}
			return nil
		}
		if len(args) != 1 {
			return fmt.Errorf("specify a runtime name or use --all")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		s := store()
		if removeAll {
			names, err := allRuntimeNames(s)
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Fprintln(os.Stderr, "No runtimes to remove.")
				return nil
			}
			if !rmYes {
				ok, err := confirmRemoveAll(names)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(os.Stderr, "Cancelled.")
					return nil
				}
			}
			removed := 0
			for _, name := range names {
				ok, err := removeRuntimeByName(s, name)
				if err != nil {
					return err
				}
				if ok {
					removed++
				}
			}
			fmt.Fprintf(os.Stderr, "Removed %d runtime(s).\n", removed)
			return nil
		}

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

		removed, err := removeRuntimeByName(s, r.Name)
		if err != nil {
			return err
		}
		if !removed {
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
			if statusJSON {
				fmt.Println("[]")
				return nil
			}
			fmt.Println("No runtimes.")
			fmt.Println("Start one: rat py")
			return nil
		}

		enrichStatusRows(rows)

		if statusJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(rows)
		}

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
resolvable. Use 'rat remove' to delete the runtime from state entirely.

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
			Name:        r.Name,
			Lang:        r.Lang,
			Cwd:         r.Cwd,
			Venv:        r.Venv,
			RuntimePath: r.RuntimePath,
			Options:     r.Options,
			Env:         r.Env,
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
	Short:   "Clear namespace (may restart runtime)",
	GroupID: "setup",
	Long: `Clear the runtime namespace.

This keeps the rat MCP server entry running, but many runtimes restart
their interpreter or session under the hood to guarantee a clean state.
It does NOT auto-start — the kernel must already be running.

For an explicit daemon restart: rat restart <name>`,
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
	Name           string `json:"name"`
	Status         string `json:"status"`
	Cwd            string `json:"cwd"`
	Venv           string `json:"venv,omitempty"`
	Port           int    `json:"port,omitempty"`
	PID            int    `json:"pid,omitempty"`
	RuntimeState   string `json:"runtime_state,omitempty"`
	IdleSeconds    int    `json:"idle_seconds,omitempty"`
	MemoryMB       int    `json:"memory_mb,omitempty"`
	Clients        int    `json:"clients,omitempty"`
	ClientNames    string `json:"client_names,omitempty"`
	LastCaller     string `json:"last_caller,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
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
	fmt.Fprintln(w, termstyle.Dim("NAME\tSTATUS\tCWD\tVENV"))
	for _, row := range rows {
		venv := shortVenv(row.Venv, row.Cwd)
		if venv == "" {
			venv = "—"
		}
		name := termstyle.Bold(row.Name)
		status := formatColoredStatus(row.Status)
		note := formatStatusNote(row)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\n", name, status, termstyle.Dim(shortPath(row.Cwd)), termstyle.Dim(venv), note)
	}
	w.Flush()
}

func printVerboseStatus(rows []statusRow) {
	for i, row := range rows {
		if i > 0 {
			fmt.Println()
		}

		if row.Status == state.StatusRunning {
			printRunningCard(row)
		} else {
			printStoppedCard(row)
		}
	}
}

func printRunningCard(row statusRow) {
	// Line 1: name + status
	fmt.Printf("%s  %s\n", termstyle.Bold(row.Name), termstyle.Green("running"))

	// Line 2: vitals — runtime · memory · idle · PID
	var vitals []string
	if row.RuntimeVersion != "" {
		vitals = append(vitals, row.RuntimeVersion)
	}
	if row.MemoryMB > 0 {
		vitals = append(vitals, fmt.Sprintf("%dMB", row.MemoryMB))
	}
	idleStr := "idle <1m"
	if row.IdleSeconds > 0 {
		idleStr = "idle " + formatCompactDuration(time.Duration(row.IdleSeconds)*time.Second)
	}
	if row.IdleSeconds >= int(idleWarningAfter.Seconds()) {
		vitals = append(vitals, termstyle.Yellow("⚠ "+idleStr))
	} else {
		vitals = append(vitals, idleStr)
	}
	if row.PID > 0 {
		vitals = append(vitals, fmt.Sprintf("PID %d", row.PID))
	}
	fmt.Printf("  %s\n", termstyle.Dim(strings.Join(vitals, " · ")))

	// Line 3: URL
	if row.Port > 0 {
		fmt.Printf("  %s\n", termstyle.Cyan(fmt.Sprintf("http://127.0.0.1:%d/mcp", row.Port)))
	}

	// Line 4: location
	loc := shortPath(row.Cwd)
	if row.Venv != "" {
		loc += " · " + shortVenv(row.Venv, row.Cwd)
	}
	fmt.Printf("  %s\n", termstyle.Dim(loc))

	// Line 5: clients (only if any)
	if row.ClientNames != "" {
		fmt.Printf("  Clients: %s\n", row.ClientNames)
	} else if row.Clients > 0 {
		fmt.Printf("  Clients: %d\n", row.Clients)
	}
}

func printStoppedCard(row statusRow) {
	fmt.Printf("%s  %s\n", termstyle.Dim(row.Name), termstyle.Dim("stopped"))
	loc := shortPath(row.Cwd)
	if row.Venv != "" {
		loc += " · " + shortVenv(row.Venv, row.Cwd)
	}
	fmt.Printf("  %s\n", termstyle.Dim(loc))
}

// shortVenv returns the venv path relative to cwd if it's inside it,
// otherwise the short absolute path. ".venv" inside the project shows
// as just ".venv" rather than the full path.
func shortVenv(venv, cwd string) string {
	if venv == "" {
		return ""
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, venv); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return shortPath(venv)
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
		return termstyle.Yellow(fmt.Sprintf("  ⚠ idle %s", formatCompactDuration(time.Duration(row.IdleSeconds)*time.Second)))
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
		fmt.Printf(" Stop idle ones? %s", termstyle.Bold("rat stop "+firstIdle))
	}
	fmt.Println("")
}

func formatColoredStatus(status string) string {
	switch status {
	case state.StatusRunning:
		return termstyle.Green(status)
	default:
		return termstyle.Dim(status)
	}
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

func confirmRemoveAll(names []string) (bool, error) {
	if fi, err := os.Stdin.Stat(); err != nil {
		return false, err
	} else if (fi.Mode() & os.ModeCharDevice) == 0 {
		return false, fmt.Errorf("refusing to prompt for confirmation without a terminal; rerun with --yes")
	}

	fmt.Fprintf(os.Stderr, "Remove all %d runtimes? [y/N] ", len(names))
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

func allRuntimeNames(s *state.Store) ([]string, error) {
	kernels, err := s.ListKnown()
	if err != nil {
		return nil, err
	}
	runtimes, err := s.ListRuntimes()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(kernels)+len(runtimes))
	for _, k := range kernels {
		if !seen[k.Name] {
			seen[k.Name] = true
			names = append(names, k.Name)
		}
	}
	for _, r := range runtimes {
		if !seen[r.Name] {
			seen[r.Name] = true
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func removeRuntimeByName(s *state.Store, name string) (bool, error) {
	found := false
	if k, _ := s.GetRunning(name); k != nil {
		if err := daemon.Stop(s, name); err != nil {
			return false, err
		}
		fmt.Fprintf(os.Stderr, "%s stopped.\n", name)
		found = true
	}
	if removed, err := s.Remove(name); err != nil {
		return false, err
	} else if removed {
		found = true
	}
	if removed, err := s.RemoveRuntime(name); err != nil {
		return false, err
	} else if removed {
		found = true
	}
	return found, nil
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
