// Package mcpclient provides a thin wrapper around mcp-go's HTTP client
// for rat CLI commands that need to talk to running kernels.
//
// We use mcp-go (github.com/mark3labs/mcp-go/client) — the same library
// that powers rat's MCP server. As MCP evolves, mcp-go tracks it, and
// both our server and client stay in sync.
package mcpclient

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// DefaultTimeout for CLI operations.
const DefaultTimeout = 30 * time.Second

// Session is an initialized MCP connection to a kernel.
type Session struct {
	client *client.Client
	url    string
}

// Connect connects to a kernel's MCP HTTP endpoint, initializes the
// session, and returns a ready-to-use Session.
func Connect(ctx context.Context, port int) (*Session, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	c, err := client.NewStreamableHttpClient(url)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", url, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "rat",
		Version: "0.1.0",
	}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize MCP session on %s: %w", url, err)
	}

	return &Session{client: c, url: url}, nil
}

// Run executes code on the kernel.
func (s *Session) Run(ctx context.Context, code string) (*mcp.CallToolResult, error) {
	return s.callTool(ctx, "run", map[string]any{"code": code})
}

// Look inspects kernel state.
func (s *Session) Look(ctx context.Context, at string) (*mcp.CallToolResult, error) {
	args := map[string]any{}
	if at != "" {
		args["at"] = at
	}
	return s.callTool(ctx, "look", args)
}

// LookComplete requests completions for code at cursor position.
func (s *Session) LookComplete(ctx context.Context, code string, cursor int) (*mcp.CallToolResult, error) {
	return s.callTool(ctx, "look", map[string]any{
		"code":   code,
		"cursor": cursor,
	})
}

// SendInput writes text to the running command's stdin via MCP run(input=...).
func (s *Session) SendInput(ctx context.Context, text string) (*mcp.CallToolResult, error) {
	return s.callTool(ctx, "run", map[string]any{"input": text})
}

// IsWaitingForInput checks if the kernel process is waiting for stdin.
func (s *Session) IsWaitingForInput(ctx context.Context) bool {
	result, err := s.Ctl(ctx, "status")
	if err != nil {
		return false
	}
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			if tc.Text == "waiting_for_input" {
				return true
			}
		}
	}
	return false
}

// Ctl sends a control operation (reset, cancel, restart, status).
func (s *Session) Ctl(ctx context.Context, op string) (*mcp.CallToolResult, error) {
	return s.callTool(ctx, "ctl", map[string]any{"op": op})
}

// Close closes the session.
func (s *Session) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

func (s *Session) callTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	result, err := s.client.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", name, err)
	}
	return result, nil
}
