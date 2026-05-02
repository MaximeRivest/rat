// Package cachedir provides a single canonical cache directory for rat.
//
// os.UserCacheDir() respects XDG_CACHE_HOME, which snap/flatpak
// containers redirect to a sandbox-specific location.  This causes
// the kernel daemon (started from one context) and the frontend
// (started from another) to disagree on file paths.
//
// This package always resolves to $HOME/.cache on Unix, which is
// stable across snap, flatpak, tmux, ssh, and regular terminals.
// On Windows it delegates to os.UserCacheDir().
package cachedir

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/maximerivest/rat/internal/runtimeid"
)

// Dir returns the user cache directory, ignoring container-scoped
// overrides like XDG_CACHE_HOME set by snap or flatpak.
func Dir() (string, error) {
	if runtime.GOOS == "windows" {
		return os.UserCacheDir()
	}
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(home, ".cache"), nil
}

// Rat returns $HOME/.cache/rat (or equivalent), creating it if needed.
func Rat() (string, error) {
	base, err := Dir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(base, "rat")
	return p, os.MkdirAll(p, 0o700)
}

// Kernels returns $HOME/.cache/rat/kernels/<name>, creating it if needed.
func Kernels(name string) (string, error) {
	if err := runtimeid.ValidateName(name); err != nil {
		return "", err
	}
	base, err := Dir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(base, "rat", "kernels", name)
	return p, os.MkdirAll(p, 0o700)
}
