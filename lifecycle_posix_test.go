//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	p, err := New(WithPIDFile(path))
	if err != nil {
		t.Fatal(err)
	}

	if code := startDaemon(t, path); code != exitOK {
		t.Fatalf("start: exit code %d", code)
	}
	pid, err := p.PID()
	if err != nil {
		t.Fatalf("PID() error = %v", err)
	}

	// Regression: IsRunning used to send SIGUSR2 to the recorded PID.  The
	// helper daemon exits if it receives SIGUSR1/2.
	for range 3 {
		running, err := p.IsRunning()
		if err != nil || !running {
			t.Fatalf("IsRunning() = %v, %v; want true", running, err)
		}
	}
	if !alive(pid) {
		t.Fatal("daemon died after IsRunning")
	}

	// A second instance must not start, and must not disturb the first.
	if code := startDaemon(t, path); code != exitAlreadyRunning {
		t.Errorf("second start: exit code %d, want %d", code, exitAlreadyRunning)
	}
	if again, err := p.PID(); err != nil || again != pid {
		t.Errorf("PID() after second start = %d, %v; want %d", again, err, pid)
	}

	// Close from a foreign process must not remove a live PID file.
	if err := p.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Close() removed a live PID file: %v", err)
	}

	// Terminate returns only once the daemon is gone and has cleaned up.
	if err := p.Terminate(); err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if running, err := p.IsRunning(); err != nil || running {
		t.Errorf("IsRunning() after Terminate = %v, %v; want false", running, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("PID file was not removed: %v", err)
	}
	if err := p.Terminate(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("second Terminate() error = %v, want %v", err, ErrNotRunning)
	}

	// Restart right away works.
	if code := startDaemon(t, path); code != exitOK {
		t.Fatalf("restart: exit code %d", code)
	}
	if err := p.Terminate(); err != nil {
		t.Fatalf("Terminate() after restart error = %v", err)
	}
}

// Regression: a stale PID file must never cause the recorded PID to be
// signalled.
func TestStalePIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	sacrifice := startSacrifice(t)
	if err := os.WriteFile(path, []byte(strconv.Itoa(sacrifice.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := New(WithPIDFile(path))
	if err != nil {
		t.Fatal(err)
	}

	if running, err := p.IsRunning(); err != nil || running {
		t.Errorf("IsRunning() = %v, %v; want false", running, err)
	}
	if _, err := p.PID(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("PID() error = %v, want %v", err, ErrNotRunning)
	}
	if err := p.Terminate(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Terminate() error = %v, want %v", err, ErrNotRunning)
	}
	if !alive(sacrifice.Process.Pid) {
		t.Fatal("unrelated process was signalled")
	}

	// The daemon starts over a stale file.
	if code := startDaemon(t, path); code != exitOK {
		t.Fatalf("start over stale file: exit code %d", code)
	}
	if err := p.Terminate(); err != nil {
		t.Fatal(err)
	}
	if !alive(sacrifice.Process.Pid) {
		t.Fatal("unrelated process was signalled")
	}
}

// After the daemon is killed without cleanup, a new one can start.
func TestRestartAfterKill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	p, err := New(WithPIDFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if code := startDaemon(t, path); code != exitOK {
		t.Fatalf("start: exit code %d", code)
	}
	pid, err := p.PID()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := waitUnlocked(path, pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected a stale PID file to remain: %v", err)
	}
	if running, err := p.IsRunning(); err != nil || running {
		t.Errorf("IsRunning() = %v, %v; want false", running, err)
	}
	if code := startDaemon(t, path); code != exitOK {
		t.Fatalf("restart: exit code %d", code)
	}
	if err := p.Terminate(); err != nil {
		t.Fatal(err)
	}
}
