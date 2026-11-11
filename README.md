# GO-TSR

gotsr — is a library for daemonising Go programs.

TSR stands for [Terminate-and-Stay-Resident][1], was an acronym describing the
type of behaviour when loaded program would return the control to the
Operating System, and continue to execute in memory.  These types of programs
commonly refer to daemons in Unix/Linux systems and Services in Windows.

Currently only POSIX systems are supported (tested to work on Linux, Darwin and
NetBSD).  Windows support is experimental.

For usage example, see [cmd/responder](cmd/responder/main.go)

## How it works (POSIX)

The background process holds an `fcntl(2)` write lock on `<pidfile>.lock` for
its whole lifetime.  The kernel releases the lock when the process exits, even if
it is killed with `SIGKILL`.  `IsRunning`, `PID` and `Terminate` ask the
kernel who holds the lock instead of trusting the PID written in the PID file,
which is informational only.  So:

- checking the status never sends a signal;
- a stale PID file never causes an unrelated process to be signalled;
- a second `TSR()` returns `ErrAlreadyRunning` and leaves the running process
  alone;
- `Terminate` sends `SIGTERM` to the lock holder and returns once it has
  exited.

Limitations:

- The lock file is never removed; it stays next to the PID file as a sentinel.
- The PID file must be on a local filesystem, and all processes must share a
  PID namespace.
- A relative PID file path is resolved against the working directory when
  `New` is called.  Use an absolute path (`WithPIDFile`) if the program can be
  started from different working directories.
- On Linux, `Terminate` signals the process through a pidfd, which rules out
  PID reuse.  On other systems a very small window remains between the lock
  check and the signal.
- Windows uses a different, experimental mechanism and does not lock the PID
  file.

[1]: https://en.wikipedia.org/wiki/Terminate-and-stay-resident_program
