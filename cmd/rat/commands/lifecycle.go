package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/daemon"
	"github.com/maximerivest/rat/internal/state"
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

// store returns the default state store. Defined once so all commands share it.
func store() *state.Store {
	return state.DefaultStore()
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
		kernels, err := store().List()
		if err != nil {
			return err
		}
		if len(kernels) == 0 {
			fmt.Println("No running kernels.")
			fmt.Println("Start one: rat start sh")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tLANG\tPORT\tSTATE\tCWD\tSTARTED")
		for _, k := range kernels {
			cwd := shortPath(k.Cwd)
			ago := timeAgo(k.Started)
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
				k.Name, k.Lang, k.Port, "running", cwd, ago)
		}
		w.Flush()
		return nil
	},
}

var startCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a kernel explicitly",
	Long: `Start a kernel in the background.

The name can be a language (sh, py, r) or a named runtime (py-ml).
Auto-assigns a port and records in ~/.config/rat/state.yaml.

Examples:
  rat start sh
  rat start py`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		lang, err := resolveLang(name)
		if err != nil {
			return err
		}

		cwd, _ := os.Getwd()
		cwd, _ = filepath.Abs(cwd)

		k, err := daemon.Start(store(), daemon.StartOpts{
			Name: name,
			Lang: lang,
			Cwd:  cwd,
		})
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "%s started on http://127.0.0.1:%d/mcp (PID %d)\n", k.Name, k.Port, k.PID)
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [<name>] [--all]",
	Short: "Stop a kernel",
	RunE: func(cmd *cobra.Command, args []string) error {
		s := store()

		if stopAll {
			n, err := daemon.StopAll(s)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Println("No kernels running.")
			} else {
				fmt.Fprintf(os.Stderr, "Stopped %d kernel(s).\n", n)
			}
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("specify a runtime name or use --all")
		}

		name := args[0]
		if err := daemon.Stop(s, name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s stopped.\n", name)
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart <name>",
	Short: "Restart a kernel (fresh namespace, same config)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		s := store()

		// Get current config before stopping
		existing, err := s.Get(name)
		if err != nil {
			return err
		}

		// Resolve lang — either from existing state or from the name
		lang := ""
		cwd := ""
		venv := ""
		if existing != nil {
			lang = existing.Lang
			cwd = existing.Cwd
			venv = existing.Venv

			// Stop the existing kernel
			if err := daemon.Stop(s, name); err != nil {
				return err
			}
		} else {
			// Not running — just start it
			lang, err = resolveLang(name)
			if err != nil {
				return err
			}
			cwd, _ = os.Getwd()
		}

		cwd, _ = filepath.Abs(cwd)

		k, err := daemon.Start(s, daemon.StartOpts{
			Name: name,
			Lang: lang,
			Cwd:  cwd,
			Venv: venv,
		})
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "%s restarted on http://127.0.0.1:%d/mcp (PID %d)\n", k.Name, k.Port, k.PID)
		return nil
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, args[0])
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Ctl(ctx, "reset")
		if err != nil {
			return err
		}
		fmt.Println(extractText(result))
		return nil
	},
}

var cancelCmd = &cobra.Command{
	Use:   "cancel <name>",
	Short: "Cancel running execution on a kernel",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		session, err := connectToKernel(ctx, args[0])
		if err != nil {
			return err
		}
		defer session.Close()

		result, err := session.Ctl(ctx, "cancel")
		if err != nil {
			return err
		}
		fmt.Println(extractText(result))
		return nil
	},
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics",
	RunE: func(cmd *cobra.Command, args []string) error {
		printShellDoctor(inspectShellEnv())
		return nil
	},
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update rat binary and kernel dependencies",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("update not yet implemented")
	},
}

// ── helpers ─────────────────────────────────────────────────

func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && len("~/"+rel) < len(p) {
		return "~/" + rel
	}
	return p
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
