// Package repl implements the interactive REPL for rat.
//
// Uses chzyer/readline for line editing, history, and tab completion.
// Every command goes through MCP run(), sharing the namespace with
// Claude, Cursor, notebooks — whoever else is connected.
package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/mcpclient"
)

// Config for the REPL session.
type Config struct {
	Name string // kernel name for display
	Lang string // canonical language
	Port int    // kernel MCP port
	Cwd  string // kernel working directory
}

// Run starts an interactive REPL session.
func Run(cfg Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := mcpclient.Connect(ctx, cfg.Port)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.Name, err)
	}
	defer session.Close()

	// History file
	histDir := filepath.Join(configDir(), "history")
	os.MkdirAll(histDir, 0755)
	histFile := filepath.Join(histDir, cfg.Name+".history")

	// Tab completion via MCP look()
	completer := &mcpCompleter{session: session, ctx: ctx}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:            shellPrompt(cfg),
		HistoryFile:       histFile,
		HistoryLimit:      10000,
		AutoComplete:      completer,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
		// Disable Ctrl+Z suspension
		FuncFilterInputRune: func(r rune) (rune, bool) {
			if r == readline.CharCtrlZ {
				return r, false // swallow it
			}
			return r, true
		},
	})
	if err != nil {
		return fmt.Errorf("readline: %w", err)
	}
	defer rl.Close()

	// Print banner
	fmt.Fprintf(os.Stderr,
		"\033[1m%s\033[0m | :%d | %s\n",
		cfg.Name, cfg.Port, cfg.Cwd,
	)
	fmt.Fprintf(os.Stderr, "Ctrl+D to exit. Ctrl+C to cancel.\n\n")

	for {
		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt {
				// Ctrl+C on empty line — just show new prompt
				continue
			}
			if err == io.EOF {
				return nil // Ctrl+D
			}
			return fmt.Errorf("readline: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Execute via MCP
		execCtx, execCancel := context.WithTimeout(ctx, 10*time.Minute)
		result, execErr := session.Run(execCtx, line)
		execCancel()

		if execErr != nil {
			fmt.Fprintf(os.Stderr, "\033[31merror:\033[0m %v\n", execErr)
			continue
		}

		text := resultText(result)
		if text != "" {
			fmt.Println(text)
		}
	}
}

// mcpCompleter implements readline.AutoCompleter using MCP look().
type mcpCompleter struct {
	session *mcpclient.Session
	ctx     context.Context
}

func (c *mcpCompleter) Do(line []rune, pos int) ([][]rune, int) {
	code := string(line)
	if code == "" {
		return nil, 0
	}

	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Second)
	defer cancel()

	result, err := c.session.LookComplete(ctx, code, pos)
	if err != nil {
		return nil, 0
	}

	text := resultText(result)
	if text == "" || text == "No completions." {
		return nil, 0
	}

	// Parse completion lines: "label    kind"
	// Find the word being completed to calculate replacement length
	wordStart := pos
	for wordStart > 0 && !strings.ContainsRune(" \t\n|&;(){}[]<>'\"", rune(line[wordStart-1])) {
		wordStart--
	}
	prefix := string(line[wordStart:pos])

	var candidates [][]rune
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// First field is the completion label
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		label := fields[0]
		// Only include if it extends beyond what's typed
		if strings.HasPrefix(label, prefix) {
			suffix := label[len(prefix):]
			candidates = append(candidates, []rune(suffix))
		}
	}

	return candidates, len(prefix)
}

// resultText extracts all text content from an MCP tool result.
func resultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func shellPrompt(cfg Config) string {
	switch cfg.Lang {
	case "sh":
		return "\033[1;32m$\033[0m "
	case "py":
		return "\033[1;33m>>>\033[0m "
	case "r":
		return "\033[1;34m>\033[0m "
	case "ju":
		return "\033[1;35mjulia>\033[0m "
	case "js":
		return "\033[1;36m>\033[0m "
	default:
		return cfg.Name + "> "
	}
}

func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "rat")
}
