# GO-TSR

gotsr — is a library for daemonising Go programs.

TSR stands for [Terminate-and-Stay-Resident][1], was an acronym describing the
type of behaviour when loaded program would return the control to the
Operating System, and continue to execute in memory.  These types of programs
commonly refer to daemons in Unix/Linux systems and Services in Windows.

Currently only POSIX systems are supported (tested to work on Linux, Darwin and
NetBSD).  Windows support is experimental.

For usage example, see [cmd/responder](cmd/responder/main.go)

## Process state and compatibility

`gotsr` uses the configured PID-file path as an authenticated process-state
file. The first line remains the daemon PID for compatibility with `PID()`.
The second line contains versioned control metadata for a loopback-only endpoint
and a cryptographically random per-instance secret. Status and termination use
an authenticated challenge/response exchange with that endpoint; they never
signal the PID recorded in the file.

State is published atomically with owner-only file permissions. Callers should
place it in a private, user-specific directory. A stale or malformed file, an
endpoint that does not authenticate as the recorded daemon instance, and the
legacy PID-only format from v0.1.0 all fail closed without signalling the
recorded PID. Legacy state must be removed manually before starting a new
instance.

An OS-released lifetime lock beside the state file serialises publication and
cleanup. Concurrent starts return `ErrAlreadyRunning`, and a successor cannot
publish until its predecessor has removed its state and released the lock. If a
process crashes, the operating system releases the lock; a later start may
replace well-formed v2 state only when its authenticated endpoint is no longer
reachable. Malformed, legacy, insecure, or identity-mismatched state is not
recovered automatically and must be inspected and removed manually.

Daemon startup uses a bounded, authenticated result channel from the running
stage back to the original `TSR()` caller. Errors such as
`ErrAlreadyRunning` are returned promptly instead of being reported as a
generic startup timeout.

[1]: https://en.wikipedia.org/wiki/Terminate-and-stay-resident_program
