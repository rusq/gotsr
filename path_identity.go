package gotsr

import (
	"os"
	"path/filepath"
)

type pathIdentity struct {
	canonical string
	info      os.FileInfo
}

func identifyPath(filename string) (pathIdentity, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return pathIdentity{}, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return pathIdentity{}, err
	}
	canonical := filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(canonical)
	if err != nil {
		if os.IsNotExist(err) {
			return pathIdentity{canonical: canonical}, nil
		}
		return pathIdentity{}, err
	}
	return pathIdentity{canonical: canonical, info: info}, nil
}

func samePathIdentity(a, b pathIdentity) bool {
	if a.canonical == b.canonical {
		return true
	}
	return a.info != nil && b.info != nil && os.SameFile(a.info, b.info)
}
