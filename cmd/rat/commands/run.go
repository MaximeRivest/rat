package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/mcpclient"
)

func init() {
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:     "run <runtime> '<code>'",
	Short:   "Execute code on a kernel",
	GroupID: "daily",
	Long: `Run code on a kernel.

Resolves the runtime name, auto-starts the kernel if needed, executes
the code, prints output, and exits.

The runtime can be a language (py, sh, r, jl, js) which resolves
to your current project's kernel, or a full name (py@myproject, py-ml).

Examples:
  rat run py 'x = 42'
  rat run py 'print(x)'
  rat run sh 'ls -la'
  rat run py@myproject 'df.head()'
  rat run py-ml 'import torch'`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		code := strings.Join(args[1:], " ")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		k, action, err := ensureKernel(name)
		if err != nil {
			return err
		}
		printKernelAction(k, action)

		// Track what was already streamed so we can dedup the final result.
		var printed strings.Builder

		session, err := mcpclient.Connect(ctx, k.Port, mcpclient.ConnectOpts{
			// Stream output as it arrives.
			OnNotification: func(n mcp.JSONRPCNotification) {
				if n.Method == "rat/output" {
					if text, ok := n.Params.AdditionalFields["text"].(string); ok && text != "" {
						fmt.Print(text)
						printed.WriteString(text)
					}
				}
			},
			// Handle input requests via MCP elicitation.
			Elicitation: &stdinElicitor{reader: bufio.NewReader(os.Stdin)},
		})
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Run(ctx, code)
		if err != nil {
			return err
		}

		text := mcpclient.ExtractText(result)
		text = trimAlreadyPrinted(text, printed.String())
		if text != "" {
			fmt.Println(text)
		}

		if result.IsError {
			os.Exit(1)
		}
		return nil
	},
}

// stdinElicitor implements client.ElicitationHandler by reading from stdin.
type stdinElicitor struct {
	reader *bufio.Reader
}

func (e *stdinElicitor) Elicit(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	line, err := e.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return &mcp.ElicitationResult{
			ElicitationResponse: mcp.ElicitationResponse{
				Action: mcp.ElicitationResponseActionCancel,
			},
		}, nil
	}
	if err == io.EOF && line == "" {
		return &mcp.ElicitationResult{
			ElicitationResponse: mcp.ElicitationResponse{
				Action: mcp.ElicitationResponseActionCancel,
			},
		}, nil
	}
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: map[string]any{"text": line},
		},
	}, nil
}

// trimAlreadyPrinted removes the already-streamed prefix from the final
// tool result text so output isn't printed twice.
func trimAlreadyPrinted(text, printed string) string {
	text = strings.TrimSpace(text)
	printed = strings.TrimSpace(printed)
	if printed == "" {
		return text
	}
	if strings.HasPrefix(text, printed) {
		return strings.TrimSpace(strings.TrimPrefix(text, printed))
	}
	return text
}
