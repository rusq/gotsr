package gotsr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

const (
	startTimeout = 2 * time.Second
)

var (
	errInvalidStage = errors.New("invalid stage")
	errTimeout      = errors.New("stage 1 process timeout")
)

// stage is the initialisation stage of the program.
//
//go:generate stringer -type stage -linecomment
type stage int8

const (
	sUnknown    stage = -1 + iota // UNKNOWN
	sInitialise                   // INIT
	sDetach                       // DETACH
	sRunning                      // RUN
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
// program in a new session.
func stageInit(pidFile string, vars envVar, image string, timeout time.Duration) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)

	os.Setenv(vars.stage(), sDetach.String())
	os.Setenv(vars.pid(), strconv.Itoa(os.Getpid()))

	cmd := exec.Command(image, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = nil
	cmd.Stdout = nil
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to initialise the process: %s", err)
	}
	timer := time.After(timeout)
	select {
	case <-sig:
		pid, err := readPID(pidFile)
		if err != nil {
			lg.Printf("process started, but PID file is missing: %s", err)
		} else if pid == 0 {
			lg.Println("warning: process started, but PID is 0")
		} else {
			lg.Printf("process started with PID: %d", pid)
		}
	case <-timer:
		return errTimeout
	}
	return nil
}

// stageDetach starts a new process with the same arguments and environment.
func stageDetach(vars envVar, image string) error {
	os.Setenv(vars.stage(), sRunning.String())

	cmd := exec.Command(image, os.Args[1:]...)

	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	return cmd.Start()
}

// stageRun runs the main program.
func stageRun(pidFile string, vars envVar, atExit []func()) error {
	pid := os.Getpid()
	if err := writePID(pidFile, pid); err != nil {
		return err
	}

	_ = notifySuccess(vars)
	// unset the environment variables once the program is running.
	for _, envVar := range []string{vars.stage(), vars.pid()} {
		os.Unsetenv(envVar)
	}

	quit := make(chan os.Signal, 1)
	go func() {
		<-quit
		for _, fn := range atExit {
			fn()
		}
		os.Remove(pidFile)
		os.Exit(0)
	}()
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)
	return nil
}

// notifySuccess notifies the parent process that the program has started.
func notifySuccess(vars envVar) error {
	sPID := os.Getenv(vars.pid())
	if pid, err := strconv.Atoi(sPID); err != nil {
		return fmt.Errorf("invalid pid value: %q, error: %w", sPID, err)
	} else {
		p, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("parent process not found: %d: %w", p, err)
		}
		if err := p.Signal(syscall.SIGUSR1); err != nil {
			return fmt.Errorf("failed to notify parent with PID=%d: %w", pid, err)
		}
	}
	return nil
}

// isRunning checks if the process with the given PID is running.
func isRunning(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.SIGUSR2); err != nil {
		return false
	}
	return true
}

// terminate sends a SIGTERM signal to the process with the given PID.
func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

// envVar is a unique identifier for the environment variables used by TSR.
type envVar string

// newEnvVar returns a new unique identifier for the environment variables.
// It is calculated as the first 7 characters of the SHA1 hash of the given
// string.
func newEnvVar(s string) envVar {
	return envVar(hash(s)[0:7])
}

// stage returns the name of the environment variable that holds the stage.
func (id envVar) stage() string {
	return "TSR_" + string(id) + "__STG"
}

// pid returns the name of the environment variable that holds the PID.
func (id envVar) pid() string {
	return "TSR_" + string(id) + "__PID"
}
