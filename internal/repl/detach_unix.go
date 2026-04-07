//go:build !windows

package repl

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func setupDetachSignal(lang string) {
	szCh := make(chan os.Signal, 1)
	signal.Notify(szCh, syscall.SIGTSTP)
	go func() {
		<-szCh
		fmt.Printf("\nDetached. Kernel still running. Reconnect: rat %s\n", lang)
		os.Exit(0)
	}()
}
