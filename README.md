# GO-TSR

gotsr — is a library for daemonising Go programs.

TSR stands for [Terminate-and-Stay-Resident][1], was an acronym describing the
type of behaviour when loaded program would return the control to the
Operating System, and continue to execute in memory.  These types of programs
commonly refer to daemons in Unix/Linux systems and Services in Windows.

Currently only Posix systems are supported (tested on Linux and Darwin).
Windows support is not yet implemented.

For usage, see [responder][./cmd/responder/main.go]

[1]: https://en.wikipedia.org/wiki/Terminate-and-stay-resident_program
