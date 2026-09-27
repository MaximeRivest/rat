package commands

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/maximerivest/rat/internal/lang"
	"github.com/maximerivest/rat/internal/runtimes"
)

var (
	guideSystem string
	guideJSON   bool
)

func init() {
	guideCmd.Flags().StringVar(&guideSystem, "system", "", "Write the guide for another system: macOS, Windows, Linux or NixOS")
	guideCmd.Flags().BoolVar(&guideJSON, "json", false, `Print {"lang", "system", "guide"} as JSON`)
	rootCmd.AddCommand(guideCmd)
}

var guideCmd = &cobra.Command{
	Use:     "guide <language>",
	Short:   "How to install a language on this computer",
	GroupID: "setup",
	Long: `Print how to install a language's runtime on this computer — Python (uv),
R or Julia — for a person to follow or to hand to their AI as it is.

rat installs a notebook's packages itself (rat ensure); a guide covers only
the one-time install of the language. With --doc, its commands name that
notebook.

Examples:
  rat guide r
  rat guide julia --doc analysis.md
  rat guide py --system Windows`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := lang.Resolve(args[0])
		if err != nil {
			name = args[0]
		}
		text, err := runtimes.Guide(name, guideSystem, docFlag)
		if err != nil {
			return err
		}
		if guideJSON {
			system := guideSystem
			if system == "" {
				system = runtimes.System()
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"lang": name, "system": system, "guide": text})
		}
		fmt.Print(text)
		return nil
	},
}
