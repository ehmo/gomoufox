//go:build windows

package sidecar

import (
	"os"
	"os/exec"
	"testing"
)

func TestWindowsJobRegistryHelpers(t *testing.T) {
	windowsForgetJobsForTest()
	t.Cleanup(windowsForgetJobsForTest)
	if windowsJobCountForTest() != 0 {
		t.Fatalf("initial job count = %d", windowsJobCountForTest())
	}
	windowsRememberJobForTest(123, 0)
	if windowsJobCountForTest() != 1 {
		t.Fatalf("remembered job count = %d", windowsJobCountForTest())
	}
	cmd := &exec.Cmd{Process: &os.Process{Pid: 123}}
	if got := takeWindowsJob(cmd); got != 0 {
		t.Fatalf("job handle = %v", got)
	}
	if windowsJobCountForTest() != 0 {
		t.Fatalf("final job count = %d", windowsJobCountForTest())
	}
	releaseProcessBoundary(&exec.Cmd{})
}
