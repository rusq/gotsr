package gotsr

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	helperModeEnv  = "GOTSR_CONTROL_HELPER"
	helperStateEnv = "GOTSR_CONTROL_STATE"
	helperReadyEnv = "GOTSR_CONTROL_READY"
	helperExitEnv  = "GOTSR_CONTROL_EXIT"
)

func TestControlHelperProcess(t *testing.T) {
	switch os.Getenv(helperModeEnv) {
	case "daemon":
		pidFile := os.Getenv(helperStateEnv)
		done := make(chan struct{})
		var once sync.Once
		runtime, err := startControl(pidFile, os.Getpid(), func() {
			once.Do(func() { close(done) })
		})
		if err != nil {
			t.Fatal(err)
		}
		<-done
		runtime.Close()
	case "sacrifice":
		if err := os.WriteFile(os.Getenv(helperReadyEnv), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
	case "tsr":
		p, err := New(WithPIDFile(os.Getenv(helperStateEnv)))
		if err != nil {
			t.Fatal(err)
		}
		p.startTimeout = 2 * time.Second
		p.AtExit(func() {
			_ = os.WriteFile(os.Getenv(helperExitEnv), []byte("exited"), 0o600)
		})
		headless, err := p.TSR()
		if err != nil {
			t.Fatal(err)
		}
		if !headless {
			return
		}
		select {}
	case "tsr_expect_running":
		pidFile := os.Getenv(helperStateEnv)
		initial := os.Getenv(newEnvVar(pidFile).stage()) == ""
		p, err := New(WithPIDFile(pidFile))
		if err != nil {
			t.Fatal(err)
		}
		p.startTimeout = 5 * time.Second
		_, err = p.TSR()
		if initial {
			if !errors.Is(err, ErrAlreadyRunning) {
				t.Fatalf("TSR() error = %v, want ErrAlreadyRunning", err)
			}
			if err := os.WriteFile(os.Getenv(helperReadyEnv), []byte("already_running"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err != nil && !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("TSR() stage error = %v, want ErrAlreadyRunning", err)
		}
	}
}

func TestAuthenticatedLifecycle(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	cmd := helperCommand("daemon", pidFile, "")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start control helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = waitCommand(cmd, time.Second)
		}
	})

	p, err := New(WithPIDFile(pidFile))
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	waitForRunning(t, p, 5*time.Second)
	if err := p.Close(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Close() while another process owns state = %v, want ErrAlreadyRunning", err)
	}
	for i := 0; i < 3; i++ {
		running, err := p.IsRunning()
		if err != nil || !running {
			t.Fatalf("IsRunning() attempt %d = %t, %v; want true, nil", i+1, running, err)
		}
	}

	original, err := readControlState(pidFile)
	if err != nil {
		t.Fatalf("read original state: %v", err)
	}
	wrongSecret, err := randomBytes(secretSize)
	if err != nil {
		t.Fatalf("generate mismatched secret: %v", err)
	}
	mismatched := original
	mismatched.secret = wrongSecret
	if err := writeControlState(pidFile, mismatched); err != nil {
		t.Fatalf("publish mismatched state: %v", err)
	}
	if err := p.Terminate(); err == nil {
		t.Fatal("Terminate() with mismatched identity succeeded")
	}
	if err := writeControlState(pidFile, original); err != nil {
		t.Fatalf("restore original state: %v", err)
	}
	if running, err := p.IsRunning(); err != nil || !running {
		t.Fatalf("daemon after mismatched termination = %t, %v; want alive", running, err)
	}

	if err := p.Terminate(); err != nil {
		t.Fatalf("Terminate(): %v", err)
	}
	if err, timedOut := waitCommand(cmd, 5*time.Second); timedOut {
		t.Fatal("control helper did not terminate")
	} else if err != nil {
		t.Fatalf("control helper exit: %v", err)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("state file after termination: %v; want not exist", err)
	}
}

func TestConcurrentControlStartHasSingleOwner(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	start := make(chan struct{})
	type result struct {
		runtime *controlRuntime
		err     error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		pid := 100 + i
		go func() {
			<-start
			runtime, err := startControl(pidFile, pid, func() {})
			results <- result{runtime: runtime, err: err}
		}()
	}
	close(start)

	var owner *controlRuntime
	var successes, locked int
	for i := 0; i < 2; i++ {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			owner = result.runtime
		case errors.Is(result.err, ErrAlreadyRunning):
			locked++
		default:
			t.Fatalf("startControl() unexpected error: %v", result.err)
		}
	}
	if successes != 1 || locked != 1 {
		if owner != nil {
			owner.Close()
		}
		t.Fatalf("concurrent starts: successes=%d locked=%d, want 1 and 1", successes, locked)
	}
	owner.Close()
}

func TestStateLockIsNonblockingAndReleasedOnClose(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	first, err := acquireStateLock(pidFile)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if second, err := acquireStateLock(pidFile); !errors.Is(err, ErrAlreadyRunning) {
		if second != nil {
			second.Close()
		}
		first.Close()
		t.Fatalf("acquire contended lock = %v, want ErrAlreadyRunning", err)
	}
	first.Close()
	third, err := acquireStateLock(pidFile)
	if err != nil {
		t.Fatalf("acquire released lock: %v", err)
	}
	third.Close()
}

func TestStateLockRecognizesSymlinkedDirectoryAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated Windows privileges")
	}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	first, err := acquireStateLock(filepath.Join(realDir, "daemon.pid"))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := acquireStateLock(filepath.Join(aliasDir, "daemon.pid")); !errors.Is(err, ErrAlreadyRunning) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("acquire symlink-directory alias = %v, want ErrAlreadyRunning", err)
	}
}

func TestStateLockRecognizesHardLinkAliasBeforeOpening(t *testing.T) {
	dir := t.TempDir()
	firstPID := filepath.Join(dir, "first.pid")
	secondPID := filepath.Join(dir, "second.pid")
	first, err := acquireStateLock(firstPID)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := os.Link(firstPID+".lock", secondPID+".lock"); err != nil {
		t.Fatal(err)
	}
	if second, err := acquireStateLock(secondPID); !errors.Is(err, ErrAlreadyRunning) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("acquire hard-link alias = %v, want ErrAlreadyRunning", err)
	}
}

func TestRuntimeOwnershipRecognizesStateAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated Windows privileges")
	}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(realDir, "daemon.pid")
	runtimeOwner, err := startControl(pidFile, 555, func() {})
	if err != nil {
		t.Fatal(err)
	}
	registerControlRuntime(runtimeOwner)
	defer runtimeOwner.Close()

	symlinkAlias := filepath.Join(aliasDir, "daemon.pid")
	if got := currentControlRuntime(symlinkAlias); got != runtimeOwner {
		t.Fatalf("currentControlRuntime(symlink alias) = %p, want %p", got, runtimeOwner)
	}
	hardLinkAlias := filepath.Join(realDir, "hardlink.pid")
	if err := os.Link(pidFile, hardLinkAlias); err != nil {
		t.Fatal(err)
	}
	if got := currentControlRuntime(hardLinkAlias); got != runtimeOwner {
		t.Fatalf("currentControlRuntime(hard-link alias) = %p, want %p", got, runtimeOwner)
	}
	if successor, err := startControl(hardLinkAlias, 556, func() {}); !errors.Is(err, ErrAlreadyRunning) {
		if successor != nil {
			successor.Close()
		}
		t.Fatalf("startControl(hard-link alias) = %v, want ErrAlreadyRunning", err)
	}
}

func TestCleanupCompletesBeforeSuccessorPublication(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	first, err := startControl(pidFile, 111, func() {})
	if err != nil {
		t.Fatalf("start first control: %v", err)
	}

	startSuccessor := make(chan struct{})
	successorResult := make(chan struct {
		runtime *controlRuntime
		err     error
	}, 1)
	go func() {
		<-startSuccessor
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			runtime, err := startControl(pidFile, 222, func() {})
			if errors.Is(err, ErrAlreadyRunning) {
				time.Sleep(time.Millisecond)
				continue
			}
			successorResult <- struct {
				runtime *controlRuntime
				err     error
			}{runtime: runtime, err: err}
			return
		}
		successorResult <- struct {
			runtime *controlRuntime
			err     error
		}{err: errors.New("timed out acquiring successor state")}
	}()
	close(startSuccessor)
	first.Close()

	result := <-successorResult
	if result.err != nil {
		t.Fatalf("start successor: %v", result.err)
	}
	defer result.runtime.Close()
	state, err := readControlState(pidFile)
	if err != nil {
		t.Fatalf("read successor state: %v", err)
	}
	if state.PID != 222 {
		t.Fatalf("state PID after overlapping cleanup/start = %d, want 222", state.PID)
	}
	if running, err := isRunning(pidFile); err != nil || !running {
		t.Fatalf("successor status = %t, %v; want running", running, err)
	}
}

func TestStaleAndMalformedStateDoNotTerminateRecordedProcess(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "sacrifice.ready")
	cmd := helperCommand("sacrifice", "", ready)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sacrificial helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = waitCommand(cmd, time.Second)
		}
	})
	waitForFile(t, ready, 5*time.Second)

	pidFile := filepath.Join(dir, "stale.pid")
	secret, err := randomBytes(secretSize)
	if err != nil {
		t.Fatal(err)
	}
	stale := controlState{
		PID:     cmd.Process.Pid,
		Network: "tcp",
		Address: "127.0.0.1:1",
		secret:  secret,
	}
	if err := writeControlState(pidFile, stale); err != nil {
		t.Fatalf("write stale state: %v", err)
	}
	p, err := New(WithPIDFile(pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Terminate(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Terminate() stale state = %v, want ErrNotRunning", err)
	}

	legacy := []byte(strconv.Itoa(cmd.Process.Pid) + "\n")
	if err := os.WriteFile(pidFile, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Terminate(); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("Terminate() legacy state = %v, want fail-closed legacy error", err)
	}

	malformed := []byte(fmt.Sprintf("%d\n{not-json}\n", cmd.Process.Pid))
	if err := os.WriteFile(pidFile, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Terminate(); err == nil {
		t.Fatal("Terminate() malformed state succeeded")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("sacrificial process was not alive after stale state checks: %v", err)
	}
	if err, timedOut := waitCommand(cmd, 5*time.Second); timedOut {
		t.Fatal("sacrificial helper did not exit after cleanup kill")
	} else if err == nil {
		t.Fatal("sacrificial helper unexpectedly exited successfully after Kill")
	}
}

func TestCrashRecoveryAfterLifetimeLockRelease(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	cmd := helperCommand("daemon", pidFile, "")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start control helper: %v", err)
	}
	p, err := New(WithPIDFile(pidFile))
	if err != nil {
		t.Fatal(err)
	}
	waitForRunning(t, p, 5*time.Second)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("crash control helper: %v", err)
	}
	if _, timedOut := waitCommand(cmd, 5*time.Second); timedOut {
		t.Fatal("crashed helper did not exit")
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("crash did not leave recoverable state: %v", err)
	}

	successor, err := startControl(pidFile, 333, func() {})
	if err != nil {
		t.Fatalf("start successor after crash: %v", err)
	}
	defer successor.Close()
	state, err := readControlState(pidFile)
	if err != nil {
		t.Fatalf("read successor state: %v", err)
	}
	if state.PID != 333 {
		t.Fatalf("successor PID = %d, want 333", state.PID)
	}
}

func TestControlStateFile(t *testing.T) {
	secret, err := randomBytes(secretSize)
	if err != nil {
		t.Fatal(err)
	}
	want := controlState{
		PID:     12345,
		Network: "tcp",
		Address: "127.0.0.1:54321",
		secret:  secret,
	}
	filename := filepath.Join(t.TempDir(), "state.pid")
	if err := writeControlState(filename, want); err != nil {
		t.Fatalf("writeControlState(): %v", err)
	}
	got, err := readControlState(filename)
	if err != nil {
		t.Fatalf("readControlState(): %v", err)
	}
	if got.PID != want.PID || got.Network != want.Network || got.Address != want.Address || !hmacSecretsEqual(got.secret, want.secret) {
		t.Fatalf("readControlState() = %#v, want PID/network/address/secret to match", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("state mode = %04o, want 0600", info.Mode().Perm())
		}
	}
}

func TestReadControlStateRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated Windows privileges")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.pid")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readControlState(link); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("readControlState(symlink) = %v, want regular-file error", err)
	}
}

func TestReadControlStateRejectsPermissiveFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not implement Unix permission bits")
	}
	secret, err := randomBytes(secretSize)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "state.pid")
	state := controlState{PID: 123, Network: "tcp", Address: "127.0.0.1:12345", secret: secret}
	if err := writeControlState(filename, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filename, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readControlState(filename); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("readControlState(permissive) = %v, want permissions error", err)
	}
}

func TestRemoveControlStateRequiresMatchingInstance(t *testing.T) {
	secret, err := randomBytes(secretSize)
	if err != nil {
		t.Fatal(err)
	}
	state := controlState{PID: 123, Network: "tcp", Address: "127.0.0.1:12345", secret: secret}
	filename := filepath.Join(t.TempDir(), "state.pid")
	if err := writeControlState(filename, state); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireStateLock(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	lock.removeOwnedState(filename, "different-instance")
	if _, err := os.Stat(filename); err != nil {
		t.Fatalf("mismatched cleanup removed state: %v", err)
	}
	lock.removeOwnedState(filename, base64Secret(secret))
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Fatalf("matching cleanup left state: %v", err)
	}
}

func TestProcessCloseDoesNotRemoveAnotherProcessState(t *testing.T) {
	secret, err := randomBytes(secretSize)
	if err != nil {
		t.Fatal(err)
	}
	state := controlState{PID: os.Getpid() + 1, Network: "tcp", Address: "127.0.0.1:12345", secret: secret}
	filename := filepath.Join(t.TempDir(), "state.pid")
	if err := writeControlState(filename, state); err != nil {
		t.Fatal(err)
	}
	p, err := New(WithPIDFile(filename))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err == nil || !strings.Contains(err.Error(), "belongs") {
		t.Fatalf("Close() = %v, want ownership error", err)
	}
	if _, err := os.Stat(filename); err != nil {
		t.Fatalf("Close() removed another process state: %v", err)
	}
}

func TestProcessCloseSerializesSuccessorPublication(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "state.pid")
	first, err := startControl(pidFile, os.Getpid(), func() {})
	if err != nil {
		t.Fatal(err)
	}
	registerControlRuntime(first)
	p, err := New(WithPIDFile(pidFile))
	if err != nil {
		t.Fatal(err)
	}

	successorResult := make(chan struct {
		runtime *controlRuntime
		err     error
	}, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			runtime, err := startControl(pidFile, 444, func() {})
			if errors.Is(err, ErrAlreadyRunning) {
				time.Sleep(time.Millisecond)
				continue
			}
			successorResult <- struct {
				runtime *controlRuntime
				err     error
			}{runtime: runtime, err: err}
			return
		}
		successorResult <- struct {
			runtime *controlRuntime
			err     error
		}{err: errors.New("timed out starting successor")}
	}()
	if err := p.Close(); err != nil {
		t.Fatalf("Close() owner: %v", err)
	}
	result := <-successorResult
	if result.err != nil {
		t.Fatalf("start successor: %v", result.err)
	}
	defer result.runtime.Close()
	state, err := readControlState(pidFile)
	if err != nil {
		t.Fatalf("read successor state: %v", err)
	}
	if state.PID != 444 {
		t.Fatalf("successor state PID = %d, want 444", state.PID)
	}
}

func helperCommand(mode, state, ready string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestControlHelperProcess$")
	cmd.Env = append(os.Environ(),
		helperModeEnv+"="+mode,
		helperStateEnv+"="+state,
		helperReadyEnv+"="+ready,
	)
	return cmd
}

func waitForRunning(t *testing.T, p *Process, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		running, err := p.IsRunning()
		if err == nil && running {
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process did not become running: %v", lastErr)
}

func waitForFile(t *testing.T, filename string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filename); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %q did not appear", filename)
}

func waitCommand(cmd *exec.Cmd, timeout time.Duration) (error, bool) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err, false
	case <-time.After(timeout):
		return nil, true
	}
}

func hmacSecretsEqual(a, b []byte) bool {
	return messageMAC(a, "test") == messageMAC(b, "test")
}

func base64Secret(secret []byte) string {
	return base64.RawURLEncoding.EncodeToString(secret)
}
