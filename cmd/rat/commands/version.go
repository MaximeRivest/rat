package commands

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	s "github.com/maximerivest/rat/internal/termstyle"
)

func init() {
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:     "version",
	Short:   "Version info",
	GroupID: "setup",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("%s  %s/%s\n", s.Bold("rat "+Version), runtime.GOOS, runtime.GOARCH)
		fmt.Println()
		seen := map[string]bool{}
		runtimes := []struct{ label, binary, flag string }{
			{"Python", "python3", "--version"},
			{"Python", "python", "--version"},
			{"Bash", "bash", "--version"},
			{"R", "R", "--version"},
			{"Julia", "julia", "--version"},
			{"Node.js", "node", "--version"},
		}
		for _, rt := range runtimes {
			if seen[rt.label] {
				continue
			}
			if printRuntimeVersion(rt.label, rt.binary, rt.flag) {
				seen[rt.label] = true
			}
		}
	},
}

func printRuntimeVersion(label, binary, flag string) bool {
	path, err := exec.LookPath(binary)
	if err != nil {
		return false
	}
	out, err := exec.Command(path, flag).Output()
	if err != nil {
		return false
	}
	// Take first line, clean it up
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	// Some commands prefix with the name (e.g. "Python 3.12.1", "GNU bash, version 5.2.15...")
	// Keep it short
	version := extractVersion(label, line)
	fmt.Printf("  %-10s %s  %s\n", s.Dim(label), version, s.Dim(path))
	return true
}

func extractVersion(label, line string) string {
	line = strings.TrimSpace(line)
	// "Python 3.12.1" → "3.12.1"
	// "GNU bash, version 5.2.15(1)-release ..." → "5.2.15"
	// "julia version 1.11.0" → "1.11.0"
	// "v22.16.0" → "22.16.0"

	// Try to find a version-like pattern
	for _, word := range strings.Fields(line) {
		word = strings.TrimPrefix(word, "v")
		if len(word) > 0 && word[0] >= '0' && word[0] <= '9' {
			// Trim trailing non-version chars like "(1)-release"
			for i, c := range word {
				if c != '.' && (c < '0' || c > '9') {
					return word[:i]
				}
			}
			return word
		}
	}
	return line
}
