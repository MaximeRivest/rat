package commands

import (
	"fmt"

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
  rat run py 'x = 42; print(x)'
  rat run r 'summary(mtcars)'
  rat run sh 'ls -la'`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		code := args[1]
		return fmt.Errorf("run not yet implemented: %s %q", name, code)
	},
}
