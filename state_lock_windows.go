package gotsr

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var errStateLocked = errors.New("state is locked")

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
	procUnlockFile = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
	errorIOPending          = syscall.Errno(997)
)

func tryLockStateFile(f *os.File) error {
	overlapped := new(syscall.Overlapped)
	r1, _, err := procLockFileEx.Call(
		f.Fd(),
		lockfileExclusiveLock|lockfileFailImmediately,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if r1 != 0 {
		return nil
	}
	if err == errorLockViolation || err == errorIOPending {
		return errStateLocked
	}
	return err
}

func unlockStateFile(f *os.File) error {
	overlapped := new(syscall.Overlapped)
	r1, _, err := procUnlockFile.Call(
		f.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}
