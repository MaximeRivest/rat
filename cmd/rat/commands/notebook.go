package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

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
	doctorJSON     bool
)

func init() {
	rootCmd.PersistentFlags().StringVar(&docFlag, "doc", "",
		"Resolve language names for this notebook (its folder, or the project it pins)")

	ensureCmd.Flags().BoolVar(&ensureJSON, "json", false, "Print the report as JSON for integrations")
	ensureCmd.Flags().BoolVar(&ensureForce, "force", false, "Reinstall every requirement, ignoring the receipt")
	ensureCmd.Flags().BoolVar(&ensureRecreate, "recreate", false,
		"Delete and rebuild the environment when its interpreter does not satisfy `requires` (destructive)")
	rootCmd.AddCommand(ensureCmd)

	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Print a notebook report as JSON (notebook argument only)")
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
  ---

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
		opts := notebook.Options{Force: ensureForce, Recreate: ensureRecreate}
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
	if r.Python != nil && len(r.Python.Requirements) > 0 {
		fmt.Printf("  %s %s\n", s.Dim("requirements:"), s.Dim(strings.Join(r.Python.Requirements, ", ")))
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
