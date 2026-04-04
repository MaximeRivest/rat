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
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// DefaultTimeout for CLI operations.
const DefaultTimeout = 30 * time.Second

// Status is parsed from ctl(status). The first line is always the runtime
// state (idle, busy, waiting_for_input). Additional key: value lines are
// appended by the MCP server and kernel.
type Status struct {
	State          string
	IdleSeconds    int
	MemoryMB       int
	PID            int
	Clients        int
	ClientNames    string
	LastCaller     string
	RuntimeVersion string
}

// Session is an initialized MCP connection to a kernel.
type Session struct {
	client *client.Client
	url    string
}

// ConnectOpts configures optional MCP client capabilities.
type ConnectOpts struct {
	// Elicitation handles server requests for user input (e.g. Python input()).
	// When set, the client declares elicitation capability.
	Elicitation client.ElicitationHandler

	// OnNotification is called for server notifications (e.g. rat/output streaming).
	OnNotification func(mcp.JSONRPCNotification)
}

// Connect connects to a kernel's MCP HTTP endpoint, initializes the
// session, and returns a ready-to-use Session.
func Connect(ctx context.Context, port int, opts ...ConnectOpts) (*Session, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	trans, err := transport.NewStreamableHTTP(url)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", url, err)
	}

	// Build client options from ConnectOpts.
	var clientOpts []client.ClientOption
	var opt ConnectOpts
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.Elicitation != nil {
		clientOpts = append(clientOpts, client.WithElicitationHandler(opt.Elicitation))
	}
	if sessionID := trans.GetSessionId(); sessionID != "" {
		clientOpts = append(clientOpts, client.WithSession())
	}

	c := client.NewClient(trans, clientOpts...)

	if opt.OnNotification != nil {
		c.OnNotification(opt.OnNotification)
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

// Status queries ctl(status) and parses the text response.
func (s *Session) Status(ctx context.Context) (Status, error) {
	result, err := s.Ctl(ctx, "status")
	if err != nil {
		return Status{}, err
	}
	return parseStatus(ExtractText(result)), nil
}

// IsWaitingForInput checks if the kernel process is waiting for stdin.
func (s *Session) IsWaitingForInput(ctx context.Context) bool {
	status, err := s.Status(ctx)
	if err != nil {
		return false
	}
	return status.State == "waiting_for_input"
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

// ExtractText pulls the text content from an MCP tool result.
func ExtractText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			if tc.Text != "" {
				parts = append(parts, tc.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func parseStatus(text string) Status {
	text = strings.TrimSpace(text)
	if text == "" {
		return Status{}
	}

	lines := strings.Split(text, "\n")
	status := Status{State: strings.TrimSpace(lines[0])}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "idle_seconds":
			status.IdleSeconds, _ = strconv.Atoi(value)
		case "memory_mb":
			status.MemoryMB, _ = strconv.Atoi(value)
		case "pid":
			status.PID, _ = strconv.Atoi(value)
		case "clients":
			status.Clients, _ = strconv.Atoi(value)
		case "client_names":
			status.ClientNames = value
		case "last_caller":
			status.LastCaller = value
		case "runtime_version":
			status.RuntimeVersion = value
		}
	}
	return status
}
