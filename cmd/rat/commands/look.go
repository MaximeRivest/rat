package commands

import (
	"context"
	"fmt"
	"time"

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
  rat look sh             # list all variables
  rat look py --at df     # inspect df in detail`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, name)
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Look(ctx, lookAt)
		if err != nil {
			return err
		}

		text := extractText(result)
		if text != "" {
			fmt.Println(text)
		}
		return nil
	},
}
