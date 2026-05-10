package commands

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	pydetect "github.com/maximerivest/rat/internal/python"
	s "github.com/maximerivest/rat/internal/termstyle"
)

// setupState tracks what's detected and what the user wants installed.
type setupState struct {
	// Detected paths (empty = not found)
	pythonPath string
	pythonVer  string
	uvPath     string
	nodePath   string
	npmPath    string
	piPath     string
	tmuxPath   string
	bashPath   string
	psPath     string
	venvPath   string // existing .venv or VIRTUAL_ENV
	venvPython string
	ipythonOK  bool
	jediOK     bool

	// User choices
	installPython bool
	installUV     bool
	installNode   bool
	installPi     bool
	installTmux   bool
	createVenv    bool
	installDeps   bool
}

func runSetup() error {
	fmt.Println()
	fmt.Println(s.Bold("  🐀 rat setup"))
	fmt.Println()
	fmt.Printf("  %s %s/%s\n", s.Dim("OS:"), runtime.GOOS, runtime.GOARCH)
	fmt.Println()

	// ── Phase 1: detect ──────────────────────────────────────
	fmt.Println(s.Bold("  Checking system..."))
	fmt.Println()
	st := detect()
	printDetected(st)
	fmt.Println()

	// ── Phase 2: ask ─────────────────────────────────────────
	nothingToDo := true

	// Python
	if st.pythonPath == "" {
		st.installPython = confirm("  Install Python 3.12?")
		nothingToDo = false
	}

	// uv
	if st.uvPath == "" {
		st.installUV = confirm("  Install uv (fast Python package manager)?")
		nothingToDo = false
	}

	// Node.js + npm (for pi — Linux/Mac only since pi needs tmux)
	if st.nodePath == "" && runtime.GOOS != "windows" {
		st.installNode = confirm("  Install Node.js (required for pi)?")
		nothingToDo = false
	}

	// pi (coding agent) — requires tmux, not available on Windows natively
	if runtime.GOOS != "windows" {
		if st.piPath == "" {
			if st.nodePath != "" || st.installNode {
				st.installPi = confirm("  Install pi (coding agent, pi.dev)?")
				nothingToDo = false
			}
		}
	}

	// tmux (Linux/Mac only)
	if runtime.GOOS != "windows" && st.tmuxPath == "" {
		st.installTmux = confirm("  Install tmux (required for shell sharing)?")
		nothingToDo = false
	}

	// venv
	hasPython := st.pythonPath != "" || st.installPython
	hasUV := st.uvPath != "" || st.installUV
	if hasPython && st.venvPath == "" {
		st.createVenv = true // always create venv — best practice
		nothingToDo = false
	}

	// IPython + jedi
	if hasPython && (!st.ipythonOK || !st.jediOK) {
		st.installDeps = true // always install — required for rat py
		nothingToDo = false
	}

	if nothingToDo {
		fmt.Println(s.Green("  Everything is already set up. ✓"))
		printReadyMessage()
		return nil
	}

	// Show plan
	fmt.Println()
	fmt.Println(s.Bold("  Plan:"))
	step := 0
	if st.installPython {
		step++
		fmt.Printf("  %d. Install Python 3.12        %s\n", step, s.Dim(pythonInstallMethod()))
	}
	if st.installUV {
		step++
		fmt.Printf("  %d. Install uv                 %s\n", step, s.Dim(uvInstallMethod()))
	}
	if st.installNode {
		step++
		fmt.Printf("  %d. Install Node.js            %s\n", step, s.Dim(nodeInstallMethod()))
	}
	if st.installPi {
		step++
		fmt.Printf("  %d. Install pi (coding agent)  %s\n", step, s.Dim("npm install -g @earendil-works/pi-coding-agent"))
	}
	if st.installTmux {
		step++
		fmt.Printf("  %d. Install tmux               %s\n", step, s.Dim(tmuxInstallMethod()))
	}
	if st.createVenv {
		step++
		mgr := "python -m venv"
		if hasUV {
			mgr = "uv venv"
		}
		fmt.Printf("  %d. Create .venv               %s\n", step, s.Dim(mgr))
	}
	if st.installDeps {
		step++
		mgr := "pip"
		if hasUV {
			mgr = "uv pip"
		}
		fmt.Printf("  %d. Install IPython + jedi     %s\n", step, s.Dim(mgr))
	}
	fmt.Println()

	if !confirm("  Proceed?") {
		fmt.Println("  Cancelled.")
		return nil
	}
	fmt.Println()

	// ── Phase 3: execute ─────────────────────────────────────
	total := step
	current := 0

	if st.installPython {
		current++
		fmt.Printf("  [%d/%d] Installing Python...\n", current, total)
		if err := doInstallPython(); err != nil {
			return setupError(err)
		}
		refreshPath()
		st.pythonPath, st.pythonVer = detectPythonForSetup()
		if st.pythonPath == "" {
			return setupError(fmt.Errorf("Python installed but not found on PATH. Restart your terminal and re-run: rat setup"))
		}
		fmt.Printf("         %s %s\n\n", s.Green("✓"), st.pythonPath)
	}

	if st.installUV {
		current++
		fmt.Printf("  [%d/%d] Installing uv...\n", current, total)
		if err := doInstallUV(); err != nil {
			return setupError(err)
		}
		refreshPath()
		st.uvPath, _ = exec.LookPath("uv")
		if st.uvPath == "" {
			// Try common locations
			for _, p := range uvSearchPaths() {
				if _, err := os.Stat(p); err == nil {
					st.uvPath = p
					break
				}
			}
		}
		if st.uvPath != "" {
			fmt.Printf("         %s %s\n\n", s.Green("✓"), st.uvPath)
		} else {
			fmt.Printf("         %s installed but not found on PATH — continuing with pip\n\n", s.Yellow("⚠"))
		}
	}

	if st.installNode {
		current++
		fmt.Printf("  [%d/%d] Installing Node.js...\n", current, total)
		if err := doInstallNode(); err != nil {
			return setupError(err)
		}
		refreshPath()
		st.nodePath, _ = exec.LookPath("node")
		st.npmPath, _ = exec.LookPath("npm")
		if st.nodePath != "" {
			fmt.Printf("         %s %s\n\n", s.Green("✓"), st.nodePath)
		} else {
			fmt.Printf("         %s installed but not found on PATH — restart terminal\n\n", s.Yellow("⚠"))
		}
	}

	if st.installPi {
		current++
		fmt.Printf("  [%d/%d] Installing pi (coding agent)...\n", current, total)
		if err := doInstallPi(st.npmPath); err != nil {
			fmt.Printf("         %s %v — skipping, install manually later\n\n", s.Yellow("⚠"), err)
		} else {
			refreshPath()
			st.piPath, _ = exec.LookPath("pi")
			fmt.Printf("         %s done\n\n", s.Green("✓"))
		}
	}

	if st.installTmux {
		current++
		fmt.Printf("  [%d/%d] Installing tmux...\n", current, total)
		if err := doInstallTmux(); err != nil {
			fmt.Printf("         %s %v — shell sharing won't work\n\n", s.Yellow("⚠"), err)
		} else {
			st.tmuxPath, _ = exec.LookPath("tmux")
			fmt.Printf("         %s done\n\n", s.Green("✓"))
		}
	}

	// Re-detect Python after installs
	if st.pythonPath == "" {
		st.pythonPath, st.pythonVer = detectPythonForSetup()
	}

	if st.createVenv && st.pythonPath != "" {
		current++
		fmt.Printf("  [%d/%d] Creating .venv...\n", current, total)
		if err := doCreateVenv(st); err != nil {
			return setupError(err)
		}
		cwd, _ := os.Getwd()
		st.venvPath = filepath.Join(cwd, ".venv")
		st.venvPython = venvPythonPath(st.venvPath)
		fmt.Printf("         %s %s\n\n", s.Green("✓"), st.venvPath)
	}

	if st.installDeps {
		current++
		py := st.effectivePython()
		if py == "" {
			fmt.Printf("  [%d/%d] %s no Python available — skipping IPython install\n\n", current, total, s.Yellow("⚠"))
		} else {
			fmt.Printf("  [%d/%d] Installing IPython + jedi...\n", current, total)
			if err := doInstallPythonDeps(st, py); err != nil {
				return setupError(err)
			}
			st.ipythonOK = canImport(py, "IPython")
			st.jediOK = canImport(py, "jedi")
			if st.ipythonOK && st.jediOK {
				fmt.Printf("         %s done\n\n", s.Green("✓"))
			} else {
				fmt.Printf("         %s partial install — try: %s -m pip install ipython jedi\n\n", s.Yellow("⚠"), py)
			}
		}
	}

	// ── Phase 4: summary ─────────────────────────────────────
	fmt.Println(s.Bold("  Result:"))
	fmt.Println()
	printFinalStatus(st)
	printReadyMessage()
	return nil
}

// ── Detection ────────────────────────────────────────────────

func detect() *setupState {
	st := &setupState{}

	// Python — use the same detection as the kernel
	st.pythonPath, st.pythonVer = detectPythonForSetup()
	// Reject Windows Store aliases
	if st.pythonPath != "" && pydetect.IsWindowsStoreAlias(st.pythonPath) {
		st.pythonPath = ""
		st.pythonVer = ""
	}

	// uv
	st.uvPath, _ = exec.LookPath("uv")

	// Node.js / npm
	st.nodePath, _ = exec.LookPath("node")
	st.npmPath, _ = exec.LookPath("npm")

	// pi — the coding agent harness (@earendil-works/pi-coding-agent)
	st.piPath, _ = exec.LookPath("pi")

	// Shell
	if runtime.GOOS == "windows" {
		for _, c := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(c); err == nil {
				st.psPath = p
				break
			}
		}
	} else {
		st.bashPath, _ = exec.LookPath("bash")
		st.tmuxPath, _ = exec.LookPath("tmux")
	}

	// Venv
	cwd, _ := os.Getwd()
	for _, name := range []string{".venv", "venv"} {
		d := filepath.Join(cwd, name)
		if py := venvPythonPath(d); py != "" {
			st.venvPath = d
			st.venvPython = py
			break
		}
	}
	if st.venvPath == "" {
		if v := os.Getenv("VIRTUAL_ENV"); v != "" {
			if py := venvPythonPath(v); py != "" {
				st.venvPath = v
				st.venvPython = py
			}
		}
	}

	// IPython + jedi
	py := st.effectivePython()
	if py != "" {
		st.ipythonOK = canImport(py, "IPython")
		st.jediOK = canImport(py, "jedi")
	}

	return st
}

func (st *setupState) effectivePython() string {
	if st.venvPython != "" {
		return st.venvPython
	}
	return st.pythonPath
}

// ── Display ──────────────────────────────────────────────────

func printDetected(st *setupState) {
	check := func(label string, path string) {
		if path != "" {
			fmt.Printf("    %s %-14s %s\n", s.Green("✓"), label, s.Dim(path))
		} else {
			fmt.Printf("    %s %-14s %s\n", s.Red("✗"), label, s.Dim("not found"))
		}
	}

	check("Python", st.pythonPath)
	check("uv", st.uvPath)
	if runtime.GOOS != "windows" {
		check("Node.js", st.nodePath)
		check("npm", st.npmPath)
		check("pi", st.piPath)
	}

	if runtime.GOOS == "windows" {
		check("PowerShell", st.psPath)
	} else {
		check("bash", st.bashPath)
		check("tmux", st.tmuxPath)
	}

	if st.venvPath != "" {
		fmt.Printf("    %s %-14s %s\n", s.Green("✓"), ".venv", s.Dim(st.venvPath))
	} else {
		fmt.Printf("    %s %-14s %s\n", s.Dim("·"), ".venv", s.Dim("none"))
	}

	if st.ipythonOK {
		fmt.Printf("    %s %-14s %s\n", s.Green("✓"), "IPython", s.Dim("installed"))
	} else {
		fmt.Printf("    %s %-14s %s\n", s.Dim("·"), "IPython", s.Dim("not installed"))
	}
}

func printFinalStatus(st *setupState) {
	ok := func(label string, found bool, detail string) {
		if found {
			fmt.Printf("    %s %-14s %s\n", s.Green("✓"), label, s.Dim(detail))
		} else {
			fmt.Printf("    %s %-14s %s\n", s.Red("✗"), label, s.Dim(detail))
		}
	}

	ok("Python", st.pythonPath != "", st.pythonPath)
	ok("uv", st.uvPath != "", st.uvPath)
	if runtime.GOOS != "windows" {
		ok("Node.js", st.nodePath != "", st.nodePath)
		ok("pi", st.piPath != "", st.piPath)
	}

	if runtime.GOOS == "windows" {
		ok("PowerShell", st.psPath != "", st.psPath)
	} else {
		ok("bash", st.bashPath != "", st.bashPath)
		ok("tmux", st.tmuxPath != "", st.tmuxPath)
	}

	ok(".venv", st.venvPath != "", st.venvPath)
	ok("IPython", st.ipythonOK, "")
	ok("jedi", st.jediOK, "")
	fmt.Println()
}

func printReadyMessage() {
	fmt.Println()
	fmt.Println("  Try:")
	fmt.Println("    " + s.Cyan("rat py") + s.Dim("              Python REPL"))
	fmt.Println("    " + s.Cyan("rat sh") + s.Dim("              Shell"))
	fmt.Println("    " + s.Cyan("rat run py 'print(42)'") + s.Dim("  One-liner"))
	fmt.Println()
}

// ── Install methods (display strings) ────────────────────────

func pythonInstallMethod() string {
	switch runtime.GOOS {
	case "windows":
		return "winget"
	case "darwin":
		return "brew"
	default:
		pm := detectPackageManager()
		if pm != "" {
			return pm
		}
		return "system package manager"
	}
}

func uvInstallMethod() string {
	switch runtime.GOOS {
	case "windows":
		return "winget"
	default:
		return "curl (official installer)"
	}
}

func nodeInstallMethod() string {
	switch runtime.GOOS {
	case "windows":
		return "winget"
	case "darwin":
		return "brew"
	default:
		pm := detectPackageManager()
		if pm != "" {
			return pm
		}
		return "system package manager"
	}
}

func tmuxInstallMethod() string {
	pm := detectPackageManager()
	if pm != "" {
		return pm
	}
	return "system package manager"
}

// ── Install actions ──────────────────────────────────────────

func doInstallPython() error {
	switch runtime.GOOS {
	case "windows":
		return execCmd("winget", "install", "--accept-package-agreements", "--accept-source-agreements", "Python.Python.3.12")
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return execCmd("brew", "install", "python@3.12")
		}
		return fmt.Errorf("Homebrew not found. Install from https://python.org")
	default:
		return installWithPM([]string{"python3", "python3-venv", "python3-pip"}, []string{"python3", "python3-pip"}, []string{"python", "python-pip"})
	}
}

func doInstallUV() error {
	switch runtime.GOOS {
	case "windows":
		// Try winget
		if _, err := exec.LookPath("winget"); err == nil {
			return execCmd("winget", "install", "--accept-package-agreements", "--accept-source-agreements", "astral-sh.uv")
		}
		// Fallback: PowerShell installer
		return runShell("powershell", "-ExecutionPolicy", "ByPass", "-c", "irm https://astral.sh/uv/install.ps1 | iex")
	default:
		return runShell("sh", "-c", "curl -LsSf https://astral.sh/uv/install.sh | sh")
	}
}

func doInstallNode() error {
	switch runtime.GOOS {
	case "windows":
		return execCmd("winget", "install", "--accept-package-agreements", "--accept-source-agreements", "OpenJS.NodeJS.LTS")
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return execCmd("brew", "install", "node")
		}
		return fmt.Errorf("Homebrew not found. Install Node.js from https://nodejs.org")
	default:
		return installWithPM([]string{"nodejs", "npm"}, []string{"nodejs", "npm"}, []string{"nodejs", "npm"})
	}
}

func doInstallPi(npmPath string) error {
	if npmPath == "" {
		npmPath, _ = exec.LookPath("npm")
	}
	if npmPath == "" {
		return fmt.Errorf("npm not found — install Node.js first")
	}
	return execCmd(npmPath, "install", "-g", "@earendil-works/pi-coding-agent")
}

func doInstallTmux() error {
	return installWithPM([]string{"tmux"}, []string{"tmux"}, []string{"tmux"})
}

func doCreateVenv(st *setupState) error {
	cwd, _ := os.Getwd()
	venvDir := filepath.Join(cwd, ".venv")

	// Already exists?
	if py := venvPythonPath(venvDir); py != "" {
		fmt.Println("         .venv already exists")
		return nil
	}

	// Prefer uv
	if st.uvPath != "" {
		args := []string{"venv", venvDir}
		if st.pythonPath != "" {
			args = []string{"venv", "--python", st.pythonPath, venvDir}
		}
		return execCmd(st.uvPath, args...)
	}

	// Fallback: python -m venv
	py := st.pythonPath
	if py == "" {
		return fmt.Errorf("no Python available to create venv")
	}
	return execCmd(py, "-m", "venv", venvDir)
}

func doInstallPythonDeps(st *setupState, py string) error {
	pkgs := []string{"ipython", "jedi"}

	// Prefer uv pip
	if st.uvPath != "" {
		args := []string{"pip", "install", "--python", py}
		args = append(args, pkgs...)
		return execCmd(st.uvPath, args...)
	}

	// Fallback: pip
	args := []string{"-m", "pip", "install", "--quiet"}
	args = append(args, pkgs...)
	return execCmd(py, args...)
}

// ── Platform helpers ─────────────────────────────────────────

// installWithPM tries the system package manager with distro-appropriate package names.
// aptPkgs for Debian/Ubuntu, dnfPkgs for Fedora/RHEL, pacPkgs for Arch.
func installWithPM(aptPkgs, dnfPkgs, pacPkgs []string) error {
	pm := detectPackageManager()
	switch pm {
	case "apt-get":
		args := append([]string{"apt-get", "install", "-y"}, aptPkgs...)
		return execCmd("sudo", args...)
	case "dnf":
		args := append([]string{"dnf", "install", "-y"}, dnfPkgs...)
		return execCmd("sudo", args...)
	case "yum":
		args := append([]string{"yum", "install", "-y"}, dnfPkgs...)
		return execCmd("sudo", args...)
	case "pacman":
		args := append([]string{"pacman", "-S", "--noconfirm"}, pacPkgs...)
		return execCmd("sudo", args...)
	case "zypper":
		args := append([]string{"zypper", "install", "-y"}, aptPkgs...)
		return execCmd("sudo", args...)
	case "apk":
		args := append([]string{"apk", "add"}, aptPkgs...)
		return execCmd("sudo", args...)
	case "brew":
		args := append([]string{"install"}, aptPkgs...)
		return execCmd("brew", args...)
	default:
		return fmt.Errorf("no supported package manager found. Install manually: %s", strings.Join(aptPkgs, ", "))
	}
}

func execCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runShell(shell string, args ...string) error {
	cmd := exec.Command(shell, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func uvSearchPaths() []string {
	home, _ := os.UserHomeDir()
	paths := []string{}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			paths = append(paths, filepath.Join(local, "uv", "uv.exe"))
		}
		paths = append(paths, filepath.Join(home, ".cargo", "bin", "uv.exe"))
	} else {
		paths = append(paths,
			filepath.Join(home, ".local", "bin", "uv"),
			filepath.Join(home, ".cargo", "bin", "uv"),
			"/usr/local/bin/uv",
		)
	}
	return paths
}

// refreshPath reloads PATH so newly installed binaries are found
// without restarting the terminal.
func refreshPath() {
	if runtime.GOOS == "windows" {
		refreshWindowsPath()
		return
	}
	// On Unix, new installs usually land in /usr/local/bin or ~/.local/bin.
	// Ensure those are on PATH.
	home, _ := os.UserHomeDir()
	extras := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		"/usr/local/bin",
	}
	current := os.Getenv("PATH")
	for _, p := range extras {
		if !strings.Contains(current, p) {
			current = p + string(os.PathListSeparator) + current
		}
	}
	os.Setenv("PATH", current)
}

// refreshWindowsPath reloads PATH from the registry so newly installed
// programs are found without restarting the terminal.
func refreshWindowsPath() {
	if runtime.GOOS != "windows" {
		return
	}
	for _, scope := range []string{
		`HKCU\Environment`,
		`HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`,
	} {
		cmd := exec.Command("reg", "query", scope, "/v", "Path")
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if !strings.Contains(strings.ToUpper(line), "PATH") {
				continue
			}
			// Line format: "    Path    REG_EXPAND_SZ    C:\foo;C:\bar"
			idx := strings.Index(line, "REG_")
			if idx < 0 {
				continue
			}
			rest := line[idx:]
			// Skip the REG_xxx type
			space := strings.IndexAny(rest, " \t")
			if space < 0 {
				continue
			}
			newPaths := strings.TrimSpace(rest[space:])
			// Expand %vars%
			newPaths = os.ExpandEnv(newPaths)
			existing := os.Getenv("PATH")
			// Merge new paths that aren't already present
			for _, p := range strings.Split(newPaths, ";") {
				p = strings.TrimSpace(p)
				if p != "" && !strings.Contains(existing, p) {
					existing = p + ";" + existing
				}
			}
			os.Setenv("PATH", existing)
		}
	}
	time.Sleep(500 * time.Millisecond)
}

func setupError(err error) error {
	fmt.Printf("\n  %s %s\n", s.Red("✗"), err)
	fmt.Println()
	fmt.Println(s.Dim("  Fix the issue above and re-run: rat setup"))
	return err
}

func confirm(prompt string) bool {
	fmt.Printf("%s [Y/n] ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}
