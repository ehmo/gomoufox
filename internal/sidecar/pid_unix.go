//go:build !windows

package sidecar

import "syscall"

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func terminatePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
