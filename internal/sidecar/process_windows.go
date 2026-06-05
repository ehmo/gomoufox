//go:build windows

package sidecar

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsJobs = struct {
	sync.Mutex
	byPID map[int]windows.Handle
}{byPID: map[int]windows.Handle{}}

func setProcessGroup(cmd *exec.Cmd) {}

func assignProcessBoundary(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	windowsJobs.Lock()
	windowsJobs.byPID[cmd.Process.Pid] = job
	windowsJobs.Unlock()
	return nil
}

func releaseProcessBoundary(cmd *exec.Cmd) {
	job := takeWindowsJob(cmd)
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
}

func terminateProcessTree(cmd *exec.Cmd) {
	job := takeWindowsJob(cmd)
	if job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killProcessTree(cmd *exec.Cmd) {
	terminateProcessTree(cmd)
}

func takeWindowsJob(cmd *exec.Cmd) windows.Handle {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	windowsJobs.Lock()
	defer windowsJobs.Unlock()
	job := windowsJobs.byPID[cmd.Process.Pid]
	delete(windowsJobs.byPID, cmd.Process.Pid)
	return job
}

func windowsJobCountForTest() int {
	windowsJobs.Lock()
	defer windowsJobs.Unlock()
	return len(windowsJobs.byPID)
}

func windowsRememberJobForTest(pid int, job windows.Handle) {
	windowsJobs.Lock()
	defer windowsJobs.Unlock()
	windowsJobs.byPID[pid] = job
}

func windowsForgetJobsForTest() {
	windowsJobs.Lock()
	defer windowsJobs.Unlock()
	for pid, job := range windowsJobs.byPID {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
		delete(windowsJobs.byPID, pid)
	}
}
