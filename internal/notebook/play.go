package notebook

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/mcpclient"
	resolver "github.com/maximerivest/rat/internal/resolve"
	"github.com/maximerivest/rat/internal/state"
)

// PlayOptions control Play.
type PlayOptions struct {
	// PrerequisitesOnly runs the `rat.after` chain and stops before this
	// notebook's own cells (a client that runs cells itself uses this).
	PrerequisitesOnly bool
	// Force replays prerequisites even when the kernel already ran them.
	Force bool
	// Timeout bounds one cell. Zero means no limit.
	Timeout time.Duration
	// OnCell is called after each cell finishes.
	OnCell func(run *NotebookRun, cell CellResult)
	// OnNotebook is called when a notebook is about to run or was skipped.
	OnNotebook func(run *NotebookRun)
	// Ensure options for the environment step that precedes the run.
	Ensure Options
}

// PlayReport is what Play returns.
type PlayReport struct {
	Notebook string         `json:"notebook"`
	Ensure   *Report        `json:"ensure"`
	Runs     []*NotebookRun `json:"runs"`
	OK       bool           `json:"ok"`
}

// NotebookRun is one notebook's execution within a play.
type NotebookRun struct {
	Notebook string       `json:"notebook"`
	Role     string       `json:"role"` // "prerequisite" | "notebook"
	Skipped  bool         `json:"skipped"`
	Reason   string       `json:"reason,omitempty"`
	Cells    []CellResult `json:"cells"`
	OK       bool         `json:"ok"`
	Seconds  float64      `json:"seconds"`
}

// CellResult is one executed cell.
type CellResult struct {
	Line    int     `json:"line"`
	Lang    string  `json:"lang"`
	Kernel  string  `json:"kernel"`
	OK      bool    `json:"ok"`
	Output  string  `json:"output"`
	Seconds float64 `json:"seconds"`
}

// Play makes the notebook's environment true, runs its `rat.after` chain
// (each prerequisite once per kernel lifetime), then runs its own cells
// top to bottom, stopping at the first failing cell. Outputs are returned,
// never written into the files: the document is the author's.
func Play(store *state.Store, nb *Notebook, opts PlayOptions) (*PlayReport, error) {
	report := &PlayReport{Notebook: nb.Path, Runs: []*NotebookRun{}}
	ensured, err := Ensure(store, nb, opts.Ensure)
	if err != nil {
		return nil, err
	}
	report.Ensure = ensured
	if ensured.Blocked {
		return report, nil
	}
	for _, s := range ensured.Steps {
		if !s.OK {
			return report, nil
		}
	}
	root := ensured.Project

	chain, err := nb.Chain()
	if err != nil {
		return nil, err
	}
	for _, dep := range chain {
		run := &NotebookRun{Notebook: dep.Path, Role: "prerequisite", Cells: []CellResult{}}
		report.Runs = append(report.Runs, run)
		if !opts.Force && playedIn(store, dep, root) {
			run.Skipped, run.OK, run.Reason = true, true, "already ran in this kernel"
			if opts.OnNotebook != nil {
				opts.OnNotebook(run)
			}
			continue
		}
		if opts.OnNotebook != nil {
			opts.OnNotebook(run)
		}
		if !playNotebook(store, dep, root, run, opts) {
			return report, nil
		}
	}
	if opts.PrerequisitesOnly {
		report.OK = true
		return report, nil
	}
	run := &NotebookRun{Notebook: nb.Path, Role: "notebook", Cells: []CellResult{}}
	report.Runs = append(report.Runs, run)
	if opts.OnNotebook != nil {
		opts.OnNotebook(run)
	}
	report.OK = playNotebook(store, nb, root, run, opts)
	return report, nil
}

// playNotebook runs every cell of n; on success it records n as played in
// each kernel it used. Returns false at the first failing cell.
func playNotebook(store *state.Store, n *Notebook, root string, run *NotebookRun, opts PlayOptions) bool {
	t0 := time.Now()
	defer func() { run.Seconds = time.Since(t0).Seconds() }()
	kernels := map[string]bool{}
	for _, cell := range n.Cells {
		if strings.TrimSpace(cell.Code) == "" {
			continue
		}
		res, err := resolver.ResolveWith(store, cell.Lang, resolver.Options{Cwd: n.Dir, ProjectRoot: root})
		if err != nil {
			run.Cells = append(run.Cells, CellResult{Line: cell.Line, Lang: cell.Lang, Output: err.Error()})
			return false
		}
		result := runCell(store, res, cell, opts.Timeout)
		if opts.OnCell != nil {
			opts.OnCell(run, result)
		}
		run.Cells = append(run.Cells, result)
		if !result.OK {
			return false
		}
		kernels[res.Name] = true
	}
	for name := range kernels {
		_, _ = store.MarkPlayed(name, n.Path)
	}
	run.OK = true
	return true
}

func runCell(store *state.Store, res *resolver.Result, cell Cell, timeout time.Duration) CellResult {
	t0 := time.Now()
	out := CellResult{Line: cell.Line, Lang: cell.Lang, Kernel: res.Name}
	defer func() { out.Seconds = time.Since(t0).Seconds() }()
	k, err := daemon.Start(store, daemon.StartOpts{Name: res.Name, Lang: res.Lang, Cwd: res.Cwd, Venv: res.Venv,
		RuntimePath: res.RuntimePath, Options: res.Options, Env: res.Env})
	if err != nil {
		out.Output = err.Error()
		return out
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var streamed strings.Builder
	session, err := mcpclient.Connect(ctx, k.Port, mcpclient.ConnectOpts{
		OnNotification: func(n mcp.JSONRPCNotification) {
			if n.Method == "rat/output" {
				if text, ok := n.Params.AdditionalFields["text"].(string); ok {
					streamed.WriteString(text)
				}
			}
		},
		// A cell that asks for input cannot be answered by a batch run.
		Elicitation: cancelElicitor{},
	})
	if err != nil {
		out.Output = err.Error()
		return out
	}
	defer session.Close()
	result, err := session.Run(ctx, cell.Code)
	if err != nil {
		out.Output = strings.TrimSpace(streamed.String() + "\n" + err.Error())
		return out
	}
	text := mcpclient.ExtractText(result)
	if s := streamed.String(); s != "" && !strings.HasPrefix(strings.TrimSpace(text), strings.TrimSpace(s)) {
		text = s + text
	}
	out.Output = strings.TrimSpace(statusTail.ReplaceAllString(text, ""))
	out.OK = !result.IsError
	return out
}

// statusTail is the kernel's own "✓ 21ms | 1 var" line appended to every
// result. Play reports program output, not that chrome.
var statusTail = regexp.MustCompile(`\n?[✓✗] \d+(?:\.\d+)?m?s( \| \d+ vars?)?\s*$`)

type cancelElicitor struct{}

func (cancelElicitor) Elicit(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	return &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionCancel}}, nil
}

// Rel is a display helper for paths relative to a notebook.
func Rel(base, p string) string { return rel(base, p) }
