// Package project answers one question for every rat client: given a
// directory, which project does it belong to, and which environment does
// that project run in?
//
// The answer must be the same from the terminal, VS Code, aiconvo and an
// agent, so the rules live here and nowhere else.
//
// Markers are ranked. A folder that merely contains a requirements.txt or
// a Makefile is a hint, not a project: documentation folders, examples and
// scripts directories carry such files inside a larger repository all the
// time. The walk therefore stops immediately only for strong evidence:
//
//  1. A folder with its own virtual environment. Code runs there; that is
//     the project regardless of anything above it.
//  2. A folder with a repository root or a package manifest (.git,
//     pyproject.toml, package.json, Cargo.toml, ...).
//  3. Otherwise the nearest folder with a weak hint (requirements.txt,
//     Pipfile, Makefile, ...), remembered while walking, is used only when
//     nothing stronger exists above it.
//
// A notebook that lives in an unusual layout can still pin its project
// explicitly (see the notebook package); this file provides the default.
package project

import (
	"os"
	"path/filepath"
	"runtime"
)

// StrongMarkers identify a project on their own: the walk stops at the
// nearest folder containing one of them.
var StrongMarkers = []string{
	".git",

	"pyproject.toml",
	"setup.py",
	"setup.cfg",

	"DESCRIPTION",
	"renv.lock",

	"Project.toml",
	"JuliaProject.toml",

	"package.json",
	"deno.json",
	"deno.jsonc",

	"Cargo.toml",

	"go.mod",

	"Gemfile",

	"composer.json",

	"pom.xml",
	"build.gradle",
	"build.gradle.kts",

	"stack.yaml",
	"cabal.project",

	"mix.exs",

	"pubspec.yaml",

	"Package.swift",

	"build.zig",
}

// StrongGlobMarkers are strong markers matched by pattern.
var StrongGlobMarkers = []string{
	"*.sln",
	"*.csproj",
}

// WeakMarkers suggest a project but also appear inside sub-folders of
// larger projects (docs/requirements.txt, examples/Makefile). They count
// only when no strong marker exists in the folder or above it.
var WeakMarkers = []string{
	"requirements.txt",
	"Pipfile",
	"tox.ini",
	"CMakeLists.txt",
	"meson.build",
	"Makefile",
	".editorconfig",
}

// Markers lists every marker, strongest first. Kept for callers that only
// need to know what rat treats as a project signal.
var Markers = append(append([]string{}, StrongMarkers...), WeakMarkers...)

// FindRoot returns the project root for dir and whether any marker was
// found. Without any marker the start directory itself is returned so
// callers always get a usable path.
func FindRoot(dir string) (root string, found bool) {
	dir, _ = filepath.Abs(dir)
	weak := ""
	current := dir
	for {
		if VenvIn(current) != "" {
			return current, true
		}
		if hasAny(current, StrongMarkers) || hasAnyGlob(current, StrongGlobMarkers) {
			return current, true
		}
		if weak == "" && hasAny(current, WeakMarkers) {
			weak = current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break // reached filesystem root
		}
		current = parent
	}
	if weak != "" {
		return weak, true
	}
	return dir, false
}

// Name returns the display name of a project root.
func Name(root string) string {
	home, err := os.UserHomeDir()
	if err == nil && root == home {
		return "home"
	}
	return filepath.Base(root)
}

// VenvIn returns the virtual environment directory directly inside dir
// (.venv or venv, containing a python executable), or "".
func VenvIn(dir string) string {
	for _, name := range []string{".venv", "venv"} {
		venv := filepath.Join(dir, name)
		if VenvPython(venv) != "" {
			return venv
		}
	}
	return ""
}

// VenvPython returns the interpreter inside a virtual environment
// directory, or "" when the directory is not a usable venv.
func VenvPython(venv string) string {
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{filepath.Join(venv, "Scripts", "python.exe")}
	} else {
		candidates = []string{
			filepath.Join(venv, "bin", "python"),
			filepath.Join(venv, "bin", "python3"),
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// FindVenv returns the virtual environment a directory runs in: the
// nearest .venv/venv walking up from dir, never past the project root.
// Environments above the project are someone else's.
func FindVenv(dir string) string {
	dir, _ = filepath.Abs(dir)
	root, _ := FindRoot(dir)
	return FindVenvWithin(dir, root)
}

// FindVenvWithin is FindVenv with an explicit project root: the nearest
// venv walking up from dir, stopping at root. Used when a notebook pins
// its project instead of relying on marker detection.
func FindVenvWithin(dir, root string) string {
	dir, _ = filepath.Abs(dir)
	root, _ = filepath.Abs(root)

	current := dir
	for {
		if venv := VenvIn(current); venv != "" {
			return venv
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

func hasAny(dir string, names []string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

func hasAnyGlob(dir string, patterns []string) bool {
	for _, pattern := range patterns {
		if matches, _ := filepath.Glob(filepath.Join(dir, pattern)); len(matches) > 0 {
			return true
		}
	}
	return false
}
