package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(installCmd)
}

var installCmd = &cobra.Command{
	Use:   "install <lang> [<lang>...]",
	Short: "Install a language runtime",
	Long: `Install one or more language runtimes.

Detects the language interpreter, sets up dependencies, configures
Claude Desktop and Cursor.

Examples:
  rat install py
  rat install py r ju
  rat install py --with-python`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("install not yet implemented for: %v", args)
	},
}
