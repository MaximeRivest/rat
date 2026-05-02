package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/daemon"
	pydetect "github.com/maximerivest/rat/internal/python"
	"github.com/maximerivest/rat/internal/state"
)

type pythonEnvCheck struct {
	GOOS        string
	GOARCH      string
	Supported   bool
	SupportNote string

	PythonPath    string
	PythonVersion string
	UvPath        string
	PipPath       string
	VenvPath      string // detected venv directory
	VenvPython    string // python inside the venv
	IPythonOK     bool
	JediOK        bool

	PackageManager string
	StateDir       string
	CacheDir       string
	ConfigWritable bool
	CacheWritable  bool
}

func inspectPythonEnv() pythonEnvCheck {
	check := pythonEnvCheck{
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}

	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		check.Supported = true
	default:
		check.SupportNote = fmt.Sprintf("Python support is not tested on %s yet.", runtime.GOOS)
	}

	check.UvPath, _ = exec.LookPath("uv")
	check.PipPath, _ = exec.LookPath("pip3")
	if check.PipPath == "" {
		check.PipPath, _ = exec.LookPath("pip")
	}
	check.PackageManager = detectPackageManager()
	check.StateDir = filepath.Dir(state.DefaultPath())
	check.CacheDir = defaultCacheDir()
	check.ConfigWritable = dirWritable(check.StateDir)
	check.CacheWritable = dirWritable(check.CacheDir)

	// Detect Python interpreter (same order as the kernel).
	check.PythonPath, check.PythonVersion = detectPythonForSetup()

	// Detect venv in cwd or parents.
	check.VenvPath, check.VenvPython = detectVenv(check.PythonPath)

	// Check if IPython and jedi are importable.
	refreshPythonImportChecks(&check)

	return check
}

// effectivePython returns the best Python to use — venv python if
// available, otherwise system python.
func (c *pythonEnvCheck) effectivePython() string {
	if c.VenvPython != "" {
		return c.VenvPython
	}
	return c.PythonPath
}

func detectPythonForSetup() (path, version string) {
	if v := os.Getenv("RAT_PYTHON"); v != "" {
		ver := pythonVersion(v)
		return v, ver
	}
	if venv := os.Getenv("VIRTUAL_ENV"); venv != "" {
		p := filepath.Join(venv, "bin", "python")
		if runtime.GOOS == "windows" {
			p = filepath.Join(venv, "Scripts", "python.exe")
		}
		if _, err := os.Stat(p); err == nil {
			return p, pythonVersion(p)
		}
	}
	for _, candidate := range []string{"python3", "python"} {
		if p, err := exec.LookPath(candidate); err == nil {
			if !pydetect.IsWindowsStoreAlias(p) {
				return p, pythonVersion(p)
			}
		}
	}
	if p, err := exec.LookPath("py"); err == nil {
		return p, pythonVersion(p)
	}
	if runtime.GOOS == "windows" {
		if p := pydetect.FindWindowsPython(); p != "" {
			return p, pythonVersion(p)
		}
	}
	return "", ""
}

func pythonVersion(pythonPath string) string {
	out, err := exec.Command(pythonPath, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	// "Python 3.12.1\n" → "3.12.1"
	s := strings.TrimSpace(string(out))
	s = strings.TrimPrefix(s, "Python ")
	return s
}

func detectVenv(fallbackPython string) (venvDir, venvPython string) {
	// Check VIRTUAL_ENV env var first.
	if v := os.Getenv("VIRTUAL_ENV"); v != "" {
		py := venvPythonPath(v)
		if py != "" {
			return v, py
		}
	}
	// Walk cwd and parents looking for .venv or venv.
	cwd, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	dir := cwd
	for {
		for _, name := range []string{".venv", "venv"} {
			candidate := filepath.Join(dir, name)
			py := venvPythonPath(candidate)
			if py != "" {
				return candidate, py
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

func venvPythonPath(venvDir string) string {
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{filepath.Join(venvDir, "Scripts", "python.exe")}
	} else {
		candidates = []string{
			filepath.Join(venvDir, "bin", "python"),
			filepath.Join(venvDir, "bin", "python3"),
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func canImport(pythonPath, module string) bool {
	cmd := exec.Command(pythonPath, "-c", fmt.Sprintf("import %s", module))
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func refreshPythonImportChecks(check *pythonEnvCheck) {
	if check == nil {
		return
	}
	py := check.effectivePython()
	if py == "" {
		check.IPythonOK = false
		check.JediOK = false
		return
	}
	check.IPythonOK = canImport(py, "IPython")
	check.JediOK = canImport(py, "jedi")
}

// ── Install ─────────────────────────────────────────────────

func installPythonRuntime() error {
	check := inspectPythonEnv()
	fmt.Println("rat install py")
	fmt.Printf("OS: %s/%s\n", check.GOOS, check.GOARCH)

	if !check.Supported {
		fmt.Printf("\n%s\n", check.SupportNote)
		return fmt.Errorf("python install blocked on this platform")
	}

	if check.PythonPath == "" {
		fmt.Println("")
		statusLine("python", false, "not found")
		fmt.Println("")
		fmt.Println("Install Python first:")
		if hint := pythonInstallHint(check); hint != "" {
			fmt.Printf("  %s\n", hint)
		}
		return fmt.Errorf("python not found")
	}

	fmt.Println("")
	statusLine("python", true, fmt.Sprintf("%s (%s)", check.PythonPath, check.PythonVersion))
	statusLine("uv", check.UvPath != "", valueOrNote(check.UvPath, "not found (will use pip)"))

	// Ensure venv exists.
	if check.VenvPath == "" {
		fmt.Println("")
		fmt.Println("No venv detected. Creating .venv in current directory...")
		if err := createVenv(check); err != nil {
			return err
		}
		// Re-detect after creation.
		check.VenvPath, check.VenvPython = detectVenv(check.PythonPath)
		if check.VenvPath == "" {
			return fmt.Errorf("failed to create venv")
		}
	}
	statusLine("venv", true, check.VenvPath)

	// Recompute dependency checks against the effective interpreter after
	// venv creation. The initial inspection may have checked system Python.
	refreshPythonImportChecks(&check)

	// Install ipython + jedi into the venv.
	py := check.effectivePython()
	if !check.IPythonOK || !check.JediOK {
		fmt.Println("")
		fmt.Println("Installing IPython + jedi...")
		if err := installPythonDeps(check); err != nil {
			return err
		}
		// Re-check.
		check.IPythonOK = canImport(py, "IPython")
		check.JediOK = canImport(py, "jedi")
	}
	statusLine("ipython", check.IPythonOK, importVersion(py, "IPython"))
	statusLine("jedi", check.JediOK, importVersion(py, "jedi"))

	if !check.IPythonOK {
		return fmt.Errorf("IPython installation failed — try manually: %s -m pip install ipython jedi", py)
	}

	// Start the kernel (project-aware, like `rat py`).
	r, err := resolveInput("py")
	if err != nil {
		return err
	}

	k, err := daemon.Start(store(), daemon.StartOpts{
		Name: r.Name,
		Lang: r.Lang,
		Cwd:  r.Cwd,
		Venv: check.VenvPath,
	})
	if err != nil {
		return err
	}

	// Smoke test.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := connectToKernel(ctx, r.Name)
	if err != nil {
		return err
	}
	defer session.Close()

	result, err := session.Run(ctx, "print('rat py ready')")
	if err != nil {
		return err
	}
	text := extractText(result)

	fmt.Println("")
	statusLine("kernel", true, fmt.Sprintf("http://127.0.0.1:%d/mcp", k.Port))
	if text != "" {
		fmt.Println(text)
	}
	fmt.Println("")
	fmt.Println("Ready.")
	fmt.Println("Try:")
	fmt.Println("  rat py")
	fmt.Println("  rat run py 'print(42)'")
	fmt.Println("  rat look py --at x")
	return nil
}

func createVenv(check pythonEnvCheck) error {
	cwd, _ := os.Getwd()
	venvDir := filepath.Join(cwd, ".venv")

	if check.UvPath != "" {
		// uv venv is fast and reliable.
		cmd := exec.Command(check.UvPath, "venv", venvDir)
		if check.PythonPath != "" {
			cmd = exec.Command(check.UvPath, "venv", "--python", check.PythonPath, venvDir)
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("uv venv: %w", err)
		}
		return nil
	}

	// Fallback: python -m venv.
	py := check.PythonPath
	if py == "" {
		return fmt.Errorf("no python found to create venv")
	}
	cmd := exec.Command(py, "-m", "venv", venvDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("python -m venv: %w", err)
	}
	return nil
}

func installPythonDeps(check pythonEnvCheck) error {
	py := check.effectivePython()
	pkgs := []string{"ipython", "jedi"}

	if check.UvPath != "" {
		// uv pip install is fast.
		args := []string{"pip", "install", "--python", py}
		args = append(args, pkgs...)
		cmd := exec.Command(check.UvPath, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("uv pip install: %w", err)
		}
		return nil
	}

	// Fallback: pip.
	args := []string{"-m", "pip", "install", "--quiet"}
	args = append(args, pkgs...)
	cmd := exec.Command(py, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pip install: %w", err)
	}
	return nil
}

func importVersion(pythonPath, module string) string {
	var buf bytes.Buffer
	cmd := exec.Command(pythonPath, "-c", fmt.Sprintf("import %s; print(%s.__version__)", module, module))
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if cmd.Run() != nil {
		return "not installed"
	}
	return strings.TrimSpace(buf.String())
}

// ── Doctor ──────────────────────────────────────────────────

func printPythonDoctor(check pythonEnvCheck) {
	fmt.Printf("rat %s\n", Version)
	fmt.Printf("OS: %s/%s\n\n", check.GOOS, check.GOARCH)

	statusLine("python platform", check.Supported, check.SupportNote)
	statusLine("python", check.PythonPath != "", valueOrNote(
		fmt.Sprintf("%s (%s)", check.PythonPath, check.PythonVersion),
		"not found"))
	statusLine("uv", check.UvPath != "", valueOrNote(check.UvPath, "not found (optional, speeds up install)"))

	if check.VenvPath != "" {
		statusLine("venv", true, check.VenvPath)
	} else {
		statusLine("venv", false, "none detected (will create on install)")
	}

	py := check.effectivePython()
	if py != "" {
		statusLine("ipython", check.IPythonOK, importVersion(py, "IPython"))
		statusLine("jedi", check.JediOK, importVersion(py, "jedi"))
	} else {
		statusLine("ipython", false, "no python to check")
		statusLine("jedi", false, "no python to check")
	}

	statusLine("config dir", check.ConfigWritable, check.StateDir)
	statusLine("cache dir", check.CacheWritable, check.CacheDir)

	if check.PythonPath == "" {
		fmt.Println("")
		fmt.Println("Python not found. Install it:")
		if hint := pythonInstallHint(check); hint != "" {
			fmt.Printf("  %s\n", hint)
		}
		return
	}

	if !check.IPythonOK || !check.JediOK {
		fmt.Println("\nRun 'rat install py' to install missing dependencies.")
		return
	}

	fmt.Println("\nPython support is ready.")
	fmt.Println("Try: rat install py")
}

func pythonInstallHint(check pythonEnvCheck) string {
	if runtime.GOOS == "windows" {
		return "winget install Python.Python.3"
	}
	switch check.PackageManager {
	case "brew":
		return "brew install python3"
	case "apt-get":
		return "sudo apt-get update && sudo apt-get install -y python3 python3-venv"
	case "dnf":
		return "sudo dnf install -y python3"
	case "yum":
		return "sudo yum install -y python3"
	case "pacman":
		return "sudo pacman -S python"
	case "zypper":
		return "sudo zypper install -y python3"
	case "apk":
		return "sudo apk add python3"
	default:
		return "Install Python 3 from https://python.org"
	}
}
