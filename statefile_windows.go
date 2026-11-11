package gotsr

import (
	"os"
	"syscall"
	"unsafe"
)

var procMoveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

const (
	movefileReplaceExisting = 0x00000001
	movefileWriteThrough    = 0x00000008
)

func replaceStateFile(source, destination string) error {
	sourcePath, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	r1, _, err := procMoveFileEx.Call(
		uintptr(unsafe.Pointer(sourcePath)),
		uintptr(unsafe.Pointer(destinationPath)),
		movefileReplaceExisting|movefileWriteThrough,
	)
	if r1 == 0 {
		return err
	}
	return nil
}

func validateStateFilePermissions(os.FileInfo) error {
	return nil
}
