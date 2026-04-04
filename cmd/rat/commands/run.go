package commands

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
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

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, name)
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Run(ctx, code)
		if err != nil {
			return err
		}

		text := extractText(result)
		if text != "" {
			fmt.Println(text)
		}

		if result.IsError {
			os.Exit(1)
		}
		return nil
	},
}
