package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/maximerivest/rat/internal/notebook"
	resolver "github.com/maximerivest/rat/internal/resolve"
	s "github.com/maximerivest/rat/internal/termstyle"
)

// docFlag is the notebook every language name is resolved against. It is
// a root flag so that run, cancel, restart, look, tail and resolve all
// agree on which kernel a notebook means: from its folder, or from the
// project it pins in front matter.
var docFlag string

var (
	ensureJSON     bool
	ensureForce    bool
	ensureRecreate bool
	ensureUpdate   bool
	doctorJSON     bool

	playJSON          bool
	playForce         bool
	playPrerequisites bool
	playTimeout       time.Duration
)

func init() {
	rootCmd.PersistentFlags().StringVar(&docFlag, "doc", "",
		"Resolve language names for this notebook (its folder, or the project it pins)")

	ensureCmd.Flags().BoolVar(&ensureJSON, "json", false, "Print the report as JSON for integrations")
	ensureCmd.Flags().BoolVar(&ensureForce, "force", false, "Reinstall every requirement, ignoring the receipt")
	ensureCmd.Flags().BoolVar(&ensureRecreate, "recreate", false,
		"Delete and rebuild the environment when its interpreter does not satisfy `requires` (destructive)")
	ensureCmd.Flags().BoolVar(&ensureUpdate, "update", false,
		"Resolve the declarations again (newest versions allowed) instead of reproducing the lock files, and write new ones")
	rootCmd.AddCommand(ensureCmd)

	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Print a notebook report as JSON (notebook argument only)")

	playCmd.Flags().BoolVar(&playJSON, "json", false, "Print the run report as JSON for integrations")
	playCmd.Flags().BoolVar(&playForce, "force", false, "Replay prerequisites even when the kernel already ran them")
	playCmd.Flags().BoolVar(&playPrerequisites, "prerequisites", false, "Run only the `rat.after` chain, not this notebook's cells")
	playCmd.Flags().DurationVar(&playTimeout, "timeout", 0, "Maximum time for one cell (0 = no limit)")
	rootCmd.AddCommand(playCmd)
}

// indentWriter prints streamed text with a prefix at every line start and
// remembers what it printed for the cell in progress.
type indentWriter struct {
	mu      sync.Mutex
	prefix  string
	midLine bool
	printed strings.Builder
	echo    echoFilter
}

// expectEcho: the next output repeats an answer typed at the terminal.
func (w *indentWriter) expectEcho(answer string, secret bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.echo.expect(answer, secret)
	w.midLine = false // the person's Enter ended the line
}

func (w *indentWriter) write(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.printed.WriteString(text)
	text = w.echo.filter(text)
	var b strings.Builder
	for _, r := range text {
		if !w.midLine && r != '\n' {
			b.WriteString(w.prefix)
			w.midLine = true
		}
		b.WriteRune(r)
		if r == '\n' {
			w.midLine = false
		}
	}
	fmt.Print(b.String())
}

// filterEcho applies the pending echo to text the result brought.
func (w *indentWriter) filterEcho(text string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.echo.filter(text)
	w.echo.pending = ""
	return out
}

// take ends the cell in progress: finishes its last line and returns all
// it printed.
func (w *indentWriter) take() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.midLine {
		fmt.Println()
		w.midLine = false
	}
	out := w.printed.String()
	w.printed.Reset()
	return out
}

// notebookForDoc loads the --doc notebook, or returns nil when unset.
func notebookForDoc() (*notebook.Notebook, error) {
	if docFlag == "" {
		return nil, nil
	}
	nb, err := notebook.Load(docFlag)
	if err != nil {
		return nil, fmt.Errorf("--doc: %w", err)
	}
	return nb, nil
}

// resolveOptions returns how language names resolve for this invocation:
// relative to the notebook when --doc is given, else the current directory.
func resolveOptions() (resolver.Options, error) {
	nb, err := notebookForDoc()
	if err != nil {
		return resolver.Options{}, err
	}
	if nb != nil {
		opts := resolver.Options{Cwd: nb.Dir}
		if root, ok := nb.ProjectRoot(); ok {
			opts.ProjectRoot = root
		}
		return opts, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return resolver.Options{}, err
	}
	cwd, _ = filepath.Abs(cwd)
	return resolver.Options{Cwd: cwd}, nil
}

// loadNotebookArg accepts a notebook path argument: an existing file.
func loadNotebookArg(arg string) (*notebook.Notebook, error) {
	st, err := os.Stat(arg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", arg, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory; pass a notebook file", arg)
	}
	return notebook.Load(arg)
}

var ensureCmd = &cobra.Command{
	Use:     "ensure <notebook.md>",
	Short:   "Make a notebook's environment match what it declares",
	GroupID: "daily",
	Long: `Make a notebook runnable.

Reads the notebook's front matter (and any PEP 723 block in its python
cells), then brings the project's environment to that state: creates
the venv if missing, installs what is not yet installed, and restarts
the kernel only when its binding changed or an editable package was
installed. Already-satisfied requirements are skipped, so a second
call is a no-op.

A notebook declares its needs like this:

  ---
  rat:
    project: ../..            # optional: which project this runs in
    python:
      requires: ">=3.11"      # optional interpreter version
      dependencies:           # requirements.txt lines
        - -e .                # this project, editable
        - websockets
    r:
      dependencies:           # pak references: dplyr, ggplot2@3.5.1, owner/repo@ref
        - dplyr
    julia:
      dependencies:           # DataFrames, Plots@1.40, a Git URL#rev, ./LocalPkg
        - DataFrames
  ---

R and Julia packages are checked and installed by the runtime's own
script (packages.R, packages.jl). ensure writes lock files — .rat/
python.lock, .rat/r.lock, .rat/julia/Manifest.toml — that belong in Git:
on the next machine ensure reproduces their versions; --update resolves
again.

Without a declaration the notebook still runs on its project's
environment; ensure then only makes sure that environment exists.

See 'rat doctor <notebook.md>' for the plan without changing anything.

Examples:
  rat ensure docs/tutorial.md
  rat ensure docs/tutorial.md --json
  rat ensure docs/tutorial.md --recreate   # rebuild a venv with the wrong Python`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nb, err := loadNotebookArg(args[0])
		if err != nil {
			return err
		}
		opts := notebook.Options{Force: ensureForce, Recreate: ensureRecreate, Update: ensureUpdate}
		if !ensureJSON {
			opts.Progress = func(step notebook.Step) {
				mark := s.Green("✓")
				if !step.OK {
					mark = s.Red("✗")
				}
				fmt.Fprintf(os.Stderr, "%s %s %s\n", mark, step.Action.Label, s.Dim(fmt.Sprintf("%.1fs", step.Seconds)))
				if !step.OK && step.Output != "" {
					fmt.Fprintln(os.Stderr, indent(lastLines(step.Output, 20), "    "))
				}
			}
		}
		report, err := notebook.Ensure(store(), nb, opts)
		if err != nil {
			return err
		}
		if ensureJSON {
			return printJSON(report)
		}
		printReport(report, true)
		if !report.OK {
			os.Exit(1)
		}
		return nil
	},
}

var playCmd = &cobra.Command{
	Use:     "play <notebook.md>",
	Short:   "Run a notebook's cells top to bottom (after its prerequisites)",
	GroupID: "daily",
	Long: `Run a notebook.

First makes its environment true (as 'rat ensure' would), then runs the
notebooks it declares in 'rat.after' — each once per kernel lifetime —
and finally its own cells, top to bottom on the project's kernels,
stopping at the first failing cell. Outputs are printed, not written
into the files.

A notebook declares prerequisites in its front matter:

  ---
  rat:
    after: [./01-load-data.md]
  ---

The chain runs in the same kernel, so state carries over. The kernel
remembers what it played; 'rat restart' forgets.

Examples:
  rat play docs/analysis.md
  rat play docs/analysis.md --prerequisites   # only the chain
  rat play docs/analysis.md --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nb, err := loadNotebookArg(args[0])
		if err != nil {
			return err
		}
		opts := notebook.PlayOptions{PrerequisitesOnly: playPrerequisites, Force: playForce, Timeout: playTimeout}
		if !playJSON {
			opts.OnNotebook = func(run *notebook.NotebookRun) {
				name := shortPath(run.Notebook)
				switch {
				case run.Skipped:
					fmt.Fprintf(os.Stderr, "%s %s %s\n", s.Dim("↷"), name, s.Dim(run.Reason))
				case run.Role == "prerequisite":
					fmt.Fprintf(os.Stderr, "%s %s %s\n", s.Cyan("▶"), name, s.Dim("(prerequisite)"))
				default:
					fmt.Fprintf(os.Stderr, "%s %s\n", s.Cyan("▶"), name)
				}
			}
			// The notebook's own cells print their output as it arrives,
			// so a cell that waits on a person (a sign-in code, a prompt)
			// shows what it waits for. Prerequisites stay quiet unless
			// they fail.
			live := &indentWriter{prefix: "    "}
			opts.OnOutput = func(run *notebook.NotebookRun, text string) {
				if run.Role == "notebook" {
					live.write(text)
				}
			}
			if term.IsTerminal(int(os.Stdin.Fd())) {
				in := bufio.NewReader(os.Stdin)
				opts.OnInput = func(_ *notebook.NotebookRun, _ string, secret bool) (string, bool) {
					if secret {
						b, err := term.ReadPassword(int(os.Stdin.Fd()))
						fmt.Fprintln(os.Stderr)
						live.expectEcho("", true)
						return string(b), err == nil
					}
					line, err := in.ReadString('\n')
					live.expectEcho(line, false)
					return line, err == nil || line != ""
				}
			}
			opts.OnCell = func(run *notebook.NotebookRun, c notebook.CellResult) {
				// Output first (streamed, then what the result adds), the
				// verdict under it — as it happened.
				printed := live.take()
				rest := c.Output
				if printed != "" {
					rest = trimAlreadyPrinted(c.Output, printed)
				}
				rest = strings.TrimLeft(live.filterEcho(rest), "\n")
				if rest != "" && (!c.OK || run.Role == "notebook") {
					fmt.Println(indent(lastLines(rest, 40), "    "))
				}
				mark := s.Green("✓")
				if !c.OK {
					mark = s.Red("✗")
				}
				fmt.Fprintf(os.Stderr, "  %s %s cell at line %d %s\n", mark, c.Lang, c.Line, s.Dim(fmt.Sprintf("%.1fs", c.Seconds)))
			}
			opts.Ensure.Progress = func(step notebook.Step) {
				mark := s.Green("✓")
				if !step.OK {
					mark = s.Red("✗")
				}
				fmt.Fprintf(os.Stderr, "%s %s %s\n", mark, step.Action.Label, s.Dim(fmt.Sprintf("%.1fs", step.Seconds)))
			}
		}
		report, err := notebook.Play(store(), nb, opts)
		if err != nil {
			return err
		}
		if playJSON {
			return printJSON(report)
		}
		if report.Ensure != nil && report.Ensure.Blocked {
			printReport(report.Ensure, true)
		}
		if !report.OK {
			os.Exit(1)
		}
		return nil
	},
}

// runNotebookDoctor handles `rat doctor <notebook.md>`.
func runNotebookDoctor(path string) error {
	nb, err := loadNotebookArg(path)
	if err != nil {
		return err
	}
	report, err := notebook.Doctor(store(), nb, notebook.Options{})
	if err != nil {
		return err
	}
	if doctorJSON {
		return printJSON(report)
	}
	printReport(report, false)
	if !report.OK {
		os.Exit(1)
	}
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printReport(r *notebook.Report, applied bool) {
	fmt.Printf("%s %s\n", s.Bold("notebook"), shortPath(r.Notebook))
	for _, c := range r.Checks {
		mark := s.Green("✓")
		if !c.OK {
			mark = s.Red("✗")
		}
		fmt.Printf("%s %-14s %s\n", mark, c.Label, c.Detail)
		if !c.OK && c.Hint != "" {
			fmt.Printf("  %s %s\n", s.Dim("→"), c.Hint)
		}
	}
	for _, a := range r.After {
		mark := s.Green("✓")
		note := "ran in this kernel"
		if !a.Played {
			mark = s.Dim("·")
			note = "not yet run — rat play runs it first"
		}
		fmt.Printf("  %s %s %s\n", mark, shortPath(a.Path), s.Dim(note))
	}
	if r.Python != nil && len(r.Python.Requirements) > 0 {
		fmt.Printf("  %s %s\n", s.Dim("requirements:"), s.Dim(strings.Join(r.Python.Requirements, ", ")))
	}
	var keys []string
	for k := range r.Envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if es := r.Envs[k]; len(es.Requirements) > 0 {
			fmt.Printf("  %s %s\n", s.Dim("rat."+k+":"), s.Dim(strings.Join(es.Requirements, ", ")))
		}
	}
	if len(r.Actions) > 0 {
		if applied {
			fmt.Printf("\n%s\n", s.Yellow("still to do:"))
		} else {
			fmt.Printf("\n%s\n", s.Bold("rat ensure would:"))
		}
		for _, a := range r.Actions {
			fmt.Printf("  • %s %s\n", a.Label, s.Dim("— "+a.Reason))
			if a.Effect != "" {
				fmt.Printf("    %s\n", s.Yellow(a.Effect))
			}
			if len(a.Command) > 0 {
				fmt.Printf("    %s\n", s.Dim(strings.Join(a.Command, " ")))
			}
		}
	}
	switch {
	case r.OK && applied && len(r.Steps) > 0:
		fmt.Printf("\n%s\n", s.Green("Ready."))
	case r.OK:
		fmt.Printf("\n%s\n", s.Green("Ready — nothing to do."))
	case r.Blocked:
		fmt.Printf("\n%s\n", s.Red("Blocked: fix the items marked ✗ above."))
	}
}

func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = append([]string{"…"}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}
