//go:build !windows

package procutil

import (
	"os"
	"os/exec"
	"os/signal"
	"sync"
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

// OwnProcessGroup starts cmd in a process group of its own, as Jupyter
// starts its kernels: an interrupt or a kill then reaches what the
// program started (a shell command it waits on) as Ctrl-C in a terminal
// would, and Ctrl-C meant for the host does not reach the program.
func OwnProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// InterruptGroup sends SIGINT to the process group led by proc.
func InterruptGroup(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return syscall.Kill(-proc.Pid, syscall.SIGINT)
}

// KillGroup kills the process group led by proc.
func KillGroup(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	err := syscall.Kill(-proc.Pid, syscall.SIGKILL)
	if err != nil {
		return proc.Kill()
	}
	return nil
}

var interruptsOnce sync.Once

// ChildrenReceiveInterrupts makes processes started from now on begin
// with SIGINT at its default, even when this process was started with it
// ignored (a background job of a shell is). An ignored SIGINT is
// inherited across exec, and some runtimes (R) then never install their
// interrupt handler: a cancel could not stop running code. This process
// keeps ignoring SIGINT — it is caught and dropped — but a caught signal
// is reset to its default in a child.
func ChildrenReceiveInterrupts() {
	interruptsOnce.Do(func() {
		if !signal.Ignored(os.Interrupt) {
			return
		}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		go func() {
			for range ch {
			}
		}()
	})
}
