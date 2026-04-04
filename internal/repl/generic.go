package repl

// Generic MCP-connected REPL for any runtime.
//
// This is Pattern C from KERNEL-PROTOCOL.md: a thin wrapper REPL that
// routes all execution through the kernel's MCP endpoint. The kernel
// owns the namespace — what you type here, Claude sees. What Claude
// runs, you see here.
//
// Works for R, pi, Julia, or any generic runtime.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximerivest/rat/internal/mcpclient"
)

// runGenericRepl connects to the kernel MCP server and runs an interactive
// loop: read input → send to kernel → print output.
func runGenericRepl(cfg Config) error {
	ctx := context.Background()

	// Connect to the kernel MCP endpoint.
	session, err := mcpclient.Connect(ctx, cfg.Port)
	if err != nil {
		return fmt.Errorf("connect to %s kernel: %w\n\nIs the kernel running? Try: rat start %s", cfg.Lang, err, cfg.Name)
	}
	defer session.Close()

	// Activity watcher for seeing other clients' work.
	watcher := newActivityWatcher(cfg.ActivityLog)
	watcher.seekToEnd()

	// Background goroutine to display activity from other clients.
	stopActivity := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopActivity:
				return
			case <-ticker.C:
				entries := watcher.check()
				for _, e := range entries {
					if e.Event != "" {
						// Kernel-pushed event (message, alert, progress, etc.)
						formatEvent(e)
					} else {
						// Execution record from another client.
						mark := "✓"
						if !e.OK {
							mark = "✗"
						}
						fmt.Printf("\033[2m── rat: exec #%d (another client) %s ──\033[0m\n", e.N, mark)
						for i, line := range strings.Split(e.Code, "\n") {
							if i >= 5 {
								fmt.Printf("\033[2m  ...\033[0m\n")
								break
							}
							fmt.Printf("\033[2m>>> %s\033[0m\n", line)
						}
						if e.Output != "" {
							for i, line := range strings.Split(e.Output, "\n") {
								if i >= 5 {
									fmt.Printf("\033[2m  ...\033[0m\n")
									break
								}
								fmt.Printf("\033[2m%s\033[0m\n", line)
							}
						}
						fmt.Printf("\033[2m%s\033[0m\n", strings.Repeat("─", 42))
					}
				}
			}
		}
	}()
	defer func() {
		close(stopActivity)
		wg.Wait()
	}()

	// Header.
	serverURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Port)
	fmt.Printf("rat %s | %s @ %s\n", cfg.Name, cfg.Lang, serverURL)
	fmt.Println("Shared namespace — other clients see your state.")
	fmt.Println()

	// Prompt — use config if available.
	prompt := cfg.Lang + "> "
	if cfg.RuntimeConfig != nil && cfg.RuntimeConfig.Frontend.Prompt != "" {
		prompt = cfg.RuntimeConfig.Frontend.Prompt
	} else if cfg.RuntimeConfig != nil && cfg.RuntimeConfig.Frontend.Fallback != nil && cfg.RuntimeConfig.Frontend.Fallback.Prompt != "" {
		prompt = cfg.RuntimeConfig.Frontend.Fallback.Prompt
	}

	// Handle Ctrl-C gracefully — don't exit, just cancel current input.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		for range sigCh {
			// Cancel running execution if any.
			cCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			session.Ctl(cCtx, "cancel")
			cancel()
			fmt.Println("\nKeyboardInterrupt")
			fmt.Print(prompt)
		}
	}()
	defer signal.Stop(sigCh)

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print(prompt)
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Exit commands.
		switch strings.TrimSpace(line) {
		case "exit", "exit()", "quit", "quit()", "q()", ":q":
			return nil
		}

		// Mark this as our own execution (so activity watcher skips it).
		watcher.markOwn(strings.TrimSpace(line))

		// Execute on the shared kernel via MCP.
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		result, err := session.Run(runCtx, line)
		cancel()

		if err != nil {
			fmt.Printf("[rat] error: %v\n", err)
			fmt.Println("[rat] kernel may have stopped. Ctrl-D to exit, then 'rat " + cfg.Lang + "' to reconnect.")
			continue
		}

		// Extract and print output.
		text := extractResultText(result)
		// Strip trailing timing hint (✓ 3ms).
		text = stripHint(text)
		if text != "" {
			fmt.Println(text)
		}
	}

	return nil
}

func extractResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok && tc.Text != "" {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// formatEvent displays a kernel-pushed event in the REPL.
func formatEvent(e activityEntry) {
	dim := "\033[2m"
	reset := "\033[0m"
	bold := "\033[1m"

	switch e.Event {
	case "message":
		from, _ := e.Data["from"].(string)
		text, _ := e.Data["text"].(string)
		channel, _ := e.Data["channel"].(string)
		header := from
		if channel != "" {
			header += " (" + channel + ")"
		}
		fmt.Printf("%s%s:%s %s\n", bold, header, reset, text)

	case "progress":
		msg, _ := e.Data["msg"].(string)
		pct, _ := e.Data["pct"].(float64)
		if pct > 0 {
			fmt.Printf("%s⏳ %.0f%% %s%s\n", dim, pct, msg, reset)
		} else if msg != "" {
			fmt.Printf("%s⏳ %s%s\n", dim, msg, reset)
		}

	case "alert":
		msg, _ := e.Data["msg"].(string)
		level, _ := e.Data["level"].(string)
		icon := "🔔"
		if level == "error" {
			icon = "🔴"
		} else if level == "warning" {
			icon = "⚠️"
		}
		fmt.Printf("%s %s\n", icon, msg)

	case "error":
		msg, _ := e.Data["msg"].(string)
		fmt.Printf("\033[31m✗ %s\033[0m\n", msg)

	default:
		// Generic event display.
		text, _ := e.Data["text"].(string)
		msg, _ := e.Data["msg"].(string)
		display := text
		if display == "" {
			display = msg
		}
		if display == "" {
			// Show the whole data as JSON.
			if b, err := json.Marshal(e.Data); err == nil {
				display = string(b)
			}
		}
		fmt.Printf("%s[%s] %s%s\n", dim, e.Event, display, reset)
	}
}

func stripHint(s string) string {
	// Remove trailing ✓ 3ms or ✗ 3ms lines.
	lines := strings.Split(s, "\n")
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "" || strings.HasPrefix(last, "✓ ") || strings.HasPrefix(last, "✗ ") {
			lines = lines[:len(lines)-1]
		} else {
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ── Activity watcher ────────────────────────────────────────

// activityEntry is an execution record from another client.
type activityEntry struct {
	N      int    `json:"n"`
	Code   string `json:"code"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
	Time   int64  `json:"t"`

	// Event fields (mutually exclusive with N/Code/Output).
	Event string                 `json:"event,omitempty"`
	Data  map[string]interface{} `json:"data,omitempty"`
}

type activityWatcher struct {
	path    string
	pos     int64
	mu      sync.Mutex
	ownCode []string
}

func newActivityWatcher(path string) *activityWatcher {
	return &activityWatcher{path: path}
}

func (w *activityWatcher) markOwn(code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ownCode = append(w.ownCode, code)
	if len(w.ownCode) > 20 {
		w.ownCode = w.ownCode[len(w.ownCode)-20:]
	}
}

func (w *activityWatcher) seekToEnd() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.path == "" {
		return
	}
	f, err := os.Open(w.path)
	if err != nil {
		return
	}
	defer f.Close()
	pos, _ := f.Seek(0, 2)
	w.pos = pos
}

func (w *activityWatcher) check() []activityEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.path == "" {
		return nil
	}
	f, err := os.Open(w.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	f.Seek(w.pos, 0)
	scanner := bufio.NewScanner(f)
	var others []activityEntry
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e activityEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		code := strings.TrimSpace(e.Code)
		isOwn := false
		for i, own := range w.ownCode {
			if own == code {
				w.ownCode = append(w.ownCode[:i], w.ownCode[i+1:]...)
				isOwn = true
				break
			}
		}
		if !isOwn {
			others = append(others, e)
		}
	}
	newPos, _ := f.Seek(0, 1)
	w.pos = newPos
	return others
}
