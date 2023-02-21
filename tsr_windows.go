package gotsr

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

var (
	errInvalidStage = errors.New("invalid stage")
)

// addr returns an additional environment variable used only on Windows.
// It is used to pass the addr number to the detached process.
func (id envVar) addr() string {
	return "TSR_" + string(id) + "__ADDR"
}

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	os.Setenv(vars.stage(), sRunning.String())
	os.Setenv(vars.pid(), strconv.Itoa(os.Getpid()))
	os.Setenv(vars.addr(), ln.Addr().String())
	log.Printf("listening on %s", ln.Addr().String())

	cmd := exec.Command(image, os.Args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stderr = nil
	cmd.Stdout = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to initialise the process: %s", err)
	}
	timer := time.After(timeout)
	go func() {
		<-timer
		ln.Close()
	}()

	conn, err := ln.Accept()
	if err != nil {
		return err
	}
	conn.Close()
	defer ln.Close()

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
	pid := os.Getpid()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	if err := writePID(pidFile, pid, ln.Addr().String()); err != nil {
		return err
	}

	if err := notifySuccess(vars); err != nil {
		lg.Printf("failed to notify the parent process: %s", err)
	}
	// unset the environment variables once the program is running.
	for _, envVar := range []string{vars.stage(), vars.pid(), vars.addr()} {
		if err := os.Unsetenv(envVar); err != nil {
			lg.Printf("failed to unset environment variable %s: %s", envVar, err)
		}
	}

	quit := make(chan struct{})
	go func() {
		<-quit
		for _, fn := range atExit {
			fn()
		}
		ln.Close()
		os.Remove(pidFile)
		os.Exit(0)
	}()

	// listener:
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 2)
				if _, err := conn.Read(buf); err != nil {
					return
				}
				if string(buf) == "ok" {
					conn.Write([]byte("ok"))
				}
				if string(buf) == "ex" {
					conn.Write([]byte("ok"))
					close(quit)
				}
			}()
		}
	}()

	return nil
}

// notifySuccess notifies the parent process that the program has started.
func notifySuccess(vars envVar) error {
	sAddr := os.Getenv(vars.addr())
	if sAddr == "" {
		return errors.New("missing address")
	}
	conn, err := net.Dial("tcp", sAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ok")); err != nil {
		return err
	}
	return nil
}

// isRunning checks if the process with the given PID is running.
func isRunning(pidFile string) (bool, error) {
	var pAddr string
	pid, err := readPID(pidFile, &pAddr)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	} else if pid == 0 {
		return false, ErrNoPID
	}
	if pAddr == "" {
		return false, errors.New("invalid pidfile:  missing address")
	}
	conn, err := net.Dial("tcp", pAddr)
	if err != nil {
		return false, nil
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ok")); err != nil {
		return false, nil
	}
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil {
		return false, err
	}
	if string(buf) != "ok" {
		return false, errors.New("invalid response")
	}
	return true, nil
}

// terminate sends a SIGTERM signal to the process with the given PID.
func terminate(pidFile string) error {
	var pAddr string
	pid, err := readPID(pidFile, &pAddr)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotRunning
		}
		return err
	}
	if pAddr == "" {
		return errors.New("invalid pidfile:  missing address")
	}
	conn, err := net.Dial("tcp", pAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ex")); err != nil {
		return err
	}
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil {
		return err
	}
	if string(buf) != "ok" {
		return errors.New("invalid response")
	}
	lg.Printf("process %d terminated", pid)
	return nil
}
