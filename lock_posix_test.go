//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func Test_lockHolder_unlocked(t *testing.T) {
	sacrifice := startSacrifice(t)
	livePID := strconv.Itoa(sacrifice.Process.Pid)
	tests := []struct {
		name     string
		contents *string // nil: file does not exist
	}{
		{"missing file", nil},
		{"stale file with a live PID", &livePID},
		{"malformed file", new("garbage\n")},
		{"empty file", new("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.pid")
			if tt.contents != nil {
				if err := os.WriteFile(path, []byte(*tt.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			pid, err := lockHolder(path)
			if err != nil {
				t.Fatalf("lockHolder() error = %v", err)
			}
			if pid != 0 {
				t.Errorf("lockHolder() = %d, want 0", pid)
			}
		})
	}
	if !alive(sacrifice.Process.Pid) {
		t.Error("unrelated process was killed")
	}
}

func Test_lockHolder_locked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pid")
	holder := holdLock(t, path)

	pid, err := lockHolder(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != holder.Process.Pid {
		t.Errorf("lockHolder() = %d, want %d", pid, holder.Process.Pid)
	}
	got, err := readPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != holder.Process.Pid {
		t.Errorf("PID file contains %d, want %d", got, holder.Process.Pid)
	}
}

func Test_acquireLock(t *testing.T) {
	t.Run("held by another process", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.pid")
		holdLock(t, path)
		f, err := acquireLock(path)
		if f != nil {
			f.Close()
		}
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("acquireLock() error = %v, want %v", err, ErrAlreadyRunning)
		}
	})
	t.Run("stale file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.pid")
		if err := os.WriteFile(path, []byte("1\nleftover data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := acquireLock(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if want := strconv.Itoa(os.Getpid()) + "\n"; string(data) != want {
			t.Errorf("PID file = %q, want %q", data, want)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "test.pid")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		f, err := acquireLock(link)
		if err == nil {
			f.Close()
			t.Fatal("acquireLock() followed a symlink")
		}
		if data, _ := os.ReadFile(target); string(data) != "precious" {
			t.Errorf("symlink target was modified: %q", data)
		}
	})
}

func Test_removeStale(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.pid")
		if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := removeStale(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale PID file was not removed: %v", err)
		}
	})
	t.Run("live", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "test.pid")
		holder := holdLock(t, path)
		if err := removeStale(path); err != nil {
			t.Fatal(err)
		}
		if pid, err := lockHolder(path); err != nil || pid != holder.Process.Pid {
			t.Errorf("live PID file was disturbed: holder=%d, err=%v", pid, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if err := removeStale(filepath.Join(t.TempDir(), "test.pid")); err != nil {
			t.Fatal(err)
		}
	})
}

func Test_parseStatus(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		readErr error
		wantErr error // nil: no error; errAny: any error
	}{
		{"ok", "OK 123\n", nil, nil},
		{"already running", "ERR already running\n", nil, ErrAlreadyRunning},
		{"other error", "ERR permission denied\n", nil, errAny},
		{"garbage", "hello\n", nil, errAny},
		{"exited", "", io.EOF, errExited},
		{"partial line", "OK 1", io.EOF, errExited},
		{"timeout", "", os.ErrDeadlineExceeded, errTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseStatus(tt.line, tt.readErr)
			switch {
			case tt.wantErr == nil && err != nil:
				t.Errorf("parseStatus() error = %v, want nil", err)
			case tt.wantErr == errAny && err == nil:
				t.Error("parseStatus() error = nil, want error")
			case tt.wantErr != nil && tt.wantErr != errAny && !errors.Is(err, tt.wantErr):
				t.Errorf("parseStatus() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func Test_withEnv(t *testing.T) {
	got := withEnv([]string{"A=1", "TSR_X__STG=DETACH", "TSR_X__STGX=2"}, "TSR_X__STG", "RUN")
	want := []string{"A=1", "TSR_X__STGX=2", "TSR_X__STG=RUN"}
	if len(got) != len(want) {
		t.Fatalf("withEnv() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("withEnv()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

var errAny = errors.New("any error")
