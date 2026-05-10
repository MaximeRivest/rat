package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

var (
	resolveAsJSON     bool
	resolveIncludeEnv bool
)

func init() {
	resolveCmd.Flags().BoolVar(&resolveAsJSON, "json", false, "Print the resolved runtime as JSON for integrations")
	resolveCmd.Flags().BoolVar(&resolveIncludeEnv, "include-env", false, "Include saved environment variables in JSON output")
	rootCmd.AddCommand(resolveCmd)
}

var resolveCmd = &cobra.Command{
	Use:     "resolve <runtime>",
	Short:   "Resolve a runtime without starting it",
	GroupID: "setup",
	Long: `Resolve a runtime name using rat's normal project-aware algorithm.

This is intended for integrations that need the same cwd, venv, runtime
binary, options, and environment rat would use when starting a kernel, without
actually starting that kernel.

Environment variables are omitted from JSON by default because they can contain
secrets. Use --include-env when the caller needs to launch a compatible process.

Examples:
  rat resolve py
  rat resolve py --json
  rat resolve py-ml --json --include-env`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		r, err := resolveInput(args[0])
		if err != nil {
			return err
		}

		if resolveAsJSON {
			out := resolvedRuntimeOutput{
				Name:        r.Name,
				Lang:        r.Lang,
				Cwd:         r.Cwd,
				Venv:        r.Venv,
				RuntimePath: r.RuntimePath,
				Options:     r.Options,
				IsNew:       r.IsNew,
			}
			if resolveIncludeEnv {
				out.Env = r.Env
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("name: %s\n", r.Name)
		fmt.Printf("lang: %s\n", r.Lang)
		fmt.Printf("cwd: %s\n", r.Cwd)
		if r.Venv != "" {
			fmt.Printf("venv: %s\n", r.Venv)
		}
		if r.RuntimePath != "" {
			fmt.Printf("runtime: %s\n", r.RuntimePath)
		}
		if len(r.Options) > 0 {
			fmt.Println("options:")
			for _, key := range sortedKeys(r.Options) {
				fmt.Printf("  %s: %s\n", key, displayOptionValue(key, r.Options[key]))
			}
		}
		if len(r.Env) > 0 {
			fmt.Println("env:")
			for _, key := range sortedKeys(r.Env) {
				fmt.Printf("  %s: %s\n", key, displayEnvValue(key, r.Env[key]))
			}
		}
		if r.IsNew {
			fmt.Println("new: true")
		}
		return nil
	},
}

type resolvedRuntimeOutput struct {
	Name        string            `json:"name"`
	Lang        string            `json:"lang"`
	Cwd         string            `json:"cwd"`
	Venv        string            `json:"venv,omitempty"`
	RuntimePath string            `json:"runtime_path,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	IsNew       bool              `json:"is_new"`
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
