//go:build !windows

package gotsr

import (
	"errors"
	"os"
)

func replaceStateFile(source, destination string) error {
	return os.Rename(source, destination)
}

func validateStateFilePermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("state file permissions are not private")
	}
	return nil
}
