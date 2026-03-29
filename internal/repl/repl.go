// Package repl implements the interactive REPL for rat.
//
// For bash, this is a readline-style loop that sends each line to the
// shared kernel via MCP. The user gets a prompt, line editing (from
// the terminal), and history (from readline/libedit via stdin).
//
// Every command goes through MCP run(), which means it shares the
// namespace with Claude, Cursor, notebooks — whoever else is connected.
// Interactive TUI programs (vim, top) won't work through this REPL —
// use a real terminal for those. Everything else works.
package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/sys/unix"

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

	// Handle Ctrl+C — send cancel to kernel, don't exit REPL
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	// Print banner
	fmt.Fprintf(os.Stderr,
		"\033[1m%s\033[0m | :%d | %s\n",
		cfg.Name, cfg.Port, cfg.Cwd,
	)
	fmt.Fprintf(os.Stderr, "Ctrl+D to exit. Ctrl+C to cancel.\n\n")

	prompt := shellPrompt(cfg)
	reader := bufio.NewReader(os.Stdin)
	interactive := isTerminal(os.Stdin)

	for {
		if interactive {
			fmt.Fprint(os.Stdout, prompt)
		}

		line, err := readLine(reader, sigCh)
		if err != nil {
			if err == io.EOF {
				if interactive {
					fmt.Fprintln(os.Stdout) // newline after ^D
				}
				return nil
			}
			if err == errInterrupted {
				fmt.Fprintln(os.Stdout) // newline after ^C
				continue
			}
			return fmt.Errorf("read: %w", err)
		}

		if strings.TrimSpace(line) == "" {
			continue
		}

		// Execute via MCP with a generous timeout
		execCtx, execCancel := context.WithTimeout(ctx, 10*time.Minute)

		// Forward Ctrl+C as cancel during execution
		go func() {
			select {
			case <-sigCh:
				session.Ctl(ctx, "cancel")
			case <-execCtx.Done():
			}
		}()

		result, execErr := session.Run(execCtx, line)
		execCancel()

		if execErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", execErr)
			continue
		}

		text := resultText(result)
		if text != "" {
			fmt.Println(text)
		}
	}
}

var errInterrupted = fmt.Errorf("interrupted")

// readLine reads a line, returning errInterrupted if Ctrl+C is pressed.
func readLine(reader *bufio.Reader, sigCh <-chan os.Signal) (string, error) {
	// We read in a goroutine so we can also listen for signals
	type readResult struct {
		line string
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- readResult{strings.TrimRight(line, "\r\n"), err}
	}()

	select {
	case r := <-ch:
		return r.line, r.err
	case <-sigCh:
		return "", errInterrupted
	}
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

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}
