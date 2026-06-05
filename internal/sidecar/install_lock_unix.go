//go:build !windows

package sidecar

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryInstallFileLock(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errInstallLockBusy
	}
	return err
}

func unlockInstallFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
