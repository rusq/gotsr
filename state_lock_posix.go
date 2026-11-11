//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package gotsr

import (
	"errors"
	"os"
	"syscall"
)

var errStateLocked = errors.New("state is locked")

func tryLockStateFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return errStateLocked
		}
		return err
	}
	return nil
}

func unlockStateFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
