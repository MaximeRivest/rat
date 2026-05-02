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
  rat look py --at df.columns # drill into attribute`,
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
