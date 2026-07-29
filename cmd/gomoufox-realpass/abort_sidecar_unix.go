//go:build !windows

package main

import (
	"syscall"
	"time"
)

func abortSidecarProcessTree(pid int) {
	if pid <= 1 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(250 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
