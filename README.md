# GO-TSR

gotsr — is a library for daemonising Go programs.

TSR stands for [Terminate-and-Stay-Resident][1], was an acronym describing the
type of behaviour when loaded program would return the control to the
Operating System, and continue to execute in memory.  These types of programs
commonly refer to daemons in Unix/Linux systems and Services in Windows.

Currently only POSIX systems are supported (tested to work on Linux, Darwin and
NetBSD).  Windows support is experimental.

For usage example, see [cmd/responder](cmd/responder/main.go)

[1]: https://en.wikipedia.org/wiki/Terminate-and-stay-resident_program
