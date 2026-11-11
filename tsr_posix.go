//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || aix

package gotsr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var (
	errInvalidStage = errors.New("invalid stage")
	errTimeout      = errors.New("stage 1 process timeout")
	errExited       = errors.New("process exited during startup")
)

const (
	// statusFD is the file descriptor of the startup status pipe in the
	// detached processes (first entry of exec.Cmd.ExtraFiles).
	statusFD = 3
	// stopTimeout is how long terminate waits for the process to exit.
	stopTimeout = 30 * time.Second
	// stopPoll is how often terminate checks whether the process has exited.
	stopPoll = 50 * time.Millisecond
)

// tsr is the main function that starts the program in the detached mode.
func tsr(pidFile string, timeout time.Duration, atExit ...func()) (bool, error) {
	stg, err := summon(pidFile, timeout, atExit...)
	return stg == sRunning, err
}

// summon is the posix-specific function that starts the program in the
// detached mode.
//
// It does it in three stages:
//  1. Initialisation: starts a new process with the same arguments and
//     environment, but with STDIN, STDOUT and STDERR disconnected, and SetSid
//     set to true to create a new session (thanks to this advice:
//     https://stackoverflow.com/a/46799048/1169992)
//  2. Detach: restarts the process further detached from the terminal.
//  3. Running: the program is running in the background.
//
// It identifies the current stage by reading the STAGE environment variable.
// The running process reports the startup result to the initial process over
// a pipe inherited through both stages.
func summon(pidFile string, timeout time.Duration, atExit ...func()) (stage, error) {
	image, err := os.Executable()
	if err != nil {
		return sUnknown, err
	}

	vars := newEnvVar(pidFile) // initialise environment variable base name from pidFile.
	stage := os.Getenv(vars.stage())
	switch stage {
	default:
		return sUnknown, errInvalidStage
	case "": // initial setup and preparing for detachment
		return sInitialise, stageInit(pidFile, vars, image, timeout)
	case sDetach.String(): // releasing handles, clean start
		return sDetach, stageDetach(vars, image)
	case sRunning.String(): // running TSR program
		return sRunning, stageRun(pidFile, vars, atExit)
	}
	// unreachable
}

// stageInit is the first stage that starts a new detached instance of the
// program in a new session, and waits for it to report the startup result.
func stageInit(pidFile string, vars envVar, image string, timeout time.Duration) error {
	if pid, err := lockHolder(pidFile); err != nil {
		return err
	} else if pid > 0 {
		return ErrAlreadyRunning
	}

	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create status pipe: %w", err)
	}
	defer r.Close()

	cmd := exec.Command(image, os.Args[1:]...)
	cmd.Env = withEnv(os.Environ(), vars.stage(), sDetach.String())
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err = cmd.Start()
	w.Close() // only the children hold the write end now.
	if err != nil {
		return fmt.Errorf("failed to initialise the process: %w", err)
	}
	go cmd.Wait() // reap the detach stage process.

	if err := r.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("failed to set status timeout: %w", err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	return parseStatus(line, err)
}

// parseStatus interprets the startup status line sent by stageRun.
func parseStatus(line string, readErr error) error {
	if readErr != nil {
		switch {
		case errors.Is(readErr, os.ErrDeadlineExceeded):
			return errTimeout
		case errors.Is(readErr, io.EOF):
			return errExited
		default:
			return fmt.Errorf("failed to read startup status: %w", readErr)
		}
	}
	line = strings.TrimSuffix(line, "\n")
	switch {
	case strings.HasPrefix(line, "OK "):
		lg.Printf("process started with PID: %s", strings.TrimPrefix(line, "OK "))
		return nil
	case line == "ERR "+ErrAlreadyRunning.Error():
		return ErrAlreadyRunning
	case strings.HasPrefix(line, "ERR "):
		return fmt.Errorf("process failed to start: %s", strings.TrimPrefix(line, "ERR "))
	default:
		return fmt.Errorf("invalid startup status: %q", line)
	}
}

// stageDetach starts a new process with the same arguments and environment,
// passing the status pipe through.
func stageDetach(vars envVar, image string) error {
	status := os.NewFile(statusFD, "tsr-status")
	defer status.Close()

	cmd := exec.Command(image, os.Args[1:]...)
	cmd.Env = withEnv(os.Environ(), vars.stage(), sRunning.String())
	cmd.ExtraFiles = []*os.File{status}

	return cmd.Start()
}

// stageRun runs the main program.
func stageRun(pidFile string, vars envVar, atExit []func()) error {
	// unset the environment variable, so that it does not leak into the
	// processes started by the program.
	os.Unsetenv(vars.stage())

	status := os.NewFile(statusFD, "tsr-status")
	report := func(msg string) {
		fmt.Fprintln(status, oneLine(msg))
		status.Close()
	}

	c, err := acquire(pidFile)
	if err != nil {
		report("ERR " + err.Error())
		return err
	}
	setHeld(c)

	// Register before reporting success, the channel buffers a signal that
	// arrives early.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)

	report(fmt.Sprintf("OK %d", os.Getpid()))

	// Started only after the report, so that the process can not exit before
	// the parent is told that it has started.
	go func() {
		<-quit
		for _, fn := range atExit {
			fn()
		}
		releaseHeld()
		os.Exit(0)
	}()
	return nil
}

// withEnv returns env with the variable key set to value.
func withEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}

// isRunning reports whether a process holds the lock on the PID file.  It
// does not send any signals.
func isRunning(pidFile string) (bool, error) {
	pid, err := lockHolder(pidFile)
	if err != nil {
		return false, err
	}
	return pid > 0, nil
}

// statePID returns the PID of the process holding the lock on the PID file.
func statePID(pidFile string) (int, error) {
	pid, err := lockHolder(pidFile)
	if err != nil {
		return 0, err
	}
	if pid == 0 {
		return 0, ErrNotRunning
	}
	return pid, nil
}

// closeState removes the PID file.  The daemon releases its lock and removes
// the PID file if it is still its own.  Any other process removes the PID file
// only if no process holds the lock.
func closeState(pidFile string) error {
	if releaseHeldFor(pidFile) {
		return nil
	}
	return removeStale(pidFile)
}

// terminate sends SIGTERM to the process holding the lock on the PID file
// and waits for it to exit.  A PID file without a lock holder is stale, and
// its PID is never signalled.
func terminate(pidFile string) error {
	pid, err := lockHolder(pidFile)
	if err != nil {
		return err
	}
	if pid == 0 {
		return ErrNotRunning
	}
	if pid == os.Getpid() {
		// the daemon terminates itself, it can not wait for its own exit.
		return syscall.Kill(pid, syscall.SIGTERM)
	}

	// On Linux, FindProcess holds a pidfd, so the signal below reaches the
	// process found here, or nobody.  Confirming that this process still
	// holds the lock after obtaining the handle therefore rules out PID
	// reuse.  Elsewhere, a tiny window between the check and the signal
	// remains.
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	if again, err := lockHolder(pidFile); err != nil {
		return err
	} else if again != pid {
		return ErrNotRunning
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return ErrNotRunning
		}
		return fmt.Errorf("failed to signal process %d: %w", pid, err)
	}
	if err := waitUnlocked(pidFile, pid, stopTimeout); err != nil {
		return err
	}
	lg.Printf("process %d terminated", pid)
	return nil
}

// waitUnlocked waits until the process pid no longer holds the lock on the
// PID file.
func waitUnlocked(pidFile string, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		holder, err := lockHolder(pidFile)
		if err != nil {
			return err
		}
		if holder != pid {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for process %d to exit", pid)
		}
		time.Sleep(stopPoll)
	}
}
