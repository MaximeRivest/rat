package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	lookAt     string
	lookCode   string
	lookCursor int
)

func init() {
	lookCmd.Flags().StringVar(&lookAt, "at", "", "Symbol to inspect in detail")
	lookCmd.Flags().StringVar(&lookCode, "code", "", "Code buffer to complete")
	lookCmd.Flags().IntVar(&lookCursor, "cursor", -1, "Cursor position in --code (default: end of code)")
	rootCmd.AddCommand(lookCmd)
}

var lookCmd = &cobra.Command{
	Use:   "look <name>",
	Short: "Inspect variables and state on a kernel",
	Long: `Inspect the state of a running kernel — variables, types, values, or completions.

Examples:
  rat look sh                         # list all variables
  rat look py --at df                 # inspect df in detail
  rat look sh --code 'ls Pr'          # get completions at end of code
  rat look sh --code 'echo $PA' --cursor 8`,
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

		if lookAt != "" && lookCode != "" {
			return fmt.Errorf("use either --at or --code, not both")
		}

		var text string
		if lookCode != "" {
			cursor := lookCursor
			if cursor < 0 {
				cursor = len(lookCode)
			}
			result, err := session.LookComplete(ctx, lookCode, cursor)
			if err != nil {
				return err
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
