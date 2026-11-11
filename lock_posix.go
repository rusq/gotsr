//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
)

// The running daemon holds an fcntl(2) write lock on the PID file for its
// whole lifetime.  The kernel releases the lock when the process dies, even
// on SIGKILL, so the lock, not the file contents, is the source of truth:
//
//   - the PID file is "live" only while the lock is held;
//   - F_GETLK reports the PID of the lock holder, which binds the PID to the
//     daemon instance and makes stale PID files harmless.
//
// The PID file must be on a local filesystem; lock holder PIDs are not
// meaningful over network filesystems or across PID namespaces.
//
// Two POSIX quirks shape this code:
//
//   - a process does not see its own fcntl locks via F_GETLK;
//   - closing ANY descriptor of the locked file in the owning process drops
//     the lock.
//
// Therefore, inside the daemon the lock state is answered from [held], and
// the PID file is never opened again.

// held is the lock owned by this process, if it is the daemon.
var held struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// setHeld records the lock owned by this process.
func setHeld(path string, f *os.File) {
	held.mu.Lock()
	defer held.mu.Unlock()
	held.path = path
	held.f = f
}

// heldPID returns the PID of this process if it holds the lock on path.
func heldPID(path string) (int, bool) {
	held.mu.Lock()
	defer held.mu.Unlock()
	if !isHeld(path) {
		return 0, false
	}
	return os.Getpid(), true
}

// releaseHeld removes the PID file and releases the lock, if this process
// holds the lock on path.  It reports whether the lock was held.
func releaseHeld(path string) bool {
	held.mu.Lock()
	defer held.mu.Unlock()
	if !isHeld(path) {
		return false
	}
	// remove before closing, so that the name never points to an unlocked
	// file that we own.
	_ = os.Remove(held.path)
	_ = held.f.Close()
	held.path = ""
	held.f = nil
	return true
}

// isHeld must be called with held.mu locked.
func isHeld(path string) bool {
	if held.f == nil {
		return false
	}
	if held.path == path {
		return true
	}
	// different spelling of the same path?  Stat does not open the file, so
	// it does not drop the lock.
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	hi, err := held.f.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(fi, hi)
}

// newFlock returns a whole-file write lock request.
func newFlock() syscall.Flock_t {
	return syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart}
}

// acquireLock opens or creates the PID file, locks it and writes the current
// PID into it.  It returns ErrAlreadyRunning if another process holds the
// lock.  The returned file must stay open for as long as the lock is needed.
func acquireLock(path string) (*os.File, error) {
	const attempts = 3
	for range attempts {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, err
		}
		lk := newFlock()
		if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk); err != nil {
			f.Close()
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
				return nil, ErrAlreadyRunning
			}
			return nil, fmt.Errorf("failed to lock %s: %w", path, err)
		}
		// The previous owner may have unlinked the file between our open and
		// lock.  Make sure that we have locked the file that the path names.
		if !sameFile(f, path) {
			f.Close()
			continue
		}
		if err := writeLocked(f, os.Getpid()); err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to write %s: %w", path, err)
		}
		return f, nil
	}
	return nil, fmt.Errorf("failed to lock %s: file keeps changing", path)
}

// sameFile reports whether f is the file that path names.
func sameFile(f *os.File, path string) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	li, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return os.SameFile(fi, li)
}

// writeLocked replaces the contents of f with the PID.
func writeLocked(f *os.File, pid int) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt(fmt.Appendf(nil, "%d\n", pid), 0); err != nil {
		return err
	}
	return f.Sync()
}

// lockHolder returns the PID of the process holding the lock on the PID file,
// or 0 if the file does not exist or nobody holds the lock.
func lockHolder(path string) (int, error) {
	if pid, ok := heldPID(path); ok {
		return pid, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	lk := newFlock()
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return 0, fmt.Errorf("failed to query lock on %s: %w", path, err)
	}
	if lk.Type == syscall.F_UNLCK {
		return 0, nil
	}
	return int(lk.Pid), nil
}

// removeStale removes the PID file if no process holds the lock on it.  It
// takes the lock itself while removing, so that it never removes the file of
// a daemon that is starting up.
func removeStale(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	lk := newFlock()
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk); err != nil {
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES) {
			return nil // live daemon, leave it alone
		}
		return fmt.Errorf("failed to lock %s: %w", path, err)
	}
	if !sameFile(f, path) {
		return nil // replaced in the meantime
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// oneLine makes s safe to send as a single status line.
func oneLine(s string) string {
	return strings.ReplaceAll(s, "\n", " ")
}
