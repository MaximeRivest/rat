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
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/mcpclient"
	"github.com/maximerivest/rat/internal/state"
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

	store := state.DefaultStore()

	connectSessions := func(port int) (*mcpclient.Session, *mcpclient.Session, error) {
		session, err := mcpclient.Connect(ctx, port)
		if err != nil {
			return nil, nil, err
		}
		ctlSession, err := mcpclient.Connect(ctx, port)
		if err != nil {
			_ = session.Close()
			return nil, nil, err
		}
		return session, ctlSession, nil
	}

	session, ctlSession, err := connectSessions(cfg.Port)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.Name, err)
	}
	defer session.Close()
	defer ctlSession.Close()

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTSTP)
	defer signal.Stop(sigCh)

	// History file
	histDir := filepath.Join(configDir(), "history")
	_ = os.MkdirAll(histDir, 0755)
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
		FuncFilterInputRune: func(r rune) (rune, bool) {
			if r == readline.CharCtrlZ {
				return r, false
			}
			return r, true
		},
	})
	if err != nil {
		return fmt.Errorf("readline: %w", err)
	}
	defer rl.Close()

	fmt.Fprintf(os.Stderr, "\033[1m%s\033[0m | :%d | %s\n", cfg.Name, cfg.Port, cfg.Cwd)
	fmt.Fprintf(os.Stderr, "Ctrl+D to exit. Ctrl+C to cancel.\n\n")

	for {
		// If the kernel was restarted externally or by a hard restart, reconnect.
		if k, err := store.Get(cfg.Name); err == nil && k != nil && k.Port != cfg.Port {
			cfg.Port = k.Port
			_ = session.Close()
			_ = ctlSession.Close()
			session, ctlSession, err = connectSessions(cfg.Port)
			if err != nil {
				return fmt.Errorf("reconnect to %s: %w", cfg.Name, err)
			}
			completer.session = session
			rl.SetPrompt(shellPrompt(cfg))
		}

		// Drain any queued signals while idle.
		draining := true
		for draining {
			select {
			case sig := <-sigCh:
				if sig == syscall.SIGTSTP {
					fmt.Fprintln(os.Stderr, "^Z ignored — use Ctrl+D to exit rat sh")
				} else if sig == os.Interrupt {
					fmt.Fprintln(os.Stderr, "^C")
				}
			default:
				draining = false
			}
		}

		line, err := rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt {
				fmt.Fprintln(os.Stderr, "^C")
				continue
			}
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("readline: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		execCtx, execCancel := context.WithTimeout(ctx, 10*time.Minute)
		done := make(chan struct{})
		var hardRestarted atomic.Bool

		go func() {
			interrupts := 0
			for {
				select {
				case sig := <-sigCh:
					switch sig {
					case os.Interrupt:
						interrupts++
						switch interrupts {
						case 1:
							fmt.Fprintln(os.Stderr, "^C cancelling…")
							_, _ = ctlSession.Ctl(context.Background(), "cancel")
						default:
							fmt.Fprintln(os.Stderr, "^C hard restarting kernel…")
							if _, err := store.Get(cfg.Name); err == nil {
								_ = daemon.Stop(store, cfg.Name)
								if nk, err := daemon.Start(store, daemon.StartOpts{
									Name: cfg.Name,
									Lang: cfg.Lang,
									Cwd:  cfg.Cwd,
								}); err == nil {
									cfg.Port = nk.Port
									hardRestarted.Store(true)
								}
							}
							execCancel()
						}
					case syscall.SIGTSTP:
						fmt.Fprintln(os.Stderr, "^Z ignored — Ctrl+C cancels, Ctrl+C twice hard-restarts")
					}
				case <-done:
					return
				}
			}
		}()

		result, execErr := session.Run(execCtx, line)
		close(done)
		execCancel()

		if execErr != nil {
			if execCtx.Err() == context.Canceled {
				if hardRestarted.Load() {
					fmt.Fprintln(os.Stderr, "kernel restarted")
				} else {
					fmt.Fprintln(os.Stderr, "execution aborted")
				}
				continue
			}
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
		fields := strings.Fields(l)
		if len(fields) == 0 {
			continue
		}
		label := fields[0]
		if strings.HasPrefix(label, prefix) {
			suffix := label[len(prefix):]
			candidates = append(candidates, []rune(suffix))
		}
	}

	return candidates, len(prefix)
}

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
