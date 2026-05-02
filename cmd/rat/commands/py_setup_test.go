//go:build !windows

package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshPythonImportChecksUsesEffectiveVenvPython(t *testing.T) {
	dir := t.TempDir()
	systemPy := filepath.Join(dir, "system-python")
	venvPy := filepath.Join(dir, "venv-python")

	if err := os.WriteFile(systemPy, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write system python: %v", err)
	}
	venvScript := "#!/bin/sh\ncase \"$2\" in\n  *IPython*) exit 1 ;;\n  *jedi*) exit 0 ;;\n  *) exit 0 ;;\nesac\n"
	if err := os.WriteFile(venvPy, []byte(venvScript), 0o755); err != nil {
		t.Fatalf("write venv python: %v", err)
	}

	check := pythonEnvCheck{
		PythonPath: systemPy,
		VenvPython: venvPy,
		IPythonOK:  true,
		JediOK:     true,
	}

	refreshPythonImportChecks(&check)

	if check.IPythonOK {
		t.Fatal("IPythonOK should be recomputed against venv python")
	}
	if !check.JediOK {
		t.Fatal("JediOK should be true from venv python")
	}
}
