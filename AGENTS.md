# AGENTS.md

## Purpose

This file gives coding agents the repository-specific commands, conventions, and
guardrails needed to work safely in `github.com/rusq/gotsr`.

The project is a small Go library for daemonising/backgrounding Go programs.
Primary code lives in the root package `gotsr`. A usage example lives in
`cmd/responder`.

## Repository Layout

- `tsr.go`: public API, options, PID file helpers, shared logic.
- `tsr_posix.go`: POSIX implementation behind build tags.
- `tsr_windows.go`: experimental Windows implementation.
- `lock_posix.go`: PID file locking (daemon identity) on POSIX.
- `tsr_test.go`: unit tests for helper functions in the root package.
- `lock_posix_test.go`, `lifecycle_posix_test.go`, `helper_posix_test.go`:
  POSIX lock and subprocess lifecycle tests.
- `tsr_windows_test.go`: placeholder Windows test coverage.
- `stage.go` and `stage_string.go`: stage enum and generated string helpers.
- `logger.go`: package logger abstraction.
- `envvar.go`: environment variable naming helpers.
- `cmd/responder/main.go`: sample daemonised HTTP responder.
- `.github/workflows/go.yml`: current CI workflow.

## Rules Files

No repository-specific Cursor or Copilot instruction files were found at the
time this file was written.

- No `.cursor/rules/` entries found.
- No `.cursorrules` file found.
- No `.github/copilot-instructions.md` file found.

If any of those files are added later, treat them as higher-priority guidance
and update this document accordingly.

## Environment Notes

- Module path: `github.com/rusq/gotsr`
- Declared Go version: `1.27` in `go.mod`
- CI currently runs on Ubuntu with `go build -v ./...` and `go test -v ./...`
- The default implementation target is POSIX; Windows support is experimental
  per `README.md`

## Important Current State

- On POSIX, process identity is an `fcntl` lock on `<pidfile>.lock`, held by
  the daemon for its lifetime (`lock_posix.go`).  The PID file is
  informational.  Inside the daemon, never open the lock file again: closing
  any descriptor of it drops the lock.  Use the `held` state instead.  The lock
  file is never unlinked.
- Lifecycle tests re-execute the test binary as a helper (`helper_posix_test.go`).

## Build Commands

Run from repository root.

- Build all packages:
  - `go build ./...`
- Verbose build, matching CI more closely:
  - `go build -v ./...`
- Build the sample program only:
  - `go build ./cmd/responder`
- Cross-check package compilation without tests:
  - `go test -run '^$' ./...`

## Test Commands

- Run all tests:
  - `go test ./...`
- Run all tests verbosely:
  - `go test -v ./...`
- Run tests in the root package only:
  - `go test .`
- Run one specific test in the root package:
  - `go test -run '^Test_hash$' .`
- Run one specific test across all packages:
  - `go test -run '^Test_readPID$' ./...`
- Run one subtest:
  - `go test -run '^Test_readPID$/^number$' .`
- Run Windows-specific placeholder tests if working on Windows code:
  - `go test -run '^Test_stageInit$' .`
- Disable test result caching when validating behavior changes:
  - `go test -count=1 ./...`

## Lint and Static Analysis

There is no dedicated linter config in the repository today. Use standard Go
tooling unless the user asks for something else.

- Format code:
  - `gofmt -w .`
  - Prefer formatting only touched files when practical.
- Vet all packages:
  - `go vet ./...`
- If `golangci-lint` is available locally, it is reasonable to run:
  - `golangci-lint run`

Do not add new toolchain dependencies or config files unless the user asks.

## Preferred Validation Flow

For small changes in one file:

1. `gofmt -w <touched-files>`
2. `go test ./...`

For risky runtime or process-management changes:

1. `gofmt -w <touched-files>`
2. `go vet ./...`
3. `go test -count=1 ./...`
4. If relevant, build and manually exercise `./cmd/responder`

## Manual Smoke Test

The sample app is useful for daemon lifecycle checks.

- Start responder:
  - `go run ./cmd/responder`
- Check status:
  - `go run ./cmd/responder -status`
- Stop process:
  - `go run ./cmd/responder -stop`

Expect the sample to write `responder.log` when the detached child runs.

## Code Style Guidelines

Follow existing repository style first. This codebase is small and fairly
idiomatic Go, with a preference for stdlib-only solutions and simple APIs.

### Formatting

- Always run `gofmt` on changed Go files.
- Keep lines and control flow simple; avoid clever compact constructs.
- Preserve existing build tags and file-level package comments.
- Keep exported declarations documented when they are part of the public API.

### Imports

- Use standard Go import grouping as produced by `gofmt`.
- Put standard library imports first.
- Keep one blank line between stdlib and third-party imports when needed.
- Avoid adding dependencies unless they provide clear value and the user asked.
- Prefer stdlib packages already used elsewhere in the repo.

### Types and API Design

- Prefer small structs with explicit fields, as in `Process`.
- Use constructor-plus-options patterns for configurable public APIs.
- Keep exported surface area minimal.
- Prefer concrete types unless an interface is required for substitution, like
  `Logger`.
- Keep helper functions unexported unless they are part of the public contract.

### Naming

- Use short, direct package-level names consistent with current code.
- Exported names use Go standard CamelCase: `Process`, `WithPIDFile`.
- Unexported helpers use lowerCamelCase: `readPID`, `writePID`, `pidFromExe`.
- Acronyms follow existing repo conventions, not necessarily modern initialism
  rules; preserve established names like `PID`, `TSR`, and `ErrNoPID`.
- Avoid renaming public identifiers unless explicitly requested.

### Error Handling

- Return errors instead of panicking.
- Use package-level sentinel errors only when callers need to branch on them,
  as with `ErrNoPID` and `ErrNotRunning`.
- Wrap errors with context when crossing process, file, or OS boundaries.
- Prefer `%w` when preserving the original error matters.
- Keep error messages lowercase and without trailing punctuation.
- When checking expected error conditions, use `errors.Is`.

### Control Flow

- Prefer early returns over nested conditionals.
- Keep stage transitions explicit; this codebase depends on readable startup
  state management.
- Be careful with goroutines that call `os.Exit`; changes here can affect API
  semantics and cleanup behavior.

### Concurrency and Process Management

- Be conservative when editing signal handling, process startup, PID files, or
  environment-variable coordination.
- Preserve behavior differences between POSIX and Windows implementations unless
  intentionally refactoring both.
- When changing lifecycle behavior, inspect both `tsr_posix.go` and
  `tsr_windows.go` for parity or documented divergence.

### Testing Style

- Follow the existing table-driven style for unit tests.
- Prefer `t.Run` with descriptive case names.
- Use `t.TempDir()` for filesystem-related tests.
- Keep tests deterministic; avoid timing-sensitive sleeps when possible.
- Add regression coverage for any bug fix when the code path is testable.

### Generated Files

- `stage_string.go` is generated from `stage.go` via `stringer`.
- Do not hand-edit generated output unless the user explicitly asks.
- If you change the `stage` enum, regenerate the file with the appropriate tool
  rather than manually patching string mappings.

## Change Strategy for Agents

- Read the touched package before editing; behavior is subtle despite the small
  codebase.
- Prefer narrow fixes over broad refactors.
- Preserve public API compatibility unless the task explicitly requires a break.
- If you touch build-tagged files, verify the change does not silently diverge
  between platforms.

## When Unsure

- Prefer idiomatic Go.
- Prefer stdlib.
- Prefer smaller API surfaces.
- Prefer explicit error handling.
- Prefer tests for helper logic and regression cases.
