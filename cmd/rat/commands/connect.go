package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/mcpclient"
	"github.com/maximerivest/rat/internal/state"
)

// connectToKernel finds a running kernel by name, auto-starts it if
// needed, and returns an MCP session. This is the shared entry point
// for run, look, reset, cancel — any command that talks to a kernel.
func connectToKernel(ctx context.Context, name string) (*mcpclient.Session, error) {
	s := store()

	// Check if already running
	k, err := s.Get(name)
	if err != nil {
		return nil, err
	}

	// Not running — auto-start
	if k == nil {
		k, err = autoStart(s, name)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "%s started on http://127.0.0.1:%d/mcp (PID %d)\n", k.Name, k.Port, k.PID)
	}

	return mcpclient.Connect(ctx, k.Port)
}

// autoStart starts a kernel for the given name. It first checks for
// a saved runtime config (from `rat add`), then falls back to
// resolving the name as a language alias.
func autoStart(s *state.Store, name string) (*state.Kernel, error) {
	// Check for a saved named runtime first.
	rt, _ := s.GetRuntime(name)
	if rt != nil {
		cwd := rt.Cwd
		if cwd == "" {
			cwd, _ = os.Getwd()
			cwd, _ = filepath.Abs(cwd)
		}
		return daemon.Start(s, daemon.StartOpts{
			Name: rt.Name,
			Lang: rt.Lang,
			Cwd:  cwd,
			Venv: rt.Venv,
		})
	}

	// Fall back to language alias.
	lang, err := resolveLang(name)
	if err != nil {
		return nil, fmt.Errorf("unknown runtime %q — use 'rat add %s' to register it, or use a language name (py, sh, r, ju, js)", name, name)
	}

	cwd, _ := os.Getwd()
	cwd, _ = filepath.Abs(cwd)

	// Auto-detect venv for Python.
	venv := ""
	if lang == "py" {
		venv = findVenv(cwd)
	}

	return daemon.Start(s, daemon.StartOpts{
		Name: name,
		Lang: lang,
		Cwd:  cwd,
		Venv: venv,
	})
}

// extractText pulls the text content from an MCP tool result.
func extractText(result *mcp.CallToolResult) string {
	// mcp.CallToolResult is from mcp-go — content is a slice of
	// content blocks. We concatenate all text blocks.
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
