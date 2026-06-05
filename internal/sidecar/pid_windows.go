//go:build windows

package sidecar

func processExists(pid int) bool {
	return pid > 0
}

func terminatePID(pid int) error {
	return nil
}
