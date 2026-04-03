package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/mcpclient"
	resolver "github.com/maximerivest/rat/internal/resolve"
	"github.com/maximerivest/rat/internal/state"
)

type ensureAction string

const (
	ensureNoop      ensureAction = "noop"
	ensureStarted   ensureAction = "started"
	ensureRestarted ensureAction = "restarted"
)

// resolveInput applies rat's unified resolution algorithm for the current cwd.
func resolveInput(input string) (*resolver.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cwd, _ = filepath.Abs(cwd)
	return resolver.Resolve(store(), input, cwd)
}

// ensureResolvedKernel makes sure the resolved kernel is running.
// It returns the running kernel plus whether this call started/restarted it.
func ensureResolvedKernel(r *resolver.Result) (*state.Kernel, ensureAction, error) {
	s := store()
	before, err := s.GetRunning(r.Name)
	if err != nil {
		return nil, ensureNoop, err
	}

	k, err := daemon.Start(s, daemon.StartOpts{
		Name: r.Name,
		Lang: r.Lang,
		Cwd:  r.Cwd,
		Venv: r.Venv,
	})
	if err != nil {
		return nil, ensureNoop, err
	}

	switch {
	case before == nil:
		return k, ensureStarted, nil
	case before.PID != k.PID || before.Port != k.Port:
		return k, ensureRestarted, nil
	default:
		return k, ensureNoop, nil
	}
}

// ensureKernel resolves input and makes sure the target kernel is running.
func ensureKernel(input string) (*state.Kernel, ensureAction, error) {
	r, err := resolveInput(input)
	if err != nil {
		return nil, ensureNoop, err
	}
	return ensureResolvedKernel(r)
}

// runningKernel resolves input and returns a running kernel only.
func runningKernel(input string) (*state.Kernel, error) {
	r, err := resolveInput(input)
	if err != nil {
		return nil, err
	}
	k, err := store().GetRunning(r.Name)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, fmt.Errorf("kernel %q is not running", r.Name)
	}
	return k, nil
}

// connectToKernel resolves a runtime, auto-starts it if needed, and returns
// an MCP session.
func connectToKernel(ctx context.Context, input string) (*mcpclient.Session, error) {
	k, action, err := ensureKernel(input)
	if err != nil {
		return nil, err
	}
	printKernelAction(k, action)
	return mcpclient.Connect(ctx, k.Port)
}

// connectToRunningKernel resolves a runtime and connects only if it is already running.
func connectToRunningKernel(ctx context.Context, input string) (*mcpclient.Session, error) {
	k, err := runningKernel(input)
	if err != nil {
		return nil, err
	}
	return mcpclient.Connect(ctx, k.Port)
}

func printKernelAction(k *state.Kernel, action ensureAction) {
	if k == nil || action == ensureNoop {
		return
	}
	verb := string(action)
	venvMsg := ""
	if k.Venv != "" {
		venvMsg = fmt.Sprintf(" venv=%s", shortPath(k.Venv))
	}
	fmt.Fprintf(os.Stderr, "%s %s on http://127.0.0.1:%d/mcp (PID %d)%s\n", k.Name, verb, k.Port, k.PID, venvMsg)
}

// extractText pulls the text content from an MCP tool result.
func extractText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	text := ""
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			if text != "" {
				text += "\n"
			}
			text += tc.Text
		}
	}
	return text
}
