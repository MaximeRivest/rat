//go:build windows

package state

import (
	"os"

	"golang.org/x/sys/windows"
)

func acquireFileLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func releaseFileLock(f *os.File) error {
	if f == nil {
		return nil
	}
	var overlapped windows.Overlapped
	unlockErr := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
