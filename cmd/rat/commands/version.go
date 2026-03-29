package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print rat version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("rat %s\n", Version)
	},
}
