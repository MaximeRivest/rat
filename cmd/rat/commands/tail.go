package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	tailN      int
	tailAsJSON bool
)

func init() {
	tailCmd.Flags().IntVarP(&tailN, "n", "n", 10, "Number of recent activity entries to show")
	tailCmd.Flags().BoolVar(&tailAsJSON, "json", false, "Print raw activity records as JSON")
	rootCmd.AddCommand(tailCmd)
}

var tailCmd = &cobra.Command{
	Use:     "tail <runtime>",
	Short:   "See recent activity",
	GroupID: "daily",
	Long: `Show recent activity on a kernel.

This is the history view complement to rat look. It shows what was run
recently, including code, output, success state, and client when available.
The kernel must already be running.

Examples:
  rat tail py
  rat tail py --n 20
  rat tail py --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToRunningKernel(ctx, args[0])
		if err != nil {
			return err
		}
		defer session.Close()

		format := "text"
		if tailAsJSON {
			format = "json"
		}

		result, err := session.Tail(ctx, tailN, format)
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
