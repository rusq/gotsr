package gotsr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	errInvalidStage = errors.New("invalid stage")
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
	// case sDetach.String(): // releasing handles, clean start
	// 	return sDetach, stageDetach(vars, image)
	case sRunning.String(): // running TSR program
		return sRunning, stageRun(pidFile, vars, atExit)
	}
	// unreachable
}

// stageInit is the first stage that starts a new detached instance of the
// program in a new session.
func stageInit(pidFile string, vars envVar, image string, timeout time.Duration) error {
	ln, err := newStartupChannel(vars)
	if err != nil {
		return err
	}
	defer ln.Close()

	if err := os.Setenv(vars.stage(), sRunning.String()); err != nil {
		return fmt.Errorf("set running stage: %w", err)
	}

	cmd := exec.Command(image, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = nil
	cmd.Stdout = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to initialise the process: %s", err)
	}
	if err := awaitStartup(ln, vars, timeout); err != nil {
		return err
	}

	pid, err := readPID(pidFile)
	if err != nil {
		lg.Printf("process started, but PID file is missing: %s", err)
	} else if pid == 0 {
		lg.Println("warning: process started, but PID is 0")
	} else {
		lg.Printf("process started with PID: %d", pid)
	}
	return nil
}

// stageRun runs the main program.
func stageRun(pidFile string, vars envVar, atExit []func()) error {
	stop := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() {
		stopOnce.Do(func() { close(stop) })
	}
	runtime, err := startControl(pidFile, os.Getpid(), requestStop)
	if err != nil {
		_ = reportStartup(vars, err)
		return err
	}
	registerControlRuntime(runtime)

	if err := reportStartup(vars, nil); err != nil {
		runtime.Close()
		return err
	}
	// unset the environment variables once the program is running.
	for _, envVar := range []string{vars.stage(), vars.addr(), vars.secret()} {
		if err := os.Unsetenv(envVar); err != nil {
			lg.Printf("failed to unset environment variable %s: %s", envVar, err)
		}
	}

	go func() {
		<-stop
		for _, fn := range atExit {
			fn()
		}
		runtime.Close()
		os.Exit(0)
	}()
	return nil
}
