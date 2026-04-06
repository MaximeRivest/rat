package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/generic"
	"github.com/maximerivest/rat/internal/repl"
)

func init() {
	rootCmd.AddCommand(pickCmd)
}

var pickCmd = &cobra.Command{
	Use:     "pick",
	Short:   "Switch between kernels",
	GroupID: "daily",
	Long: `Interactive picker for switching between kernels.

Shows all kernels in the current project. Navigate with arrow keys
or hjkl, press Enter to connect, Escape to exit.

Running kernels show in green. Stopped kernels are dim — selecting
one starts it. The + slot creates a new instance.

This is the same picker that appears when you Ctrl-D from a REPL.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		cwd, _ = filepath.Abs(cwd)

		// Resolve a base name for the current project.
		root, _ := findProjectRoot(cwd)
		projName := projectName(root)
		baseName := "any@" + projName

		for {
			items := repl.DiscoverPickerItems(baseName, cwd)
			if len(items) == 0 {
				fmt.Println("No kernels. Start one: rat py")
				return nil
			}

			fmt.Fprint(os.Stdout, "\033[2J\033[H")
			result := repl.ShowPicker(items, "", 0)
			if result.Quit {
				return nil
			}

			input := result.Name
			if input == "" {
				input = result.Lang
				if result.Instance >= 2 {
					input = fmt.Sprintf("%s.%d", result.Lang, result.Instance)
				}
			}

			k, action, err := ensureKernel(input)
			if err != nil {
				return err
			}
			printKernelAction(k, action)

			activityLog := activityLogPath(k.Name)
			var rtCfg *generic.RuntimeConfig
			var configDir string
			if k.Lang != "sh" && k.Lang != "py" {
				if cfgPath, err := findRuntimeConfig(k.Lang); err == nil {
					if cfg, err := generic.LoadConfig(cfgPath); err == nil {
						rtCfg = cfg
						configDir = filepath.Dir(cfgPath)
					}
				}
			}

			instance := result.Instance
			if instance == 0 {
				instance = 1
			}

			cfg := repl.Config{
				Name:          k.Name,
				Lang:          k.Lang,
				Port:          k.Port,
				Cwd:           k.Cwd,
				Venv:          k.Venv,
				ActivityLog:   activityLog,
				RuntimeConfig: rtCfg,
				ConfigDir:     configDir,
				Instance:      instance,
			}

			exitCode := repl.RunOnce(cfg)
			if exitCode == 2 {
				return nil
			}
			// Loop back to picker.
		}
	},
}
