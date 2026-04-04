package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/daemon"
)

func init() {
	rootCmd.AddCommand(installCmd)
}

var installCmd = &cobra.Command{
	Use:     "install <lang> [<lang>...]",
	Short:   "Project setup (runtime + deps)",
	GroupID: "setup",
	Long: `Set up one or more language runtimes for this project.

This is the explicit setup path. It resolves the project runtime,
installs or checks the needed dependencies, starts the kernel, and
runs a quick smoke test.

Currently implemented:
  py  Detect or create a project venv, install IPython + jedi, start the kernel
  sh  Check shell prerequisites, start the shared shell kernel

Examples:
  rat install py
  rat install sh
  rat install py sh`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		seen := map[string]bool{}
		for _, arg := range args {
			lang, err := resolveLang(arg)
			if err != nil {
				return err
			}
			if seen[lang] {
				continue
			}
			seen[lang] = true

			switch lang {
			case "sh":
				if err := installShellRuntime(); err != nil {
					return err
				}
			case "py":
				if err := installPythonRuntime(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("install for %s is not implemented yet", arg)
			}
		}
		return nil
	},
}

func installShellRuntime() error {
	check := inspectShellEnv()
	fmt.Println("rat install sh")
	fmt.Printf("OS: %s/%s\n", check.GOOS, check.GOARCH)

	if !check.Supported {
		fmt.Printf("\nShell shared-session support is not available here.\n%s\n", check.SupportNote)
		return fmt.Errorf("shell install blocked on this platform")
	}

	missing := shellMissingDeps(check)
	if len(missing) > 0 {
		fmt.Println("")
		statusLine("bash", check.BashPath != "", valueOrNote(check.BashPath, "not found"))
		statusLine("tmux", check.TmuxPath != "", valueOrNote(check.TmuxPath, "not found"))
		statusLine("stty", check.SttyPath != "", valueOrNote(check.SttyPath, "not found"))
		statusLine("config dir", check.ConfigWritable, check.StateDir)
		statusLine("cache dir", check.CacheWritable, check.CacheDir)
		if hint := shellInstallHint(check); hint != "" {
			fmt.Printf("\nInstall the missing prerequisites:\n  %s\n", hint)
		}
		return fmt.Errorf("shell install incomplete: missing prerequisites")
	}

	r, err := resolveInput("sh")
	if err != nil {
		return err
	}

	k, err := daemon.Start(store(), daemon.StartOpts{Name: r.Name, Lang: r.Lang, Cwd: r.Cwd, Venv: r.Venv})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := connectToKernel(ctx, r.Name)
	if err != nil {
		return err
	}
	defer session.Close()

	result, err := session.Run(ctx, "echo rat sh ready")
	if err != nil {
		return err
	}
	text := extractText(result)

	fmt.Println("")
	statusLine("bash", true, check.BashPath)
	statusLine("tmux", true, check.TmuxPath)
	statusLine("stty", true, check.SttyPath)
	statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
	fmt.Println("")
	if text != "" {
		fmt.Println(text)
		fmt.Println("")
	}
	fmt.Println("Ready.")
	fmt.Println("Try:")
	fmt.Println("  rat sh")
	fmt.Println("  rat run sh 'echo hello'")
	fmt.Println("  rat look sh --code 'ls Pr'")
	return nil
}
