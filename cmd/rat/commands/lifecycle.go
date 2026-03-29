package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var stopAll bool

func init() {
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all kernels")

	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(addCmd)
	rootCmd.AddCommand(rmCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(cancelCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(updateCmd)
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive setup wizard",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("setup not yet implemented")
	},
}

var addCmd = &cobra.Command{
	Use:   "add <name> [--venv PATH] [--cwd PATH]",
	Short: "Register a named runtime",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("add not yet implemented for %q", args[0])
	},
}

var rmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Unregister a named runtime",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("rm not yet implemented for %q", args[0])
	},
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all runtimes and their state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("ls not yet implemented")
	},
}

var startCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a kernel explicitly",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("start not yet implemented for %q", args[0])
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [<name>] [--all]",
	Short: "Stop a kernel",
	RunE: func(cmd *cobra.Command, args []string) error {
		if stopAll {
			return fmt.Errorf("stop --all not yet implemented")
		}
		if len(args) == 0 {
			return fmt.Errorf("specify a runtime name or use --all")
		}
		return fmt.Errorf("stop not yet implemented for %q", args[0])
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a kernel (fresh namespace, same config)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("restart not yet implemented for %q", args[0])
	},
}

var resetCmd = &cobra.Command{
	Use:   "reset <name>",
	Short: "Clear the kernel namespace",
	Long: `Clear all variables in a kernel's namespace without restarting.

For full kernel restart, use: rat restart <name>
For direct MCP access:       mcp2cli rat-<name> ctl --op reset`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("reset not yet implemented for %q", args[0])
	},
}

var cancelCmd = &cobra.Command{
	Use:   "cancel <name>",
	Short: "Cancel running execution on a kernel",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("cancel not yet implemented for %q", args[0])
	},
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("doctor not yet implemented")
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update rat binary and kernel dependencies",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("update not yet implemented")
	},
}
