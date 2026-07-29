//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestAbortSidecarProcessTree(t *testing.T) {
	abortSidecarProcessTree(0)
	abortSidecarProcessTree(1)

	cmd := exec.Command("sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	abortSidecarProcessTree(cmd.Process.Pid)
	if err := cmd.Wait(); err == nil {
		t.Fatal("sidecar process survived abort")
	}
}
