//go:build windows

package main

import "os"

func abortSidecarProcessTree(pid int) {
	if pid <= 1 {
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}
