// Package runtimes embeds built-in runtime configs and kernel scripts.
//
// Built-in runtimes ship inside the rat binary via go:embed. On first
// use, they are extracted to a cache directory. User-defined runtimes
// in ~/.config/rat/runtimes/ take priority over built-ins.
package runtimes

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maximerivest/rat/internal/cachedir"
)

//go:embed frontend.py r/runtime.yaml r/kernel.R pi/runtime.yaml pi/bridge.ts slack/runtime.yaml slack/kernel-slack.py jupyter/runtime.yaml jupyter/kernel-jupyter.py
var embedded embed.FS

// builtinLangs lists which languages have embedded runtimes.
var builtinLangs = []string{"r", "pi", "slack", "jupyter"}

// IsBuiltin returns true if the language has a built-in runtime.
func IsBuiltin(lang string) bool {
	for _, l := range builtinLangs {
		if l == lang {
			return true
		}
	}
	return false
}

// List returns all built-in language names.
func List() []string {
	return append([]string{}, builtinLangs...)
}

// Extract writes the embedded runtime files to a cache directory and
// returns the path to runtime.yaml. Files are only written if missing
// or if the binary is newer than the cached files.
func Extract(lang string) (string, error) {
	if !IsBuiltin(lang) {
		return "", fmt.Errorf("no built-in runtime for %q", lang)
	}

	cacheDir, err := runtimeCacheDir(lang)
	if err != nil {
		return "", err
	}

	// Read all files for this language from the embedded FS.
	entries, err := embedded.ReadDir(lang)
	if err != nil {
		return "", fmt.Errorf("read embedded runtime %q: %w", lang, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := lang + "/" + entry.Name()
		dstPath := filepath.Join(cacheDir, entry.Name())

		data, err := embedded.ReadFile(srcPath)
		if err != nil {
			return "", fmt.Errorf("read embedded %s: %w", srcPath, err)
		}

		// Always overwrite — the binary is the source of truth.
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return "", fmt.Errorf("write %s: %w", dstPath, err)
		}
	}

	return filepath.Join(cacheDir, "runtime.yaml"), nil
}

// ExtractFrontend writes the shared frontend.py to the runtimes cache dir
// and returns its path.
func ExtractFrontend() (string, error) {
	base, err := cachedir.Dir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	path := filepath.Join(base, "rat", "runtimes")
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", fmt.Errorf("create runtimes cache dir: %w", err)
	}
	dst := filepath.Join(path, "frontend.py")
	data, err := embedded.ReadFile("frontend.py")
	if err != nil {
		return "", fmt.Errorf("read embedded frontend.py: %w", err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", fmt.Errorf("write frontend.py: %w", err)
	}
	return dst, nil
}

func runtimeCacheDir(lang string) (string, error) {
	base, err := cachedir.Dir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	path := filepath.Join(base, "rat", "runtimes", lang)
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", fmt.Errorf("create runtime cache dir: %w", err)
	}
	return path, nil
}
