package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/spf13/cobra"
)

var (
	lookAt     string
	lookCode   string
	lookCursor int
	lookJSON   bool
)

func init() {
	lookCmd.Flags().StringVar(&lookAt, "at", "", "Symbol to inspect in detail")
	lookCmd.Flags().StringVar(&lookCode, "code", "", "Code buffer to complete")
	lookCmd.Flags().IntVar(&lookCursor, "cursor", -1, "Cursor position in --code, in characters (default: end of code)")
	lookCmd.Flags().BoolVar(&lookJSON, "json", false, "With --code: print {start, matches} as JSON (start is null when the kernel does not say which text the matches replace)")
	rootCmd.AddCommand(lookCmd)
}

var lookCmd = &cobra.Command{
	Use:     "look <runtime>",
	Short:   "See what's inside",
	GroupID: "daily",
	Long: `Inspect a kernel's namespace.

Without --at, shows a variable overview. With --at, inspects a
specific symbol in detail. Auto-starts the kernel if needed.

The runtime can be a language (py, sh, r, jl, js) which resolves
to your current project's kernel, or a full name (py@myproject, py-ml).

Examples:
  rat look py                 # variable overview
  rat look py --at df         # inspect df in detail
  rat look py --at df.columns # drill into attribute
  rat look r --code 'df$co' --json   # completions, for programs`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if lookAt != "" && lookCode != "" {
			return fmt.Errorf("use either --at or --code, not both")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, name)
		if err != nil {
			return err
		}
		defer session.Close()

		var text string
		if lookCode != "" {
			cursor := lookCursor
			if cursor < 0 {
				cursor = utf8.RuneCountInString(lookCode)
			}
			result, err := session.LookComplete(ctx, lookCode, cursor)
			if err != nil {
				return err
			}
			if lookJSON {
				return json.NewEncoder(os.Stdout).Encode(completionJSON(result))
			}
			text = extractText(result)
		} else {
			result, err := session.Look(ctx, lookAt)
			if err != nil {
				return err
			}
			text = extractText(result)
		}

		if text != "" {
			fmt.Println(text)
		}
		return nil
	},
}

// completionJSON is the answer of `rat look --code --json`: the kernel's
// exact completion when it gave one, otherwise its text lines read as
// "label  kind" with start null (the client decides what they replace).
func completionJSON(result *mcp.CallToolResult) map[string]any {
	if result != nil && result.StructuredContent != nil {
		if raw, err := json.Marshal(result.StructuredContent); err == nil {
			var c struct {
				Start   *int             `json:"start"`
				Matches []map[string]any `json:"matches"`
			}
			if json.Unmarshal(raw, &c) == nil && c.Start != nil {
				if c.Matches == nil {
					c.Matches = []map[string]any{}
				}
				return map[string]any{"start": *c.Start, "matches": c.Matches}
			}
		}
	}
	matches := []map[string]any{}
	for _, line := range strings.Split(extractText(result), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || line == "No completions." || strings.HasPrefix(line, "ERROR:") {
			continue
		}
		m := map[string]any{"label": fields[0]}
		if len(fields) > 1 {
			m["kind"] = fields[len(fields)-1]
		}
		matches = append(matches, m)
	}
	return map[string]any{"start": nil, "matches": matches}
}
