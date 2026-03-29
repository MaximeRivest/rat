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
	Use:   "run <name> '<code>'",
	Short: "Run code on a kernel",
	Long: `Run code on a named kernel. Auto-starts the kernel if needed.

Examples:
  rat run sh 'ls -la'
  rat run py 'x = 42; print(x)'
  rat run r 'summary(mtcars)'`,
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
