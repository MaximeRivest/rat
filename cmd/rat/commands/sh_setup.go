package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/state"
)

type shellEnvCheck struct {
	GOOS        string
	GOARCH      string
	Supported   bool
	SupportNote string

	BashPath       string
	PowerShellPath string
	TmuxPath       string
	SttyPath       string

	PackageManager string
	StateDir       string
	CacheDir       string
	ConfigWritable bool
	CacheWritable  bool
}

func inspectShellEnv() shellEnvCheck {
	check := shellEnvCheck{
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}

	switch runtime.GOOS {
	case "linux", "darwin":
		check.Supported = true
	case "windows":
		check.Supported = true
		check.SupportNote = "Using PowerShell as the shared shell runtime."
	default:
		check.SupportNote = fmt.Sprintf("Shell sharing is not implemented on %s yet.", runtime.GOOS)
	}

	check.BashPath, _ = exec.LookPath("bash")
	for _, candidate := range []string{"pwsh", "powershell"} {
		if path, err := exec.LookPath(candidate); err == nil {
			check.PowerShellPath = path
			break
		}
	}
	check.TmuxPath, _ = exec.LookPath("tmux")
	check.SttyPath, _ = exec.LookPath("stty")
	check.PackageManager = detectPackageManager()

	check.StateDir = filepath.Dir(state.DefaultPath())
	check.CacheDir = defaultCacheDir()
	check.ConfigWritable = dirWritable(check.StateDir)
	check.CacheWritable = dirWritable(check.CacheDir)

	return check
}

func detectPackageManager() string {
	for _, candidate := range []string{"brew", "apt-get", "dnf", "yum", "pacman", "zypper", "apk", "winget", "choco"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func dirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".rat-write-test-")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func defaultCacheDir() string {
	dir, err := cachedir.Rat()
	if err != nil {
		return filepath.Join(os.Getenv("HOME"), ".cache", "rat")
	}
	return dir
}

func shellMissingDeps(check shellEnvCheck) []string {
	var missing []string
	if runtime.GOOS == "windows" {
		if check.PowerShellPath == "" {
			missing = append(missing, "powershell")
		}
	} else {
		if check.BashPath == "" {
			missing = append(missing, "bash")
		}
		if check.TmuxPath == "" {
			missing = append(missing, "tmux")
		}
		if check.SttyPath == "" {
			missing = append(missing, "stty")
		}
	}
	if !check.ConfigWritable {
		missing = append(missing, "config-dir")
	}
	if !check.CacheWritable {
		missing = append(missing, "cache-dir")
	}
	return missing
}

func shellInstallHint(check shellEnvCheck) string {
	if runtime.GOOS == "windows" {
		if check.PowerShellPath == "" {
			return "Install PowerShell 7 (pwsh) or enable Windows PowerShell."
		}
		return ""
	}

	needTmux := check.TmuxPath == ""
	needBash := check.BashPath == ""
	needStty := check.SttyPath == ""

	pkgs := []string{}
	if needTmux {
		pkgs = append(pkgs, "tmux")
	}
	if needBash {
		pkgs = append(pkgs, "bash")
	}
	if needStty {
		switch check.PackageManager {
		case "apk":
			pkgs = append(pkgs, "coreutils")
		}
	}

	if len(pkgs) == 0 {
		return ""
	}

	switch check.PackageManager {
	case "brew":
		return "brew install " + strings.Join(pkgs, " ")
	case "apt-get":
		return "sudo apt-get update && sudo apt-get install -y " + strings.Join(pkgs, " ")
	case "dnf":
		return "sudo dnf install -y " + strings.Join(pkgs, " ")
	case "yum":
		return "sudo yum install -y " + strings.Join(pkgs, " ")
	case "pacman":
		return "sudo pacman -S --needed " + strings.Join(pkgs, " ")
	case "zypper":
		return "sudo zypper install -y " + strings.Join(pkgs, " ")
	case "apk":
		return "sudo apk add " + strings.Join(pkgs, " ")
	default:
		return "Install missing packages: " + strings.Join(pkgs, ", ")
	}
}

func printShellDoctor(check shellEnvCheck) {
	fmt.Printf("rat %s\n", Version)
	fmt.Printf("OS: %s/%s\n\n", check.GOOS, check.GOARCH)

	statusLine("shell platform", check.Supported, check.SupportNote)
	if runtime.GOOS == "windows" {
		statusLine("powershell", check.PowerShellPath != "", valueOrNote(check.PowerShellPath, "not found"))
	} else {
		statusLine("bash", check.BashPath != "", valueOrNote(check.BashPath, "not found"))
		statusLine("tmux", check.TmuxPath != "", valueOrNote(check.TmuxPath, "not found"))
		statusLine("stty", check.SttyPath != "", valueOrNote(check.SttyPath, "not found"))
	}
	statusLine("config dir", check.ConfigWritable, check.StateDir)
	statusLine("cache dir", check.CacheWritable, check.CacheDir)

	missing := shellMissingDeps(check)
	if len(missing) == 0 && check.Supported {
		fmt.Println("\nShell shared-session support is ready.")
		fmt.Println("Try: rat install sh")
		return
	}

	if hint := shellInstallHint(check); hint != "" {
		fmt.Println("\nSuggested fix:")
		fmt.Printf("  %s\n", hint)
	}
}

func statusLine(label string, ok bool, detail string) {
	mark := "✗"
	if ok {
		mark = "✓"
	}
	if detail == "" {
		fmt.Printf("%s %s\n", mark, label)
		return
	}
	fmt.Printf("%s %-15s %s\n", mark, label, detail)
}

func valueOrNote(value, note string) string {
	if value != "" {
		return value
	}
	return note
}
