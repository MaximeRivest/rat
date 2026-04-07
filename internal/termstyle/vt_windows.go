//go:build windows

package termstyle

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on ENABLE_VIRTUAL_TERMINAL_PROCESSING on stdout
// so ANSI escape sequences are interpreted by the Windows console.
// Returns true if VT is now enabled.
func enableVT() bool {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if err := windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return false
	}
	return true
}
