// Package project implements project root detection, venv detection,
// and project-aware kernel naming for rat.
package project

import (
	"os"
	"path/filepath"
	"runtime"
)

// Markers are files/dirs whose presence indicates a project root.
// Ordered roughly by prevalence. Covers: git, Python, R, Julia,
// Node/JS, Rust, Go, Ruby, PHP, .NET, Java/Kotlin, Haskell, Elixir,
// Dart, Swift, Zig, Meson, CMake, Make, and common editor/tool configs.
var Markers = []string{
	// VCS
	".git",

	// Python
	"pyproject.toml",
	"setup.py",
	"setup.cfg",
	"requirements.txt",
	"Pipfile",
	"tox.ini",

	// R
	"DESCRIPTION",
	"renv.lock",

	// Julia
	"Project.toml",
	"JuliaProject.toml",

	// JavaScript / TypeScript
	"package.json",
	"deno.json",
	"deno.jsonc",

	// Rust
	"Cargo.toml",

	// Go
	"go.mod",

	// Ruby
	"Gemfile",

	// PHP
	"composer.json",

	// Java / Kotlin
	"pom.xml",
	"build.gradle",
	"build.gradle.kts",

	// Haskell
	"stack.yaml",
	"cabal.project",

	// Elixir
	"mix.exs",

	// Dart / Flutter
	"pubspec.yaml",

	// Swift
	"Package.swift",

	// Zig
	"build.zig",

	// Build systems
	"CMakeLists.txt",
	"meson.build",
	"Makefile",

	// Editor / tool configs (strong project root signals)
	".editorconfig",
}

// GlobMarkers are file patterns checked via filepath.Glob (for
// extensions that vary by project name, like .sln and .csproj).
var GlobMarkers = []string{
	"*.sln",
	"*.csproj",
}

// FindRoot walks from dir upward looking for project markers.
// Returns the root directory and whether a marker was actually found.
// If no marker is found, returns dir itself with found=false.
func FindRoot(dir string) (root string, found bool) {
	dir, _ = filepath.Abs(dir)
	current := dir
	for {
		// Check exact markers.
		for _, marker := range Markers {
			p := filepath.Join(current, marker)
			if _, err := os.Stat(p); err == nil {
				return current, true
			}
		}
		// Check glob markers.
		for _, pattern := range GlobMarkers {
			matches, _ := filepath.Glob(filepath.Join(current, pattern))
			if len(matches) > 0 {
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

// Name returns the base directory name of a path.
// Home directory is returned as "home".
func Name(root string) string {
	home, err := os.UserHomeDir()
	if err == nil && root == home {
		return "home"
	}
	return filepath.Base(root)
}

// FindVenv looks for a Python virtual environment in the given directory
// and its ancestors (up to the project root). Returns the venv path or "".
func FindVenv(dir string) string {
	dir, _ = filepath.Abs(dir)
	root, _ := FindRoot(dir)

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
