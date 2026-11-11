//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests re-execute the test binary as a helper process.  These variables
// select the helper mode and the PID file.
const (
	helperModeEnv = "GOTSR_TEST_HELPER"
	helperPathEnv = "GOTSR_TEST_PIDFILE"

	modeDaemon = "daemon" // run TSR and stay resident
	modeLock   = "lock"   // hold the lock in the foreground
)

// exit codes of the helper process.
const (
	exitOK             = 0
	exitError          = 1
	exitAlreadyRunning = 3
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(runHelper(mode, os.Getenv(helperPathEnv)))
	}
	os.Exit(m.Run())
}

func runHelper(mode, path string) int {
	switch mode {
	case modeDaemon:
		p, err := New(WithPIDFile(path))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitError
		}
		headless, err := p.TSR()
		if errors.Is(err, ErrAlreadyRunning) {
			return exitAlreadyRunning
		} else if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitError
		}
		if headless {
			// Go ignores SIGUSR1/2 by default, but programs commonly use them
			// (e.g. log rotation).  The library must never send them, so
			// the helper dies if it receives one.
			usr := make(chan os.Signal, 1)
			signal.Notify(usr, syscall.SIGUSR1, syscall.SIGUSR2)
			<-usr
			return exitError
		}
		return exitOK
	case modeLock:
		if _, err := acquireLock(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitError
		}
		fmt.Println("locked")
		for {
			time.Sleep(time.Hour)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		return exitError
	}
}

func helperCmd(t *testing.T, mode, path string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode, helperPathEnv+"="+path)
	return cmd
}

// startDaemon starts a TSR daemon using path as the PID file and returns the
// exit code of the initial process.  The daemon is killed when the test ends.
func startDaemon(t *testing.T, path string) int {
	t.Helper()
	cmd := helperCmd(t, modeDaemon, path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	t.Cleanup(func() { killHolder(t, path) })
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if stderr.Len() > 0 {
			t.Logf("daemon stderr: %s", stderr.String())
		}
		return exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return exitOK
}

// holdLock starts a foreground helper process that holds the lock on path.
func holdLock(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := helperCmd(t, modeLock, path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock helper: %q, %v", line, err)
	}
	return cmd
}

// killHolder kills whatever process holds the lock on path, and waits for
// the lock to be released.
func killHolder(t *testing.T, path string) {
	t.Helper()
	pid, err := lockHolder(path)
	if err != nil || pid == 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if err := waitUnlocked(path, pid, 10*time.Second); err != nil {
		t.Error(err)
	}
}

// startSacrifice starts an unrelated process that must survive the test.
func startSacrifice(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
