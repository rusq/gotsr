//go:build solaris || aix

package gotsr

import (
	"errors"
	"io"
	"os"
	"syscall"
)

var errStateLocked = errors.New("state is locked")

func tryLockStateFile(f *os.File) error {
	lock := syscall.Flock_t{
		Type:   syscall.F_WRLCK,
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    1,
	}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
		if err == syscall.EACCES || err == syscall.EAGAIN {
			return errStateLocked
		}
		return err
	}
	return nil
}

func unlockStateFile(f *os.File) error {
	lock := syscall.Flock_t{
		Type:   syscall.F_UNLCK,
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    1,
	}
	return syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
}
