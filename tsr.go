// Package TSR provides the API to make the program run in the background,
// what used to be called "Terminate and Stay Resident" back in the days.
package gotsr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	startTimeout = 60 * time.Second
)

// try on windows: https://superuser.com/questions/198525/how-can-i-execute-a-windows-command-line-in-background

var (
	ErrNoPID      = errors.New("PID unknown")
	ErrNotRunning = errors.New("not running")
)

type Process struct {
	pidFile      string
	startTimeout time.Duration
	atExit       []func()
}

type Option func(*Process)

func WithPIDFile(fullpath string) Option {
	return func(p *Process) {
		p.pidFile = fullpath
	}
}

func WithDebug(b bool) Option {
	return func(p *Process) {
		if b {
			SetLogger(log.New(os.Stderr, "", log.LstdFlags))
		}
	}
}

// New returns new Process.  If caller does not set the PID file path and name
// explicitely with WithPIDFile option, it is inferred from the executable file
// name.  So that the PID file for "foo.exe" will be "foo.pid".
func New(opts ...Option) (*Process, error) {
	var p = Process{
		startTimeout: startTimeout,
	}
	for _, opt := range opts {
		opt(&p)
	}
	if p.pidFile == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		p.pidFile = pidFromExe(exe)
	}

	return &p, nil
}

// pidFromExe returns the PID file name based on the executable file name.
func pidFromExe(executable string) string {
	base := filepath.Base(executable)
	ext := filepath.Ext(executable)
	return base[0:len(base)-len(ext)] + ".pid"
}

// TSR starts the program in the background.
func (p *Process) TSR() (headless bool, err error) {
	return tsr(p.pidFile, p.startTimeout, p.atExit...)
}

// PID returns the PID of the TSR process if it's running.
func (p *Process) PID() (int, error) {
	return readPID(p.pidFile)
}

// AtExit appends the function to the list of functions that will be executed
// when the TSR process terminates.  It should be called before TSR() is called.
func (p *Process) AtExit(fn func()) {
	p.atExit = append(p.atExit, fn)
}

// IsRunning returns true if the TSR process is running.
func (p *Process) IsRunning() (bool, error) {
	return isRunning(p.pidFile)
}

// Terminate instructs the TSR process to terminate if it's running.
func (p *Process) Terminate() error {
	return terminate(p.pidFile)
}

// Close removes the PID file.
func (p *Process) Close() error {
	_ = os.Remove(p.pidFile)
	return nil
}

// readPID reads the PID from the PID file.
// PID File format:
//   PID
//   data1
//   ...
//   dataN
func readPID(filename string, data ...*string) (int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	var pid int
	if _, err := fmt.Fscanf(f, "%d", &pid); err != nil {
		return 0, err
	}

	// read any additional data stored in the file, if given any
	for i := range data {
		if _, err := fmt.Fscanln(f, data[i]); err != nil {
			return 0, err
		}
	}
	return pid, nil
}

func writePID(filename string, PID int, data ...string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%d\n", PID); err != nil {
		return err
	}
	for _, s := range data {
		if _, err := fmt.Fprintln(f, s); err != nil {
			return err
		}
	}
	return nil
}

func hash(s string) string {
	h := sha256.Sum224([]byte(s))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}
