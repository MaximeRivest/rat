//go:build !windows

package procutil

import (
	"os"
	"os/exec"
	"syscall"
)

func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func Terminate(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func Kill(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func ConfigureBackgroundProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// HideWindow is a no-op on non-Windows platforms.
func HideWindow(cmd *exec.Cmd) {}

func Interrupt(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Signal(syscall.SIGINT)
}
