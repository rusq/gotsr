//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicTSRLifecycle(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "public.pid")
	exitMarker := filepath.Join(dir, "atexit")

	startTSRHelper(t, "tsr", pidFile, exitMarker, "", true)
	p, err := New(WithPIDFile(pidFile))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() {
		if running, _ := p.IsRunning(); running {
			_ = p.Terminate()
			waitForMissingFileBestEffort(pidFile, time.Second)
		}
	})
	for i := 0; i < 3; i++ {
		running, err := p.IsRunning()
		if err != nil || !running {
			t.Fatalf("IsRunning() attempt %d = %t, %v; want true, nil", i+1, running, err)
		}
	}

	// A direct repeated TSR attempt reaches the running stage but cannot acquire
	// the first instance's lifetime lock. Its structured startup result must
	// preserve ErrAlreadyRunning rather than degrading into startup timeout.
	repeatedEvidence := filepath.Join(dir, "repeated-result")
	startTSRHelper(t, "tsr_expect_running", pidFile, exitMarker, repeatedEvidence, true)
	data, err := os.ReadFile(repeatedEvidence)
	if err != nil || string(data) != "already_running" {
		t.Fatalf("repeated TSR evidence = %q, %v", data, err)
	}
	if running, err := p.IsRunning(); err != nil || !running {
		t.Fatalf("daemon after repeated start = %t, %v; want alive", running, err)
	}

	if err := p.Terminate(); err != nil {
		t.Fatalf("Terminate(): %v", err)
	}
	waitForFile(t, exitMarker, 5*time.Second)
	waitForMissingFile(t, pidFile, 5*time.Second)
	if running, err := p.IsRunning(); err != nil || running {
		t.Fatalf("IsRunning() after termination = %t, %v; want false, nil", running, err)
	}
}

func startTSRHelper(t *testing.T, mode, pidFile, exitMarker, ready string, wantSuccess bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlHelperProcess$")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"="+mode,
		helperStateEnv+"="+pidFile,
		helperExitEnv+"="+exitMarker,
		helperReadyEnv+"="+ready,
	)
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("TSR helper timed out: %v", ctx.Err())
	}
	if wantSuccess && err != nil {
		t.Fatalf("start TSR helper: %v", err)
	}
	if !wantSuccess && err == nil {
		t.Fatal("repeated TSR helper unexpectedly succeeded")
	}
}

func waitForMissingFile(t *testing.T, filename string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %q still exists", filename)
}

func waitForMissingFileBestEffort(filename string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filename); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
