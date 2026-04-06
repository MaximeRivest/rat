//go:build !windows

package python

import (
	"os/signal"
	"syscall"
)

func ignoreSuspendSignal() {
	signal.Ignore(syscall.SIGTSTP)
}
