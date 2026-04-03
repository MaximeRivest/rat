package commands

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/maximerivest/rat/internal/state"
)

// projectMarkers are files/dirs whose presence indicates a project root.
var projectMarkers = []string{
	"pyproject.toml",
	"setup.py",
	"setup.cfg",
	"requirements.txt",
	"Pipfile",
	"package.json",
	"Cargo.toml",
	"go.mod",
	"DESCRIPTION", // R packages
	".git",
}

// findProjectRoot walks from dir upward looking for project markers.
// Returns the root directory and whether a marker was actually found.
// If no marker is found, returns dir itself with found=false.
func findProjectRoot(dir string) (root string, found bool) {
	dir, _ = filepath.Abs(dir)
	current := dir
	for {
		for _, marker := range projectMarkers {
			p := filepath.Join(current, marker)
			if _, err := os.Stat(p); err == nil {
				return current, true
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir, false // reached filesystem root
		}
		current = parent
	}
}

// projectName returns the base directory name of a path.
func projectName(root string) string {
	return filepath.Base(root)
}

// resolveProjectKernelName determines the kernel name for a language
// in the given directory. If the bare name (e.g. "py") is already
// taken by a different project, returns a project-qualified name
// (e.g. "py@myproject"). If an existing kernel matches this project,
// returns its name.
func resolveProjectKernelName(s *state.Store, lang, cwd string) string {
	root, isProject := findProjectRoot(cwd)

	// Check for an existing kernel that belongs to this project.
	kernels, _ := s.List()
	for _, k := range kernels {
		if k.Lang != lang {
			continue
		}
		kRoot, _ := findProjectRoot(k.Cwd)
		if kRoot == root {
			return k.Name
		}
	}

	// No match — is the bare name free?
	existing, _ := s.Get(lang)
	if existing == nil {
		return lang // bare name available
	}

	// Bare name taken by another project — qualify with project name.
	if isProject {
		return lang + "@" + projectName(root)
	}
	return lang + "@" + projectName(cwd)
}

// findVenv looks for a Python virtual environment in the given directory
// and its ancestors (up to the project root). Returns the venv path or "".
func findVenv(dir string) string {
	dir, _ = filepath.Abs(dir)
	root, _ := findProjectRoot(dir)

	// Check dir and ancestors up to (and including) the project root.
	current := dir
	for {
		for _, name := range []string{".venv", "venv"} {
			venv := filepath.Join(current, name)
			python := filepath.Join(venv, "bin", "python")
			if runtime.GOOS == "windows" {
				python = filepath.Join(venv, "Scripts", "python.exe")
			}
			if _, err := os.Stat(python); err == nil {
				return venv
			}
		}
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return ""
}
