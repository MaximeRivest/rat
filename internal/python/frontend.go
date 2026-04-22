package python

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/maximerivest/rat/internal/cachedir"
	"github.com/maximerivest/rat/internal/userconfig"
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

	// Activity log lives in the canonical cache dir (same as the kernel daemon).
	kdir, err := cachedir.Kernels(name)
	if err != nil {
		return fmt.Errorf("resolve cache dir: %w", err)
	}
	activityPath := filepath.Join(kdir, "activity.jsonl")

	serverURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	uc := userconfig.Resolve("py")
	args := append(append([]string{}, cmdArgs...), scriptPath,
		"--server", serverURL,
		"--name", name,
		"--activity-log", activityPath,
		"--cwd", cwd,
		"--venv", venv,
		"--python-version", pyVersion,
		"--activity-max-code-lines", strconv.Itoa(uc.Activity.MaxCodeLines),
		"--activity-max-output-lines", strconv.Itoa(uc.Activity.MaxOutputLines),
		"--history-seed-limit", strconv.Itoa(uc.History.SeedLimit),
	)
	if !uc.History.SeedFromRuntime {
		args = append(args, "--no-history-seed")
	}

	ignoreSuspendSignal()

	cmd := exec.Command(cmdPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func writeFrontendScript(name string) (string, error) {
	kdir, err := cachedir.Kernels(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(kdir, "python-frontend.py")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(frontendScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
