// Package bash implements the Kernel interface for bash.
//
// It manages a persistent bash process via a PTY (pseudo-terminal).
// Code sent to Run() is written to the PTY, output is read back.
// Variables, completions, and hover all come from the live bash session.
//
// This wraps the BashWorker (copied from mrmd-bash) to satisfy
// the kernel.Kernel interface. Same code, new interface.
package bash

import (
	"fmt"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/kernel"
)

// Bash implements kernel.Kernel for bash.
//
// Go struct = Python class, roughly.
// Fields are like instance variables.
// The `worker` field holds the actual PTY-based bash process.
type Bash struct {
	worker *BashWorker // from worker.go (copied from mrmd-bash)
	cwd    string
}

// New creates a new bash kernel.
//
// In Go, "constructors" are just functions that return a struct.
// By convention they're called New or New<Type>.
func New(cwd string) (*Bash, error) {
	w, err := NewBashWorker(cwd, nil)
	if err != nil {
		return nil, fmt.Errorf("start bash: %w", err)
	}
	return &Bash{worker: w, cwd: cwd}, nil
}

// Run executes bash code in the persistent session.
func (b *Bash) Run(code string) kernel.RunResult {
	start := time.Now()
	// Execute(code, storeHistory, execID)
	result := b.worker.Execute(code, true, "")

	output := strings.TrimSpace(result.Stdout)
	errMsg := ""
	if result.Error != nil {
		errMsg = result.Error.Message
		if output != "" {
			errMsg = output + "\n" + errMsg
		}
	}

	return kernel.RunResult{
		Success:   result.Success,
		Output:    output,
		Error:     errMsg,
		ExecCount: result.ExecutionCount,
		Duration:  int(time.Since(start).Milliseconds()),
	}
}

// SendInput writes text directly to the bash PTY.
// This does NOT acquire the execution lock — it can be called while
// a command is running (which is the whole point: the running command
// is waiting for input).
func (b *Bash) SendInput(text string) error {
	return b.worker.SendInput(text)
}

// Look inspects the bash session: variables, completions, or symbol info.
func (b *Bash) Look(req kernel.LookRequest) kernel.LookResult {
	// Completions
	if req.Code != "" {
		cr := b.worker.Complete(req.Code, req.Cursor)
		if len(cr.Matches) == 0 {
			return kernel.LookResult{Text: "No completions."}
		}
		var lines []string
		for _, m := range cr.Matches {
			lines = append(lines, fmt.Sprintf("%-20s %s", m.Label, m.Kind))
		}
		return kernel.LookResult{Text: strings.Join(lines, "\n")}
	}

	// Inspect a specific symbol
	if req.At != "" {
		sym := req.At
		// Strip leading $ if present — user might type "look --at $MY_VAR" or "look --at MY_VAR"
		sym = strings.TrimPrefix(sym, "$")

		// Try as a variable first (most common case for "look --at")
		detail := b.worker.GetVariableDetail(sym, nil, 1000)
		if detail.Type != "undefined" {
			text := fmt.Sprintf("%s (%s)", sym, detail.Type)
			if detail.Value != "" {
				text += fmt.Sprintf(" = %s", detail.Value)
			}
			if detail.Truncated {
				text += " [truncated]"
			}
			return kernel.LookResult{Text: text}
		}

		// Fall back to command type lookup
		hr := b.worker.Hover(sym, len(sym))
		if !hr.Found {
			return kernel.LookResult{Text: fmt.Sprintf("%s: not found", req.At)}
		}
		var parts []string
		if hr.Name != nil {
			parts = append(parts, *hr.Name)
		}
		if hr.Type != nil {
			parts = append(parts, fmt.Sprintf("(%s)", *hr.Type))
		}
		if hr.Value != nil {
			parts = append(parts, fmt.Sprintf("= %s", *hr.Value))
		}
		return kernel.LookResult{Text: strings.Join(parts, " ")}
	}

	// Overview: list variables
	vr := b.worker.GetVariables("")
	if len(vr.Variables) == 0 {
		return kernel.LookResult{Text: fmt.Sprintf("bash idle | 0 vars")}
	}

	lines := []string{
		fmt.Sprintf("bash idle | %d vars", len(vr.Variables)),
		"",
	}

	// Find column widths for nice formatting
	nw, tw := 4, 4
	for _, v := range vr.Variables {
		if len(v.Name) > nw {
			nw = len(v.Name)
		}
		if len(v.Type) > tw {
			tw = len(v.Type)
		}
	}

	for _, v := range vr.Variables {
		val := v.Value
		if len(val) > 60 {
			val = val[:57] + "..."
		}
		lines = append(lines, fmt.Sprintf("%-*s  %-*s  %s", nw, v.Name, tw, v.Type, val))
	}
	return kernel.LookResult{Text: strings.Join(lines, "\n")}
}

// Ctl controls the bash runtime.
func (b *Bash) Ctl(op string) kernel.CtlResult {
	switch op {
	case "reset":
		err := b.worker.Reset()
		if err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: %v", err)}
		}
		return kernel.CtlResult{Text: "RESET | namespace cleared | 0 vars"}
	case "cancel":
		b.worker.Interrupt()
		return kernel.CtlResult{Text: "CANCELLED"}
	case "restart":
		_ = b.worker.Shutdown()
		w, err := NewBashWorker(b.cwd, nil)
		if err != nil {
			return kernel.CtlResult{Text: fmt.Sprintf("ERROR: restart failed: %v", err)}
		}
		b.worker = w
		return kernel.CtlResult{Text: "RESTARTED | fresh bash session"}
	case "status":
		return kernel.CtlResult{Text: "bash idle"}
	default:
		return kernel.CtlResult{Text: fmt.Sprintf("ERROR: unknown op '%s'. Use reset, cancel, restart, or status.", op)}
	}
}

// Shutdown kills the bash process.
func (b *Bash) Shutdown() error {
	return b.worker.Shutdown()
}
