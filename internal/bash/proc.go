//go:build !windows

package bash

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

func processWaitingForInput(pid int, fallbackPrompt string) (bool, string) {
	if runtime.GOOS != "linux" {
		return false, fallbackPrompt
	}
	pids := append([]int{pid}, descendantPIDs(pid)...)
	unknown := false
	for _, current := range pids {
		result, ok := processWaitingForStdin(current)
		if !ok {
			unknown = true
			continue
		}
		if result {
			return true, fallbackPrompt
		}
	}
	if unknown {
		return false, fallbackPrompt
	}
	return false, fallbackPrompt
}

func descendantPIDs(pid int) []int {
	childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
	data, err := os.ReadFile(childrenPath)
	if err != nil {
		return []int{}
	}
	fields := strings.Fields(string(data))
	children := make([]int, 0, len(fields))
	for _, field := range fields {
		child, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		children = append(children, child)
		children = append(children, descendantPIDs(child)...)
	}
	return children
}

func processWaitingForStdin(pid int) (bool, bool) {
	path := fmt.Sprintf("/proc/%d/syscall", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	content := strings.TrimSpace(string(data))
	if content == "running" {
		return false, true
	}
	parts := strings.Fields(content)
	if len(parts) < 2 {
		return false, false
	}
	syscallNum, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, false
	}
	if syscallNum != 0 {
		return false, true
	}
	fd, err := strconv.ParseInt(parts[1], 0, 64)
	if err != nil {
		return false, false
	}
	return fd == 0, true
}
