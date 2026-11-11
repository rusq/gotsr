package gotsr

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

type stateLock struct {
	file    *os.File
	ownerID uint64
}

var localStateLocks = struct {
	sync.Mutex
	nextID uint64
	owners map[uint64]pathIdentity
}{owners: make(map[uint64]pathIdentity)}

func acquireStateLock(pidFile string) (*stateLock, error) {
	filename := pidFile + ".lock"
	localStateLocks.Lock()
	defer localStateLocks.Unlock()

	identity, err := identifyPath(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve state lock: %w", err)
	}
	for _, owner := range localStateLocks.owners {
		if samePathIdentity(identity, owner) {
			return nil, ErrAlreadyRunning
		}
	}

	f, err := openStateLockFile(identity.canonical)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	if err := tryLockStateFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errStateLocked) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock process state: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = unlockStateFile(f)
		_ = f.Close()
		return nil, fmt.Errorf("stat state lock: %w", err)
	}
	identity.info = info
	localStateLocks.nextID++
	ownerID := localStateLocks.nextID
	localStateLocks.owners[ownerID] = identity
	return &stateLock{file: f, ownerID: ownerID}, nil
}

func (l *stateLock) Close() {
	if l == nil || l.file == nil {
		return
	}
	_ = unlockStateFile(l.file)
	_ = l.file.Close()
	l.file = nil
	localStateLocks.Lock()
	delete(localStateLocks.owners, l.ownerID)
	localStateLocks.Unlock()
	l.ownerID = 0
}

// removeOwnedState is a stateLock method so cleanup cannot be called without
// the lifetime-serialization object that excludes successor publication.
func (l *stateLock) removeOwnedState(filename, secretID string) {
	if l == nil || l.file == nil {
		return
	}
	state, err := readControlState(filename)
	if err != nil || state.secretID != secretID {
		return
	}
	_ = os.Remove(filename)
}

func openStateLockFile(filename string) (*os.File, error) {
	before, err := os.Lstat(filename)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && !before.Mode().IsRegular() {
		return nil, errors.New("state lock is not a regular file")
	}

	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	after, err := os.Lstat(filename)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		_ = f.Close()
		return nil, errors.New("state lock changed while opening")
	}
	if before != nil && !os.SameFile(before, opened) {
		_ = f.Close()
		return nil, errors.New("state lock changed while opening")
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
