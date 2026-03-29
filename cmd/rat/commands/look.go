package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var lookAt string

func init() {
	lookCmd.Flags().StringVar(&lookAt, "at", "", "Symbol to inspect in detail")
	rootCmd.AddCommand(lookCmd)
}

var lookCmd = &cobra.Command{
	Use:   "look <name>",
	Short: "Inspect variables and state on a kernel",
	Long: `Inspect the state of a running kernel — variables, types, values.

Examples:
  rat look py             # list all variables
  rat look py --at df     # inspect df in detail`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("look not yet implemented for %q", args[0])
	},
}
