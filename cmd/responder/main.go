package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/rusq/gotsr"
)

var (
	addr    = flag.String("addr", ":6060", "http listener address")
	stop    = flag.Bool("stop", false, "stop running process")
	status  = flag.Bool("status", false, "process status")
	pidFile = flag.String("pid", "", "custom PID file")
)

func main() {
	flag.Parse()

	// Create a new TSR process
	p, err := gotsr.New(gotsr.WithPIDFile(*pidFile))
	if err != nil {
		log.Fatal(err)
	}
	if *stop {
		// Terminate running process if -stop flag is set.
		if err := stopProcess(p); err != nil {
			log.Fatal(err)
		}
		return // exit
	}
	if *status {
		if err := printStatus(p); err != nil {
			log.Fatal(err)
		}
		return // exit
	}
	// We need to make sure that we're not trying to startup the second time.
	if isRunning, err := p.IsRunning(); err == nil && isRunning {
		log.Fatal("already running")
	}

	// Register a function to be called when the program is terminating.  It is
	// important to add all AtExit functions before calling TSR().
	p.AtExit(func() {
		log.Printf("process is terminating")
	})

	// Start the process.  If the process is already running, this will return
	// an error.
	headless, err := p.TSR()
	if err != nil {
		log.Fatal(err)
	}

	// If we're headless, we're the child process.  Otherwise, we're the parent.
	if headless {
		// Close removes the PID file with the child's PID.
		defer p.Close()

		// As we are the child process, we need to redirect the log output to
		// a file, as there's no STDOUT.
		f, err := os.Create("responder.log")
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		log.SetOutput(f)

		// Writing some info to the log file to indicate that we're alive.
		log.Printf("this is child with pid: %d, ppid: %d", os.Getpid(), os.Getppid())

		// Start the HTTP server, which will respond to all requests with "OK",
		// and will terminate if the program is called with -stop flag.
		if err := responder(context.Background(), *addr); err != nil {
			log.Printf("http server error: %s", err)
		}
	} else {
		// Write some hints on usage to the STDOUT.
		log.Printf("this is parent with PID: %d, parent: %d.  See 'responder.log' for child output.", os.Getpid(), os.Getppid())
		log.Println("Try 'curl localhost:6060' to see if it's working")
		log.Printf("To stop the process, run: %s -stop", os.Args[0])
	}
}

func stopProcess(p *gotsr.Process) error {
	if err := p.Terminate(); err != nil {
		if errors.Is(err, gotsr.ErrNotRunning) {
			log.Printf("process already stopped")
			return nil
		}
		return err
	}
	log.Println("process stopped")
	return nil
}

func printStatus(p *gotsr.Process) error {
	// Check if the process is running.
	if running, err := p.IsRunning(); err != nil {
		if errors.Is(err, gotsr.ErrNotRunning) {
			log.Printf("process is not running")
			return nil
		}
		return err
	} else if running {
		log.Println("process is running")
	} else {
		log.Println("process is not running")
	}
	return nil
}

// responder is a simple HTTP server that responds with "OK" to all requests.
func responder(ctx context.Context, addr string) error {
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/plain")
		fmt.Fprintf(w, "OK, PID=%d\n", os.Getpid())
	}))
	return http.ListenAndServe(addr, nil)
}
