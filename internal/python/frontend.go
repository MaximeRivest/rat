package python

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

//go:embed frontend.py
var frontendScript string

// RunFrontend launches the IPython frontend connected to the shared
// kernel MCP server. This is called by `rat py` — it starts a real
// IPython session where execution and completions go through MCP to
// the shared kernel. Everything else (history, multiline, syntax
// highlighting, magics) stays native IPython.
func RunFrontend(name string, port int, cwd string, venv string, pyVersion string) error {
	cmdPath, cmdArgs, err := detectPythonCommand("")
	if err != nil {
		return fmt.Errorf("find python for frontend: %w", err)
	}

	if pyVersion == "" {
		pyVersion = detectPythonVersion(cmdPath, cmdArgs)
	}

	scriptPath, err := writeFrontendScript(name)
	if err != nil {
		return fmt.Errorf("write frontend script: %w", err)
	}

	// Activity log lives next to the kernel script.
	activityPath := filepath.Join(filepath.Dir(scriptPath), "activity.jsonl")

	serverURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	args := append(append([]string{}, cmdArgs...), scriptPath,
		"--server", serverURL,
		"--name", name,
		"--activity-log", activityPath,
		"--cwd", cwd,
		"--venv", venv,
		"--python-version", pyVersion,
	)

	// Ignore SIGTSTP (Ctrl+Z) — rat owns the kernel lifecycle.
	signal.Ignore(syscall.SIGTSTP)

	cmd := exec.Command(cmdPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func writeFrontendScript(name string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir, err = os.UserConfigDir()
		if err != nil {
			dir = filepath.Join(os.Getenv("HOME"), ".cache")
		}
	}
	path := filepath.Join(dir, "rat", "kernels", name, "python-frontend.py")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(frontendScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
