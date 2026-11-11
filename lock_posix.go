//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// The running daemon holds an fcntl(2) write lock on the lock file
// "<pidfile>.lock" for its whole lifetime.  The kernel releases the lock when
// the process dies, even on SIGKILL, so the lock is the source of truth:
//
//   - the daemon is running only while the lock is held;
//   - F_GETLK reports the PID of the lock holder, which binds the PID to the
//     daemon instance and makes stale files harmless.
//
// The PID file is informational only, and anyone may read it, including the
// daemon itself.  The lock is kept on a separate file because closing ANY
// descriptor of a locked file in the owning process drops an fcntl lock: a
// plain os.ReadFile of the PID file in the daemon would otherwise silently
// release it.
//
// The lock file is never removed.  It is a persistent sentinel, which avoids
// the races of unlinking a locked file.
//
// Both files must be on a local filesystem; lock holder PIDs are not
// meaningful over network filesystems or across PID namespaces.
//
// A process does not see its own fcntl locks via F_GETLK, so inside the
// daemon the lock state is answered from [held], and the lock file is never
// opened again.

// claim is the lifetime identity of the running daemon.
type claim struct {
	pidPath string      // absolute path of the PID file
	lock    *os.File    // locked lock file, must stay open
	pidInfo os.FileInfo // PID file written by this daemon
}

// held is the claim owned by this process, if it is the daemon.
var held struct {
	mu sync.Mutex
	c  *claim
}

// lockPathFor returns the lock file path for the PID file.
func lockPathFor(pidFile string) string {
	return pidFile + ".lock"
}

// setHeld records the claim owned by this process.
func setHeld(c *claim) {
	held.mu.Lock()
	defer held.mu.Unlock()
	held.c = c
}

// heldPID returns the PID of this process if it holds the lock for pidFile.
func heldPID(pidFile string) (int, bool) {
	held.mu.Lock()
	defer held.mu.Unlock()
	if !isHeld(pidFile) {
		return 0, false
	}
	return os.Getpid(), true
}

// isHeld must be called with held.mu locked.
func isHeld(pidFile string) bool {
	if held.c == nil {
		return false
	}
	if held.c.pidPath == pidFile {
		return true
	}
	// different spelling of the same path?  Stat does not open the file, so
	// it does not drop the lock.
	fi, err := os.Stat(lockPathFor(pidFile))
	if err != nil {
		return false
	}
	hi, err := held.c.lock.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(fi, hi)
}

// releaseHeld removes the PID file, if it is still the one written by this
// process, and releases the lock.  It reports whether this process held a
// lock.
func releaseHeld() bool {
	held.mu.Lock()
	defer held.mu.Unlock()
	if held.c == nil {
		return false
	}
	// The PID file may have been replaced, for example by a daemon started
	// after this one lost its PID file.  Remove only our own file.
	if fi, err := os.Lstat(held.c.pidPath); err == nil && os.SameFile(fi, held.c.pidInfo) {
		_ = os.Remove(held.c.pidPath)
	}
	_ = held.c.lock.Close()
	held.c = nil
	return true
}

// releaseHeldFor releases the lock if this process holds it for pidFile.
func releaseHeldFor(pidFile string) bool {
	held.mu.Lock()
	ok := isHeld(pidFile)
	held.mu.Unlock()
	if !ok {
		return false
	}
	return releaseHeld()
}

// newFlock returns a whole-file write lock request.
func newFlock() syscall.Flock_t {
	return syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart}
}

// isLocked reports whether err means that another process holds the lock.
func isLocked(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES)
}

// acquire takes the lifetime lock for the PID file and publishes the PID
// file.  It returns ErrAlreadyRunning if another process holds the lock.
// pidFile must be absolute.
func acquire(pidFile string) (*claim, error) {
	f, err := acquireLock(lockPathFor(pidFile))
	if err != nil {
		return nil, err
	}
	fi, err := publishPID(pidFile, os.Getpid())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to write %s: %w", pidFile, err)
	}
	return &claim{pidPath: pidFile, lock: f, pidInfo: fi}, nil
}

// acquireLock opens or creates the lock file and locks it.  It returns
// ErrAlreadyRunning if another process holds the lock.
func acquireLock(lockFile string) (*os.File, error) {
	f, err := os.OpenFile(lockFile, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	lk := newFlock()
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lk); err != nil {
		f.Close()
		if isLocked(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("failed to lock %s: %w", lockFile, err)
	}
	return f, nil
}

// publishPID atomically replaces the PID file with one containing pid, and
// returns its file info.  Rename replaces a symlink at the path, it does not
// follow it.
func publishPID(pidFile string, pid int) (os.FileInfo, error) {
	tmp, err := os.CreateTemp(filepath.Dir(pidFile), filepath.Base(pidFile)+".tmp*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := fmt.Fprintf(tmp, "%d\n", pid); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	fi, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), pidFile); err != nil {
		return nil, err
	}
	return fi, nil
}

// lockHolder returns the PID of the process holding the lock for the PID
// file, or 0 if nobody holds it.
func lockHolder(pidFile string) (int, error) {
	if pid, ok := heldPID(pidFile); ok {
		return pid, nil
	}
	lockFile := lockPathFor(pidFile)
	f, err := os.OpenFile(lockFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	lk := newFlock()
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return 0, fmt.Errorf("failed to query lock on %s: %w", lockFile, err)
	}
	if lk.Type == syscall.F_UNLCK {
		return 0, nil
	}
	return int(lk.Pid), nil
}

// removeStale removes the PID file if no process holds the lock.  It takes
// the lock itself while removing, so that it never removes the file of a
// daemon that is starting up.
func removeStale(pidFile string) error {
	f, err := acquireLock(lockPathFor(pidFile))
	if errors.Is(err, ErrAlreadyRunning) {
		return nil // live daemon, leave it alone
	} else if err != nil {
		return err
	}
	defer f.Close()
	if err := os.Remove(pidFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// oneLine makes s safe to send as a single status line.
func oneLine(s string) string {
	return strings.ReplaceAll(s, "\n", " ")
}
