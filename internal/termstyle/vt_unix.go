//go:build !windows

package termstyle

func enableVT() bool { return true }
