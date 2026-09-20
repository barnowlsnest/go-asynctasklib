# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project

`go-asynctasklib` — a dependency-light Go library for asynchronous task
execution: retries, lifecycle hooks, worker pools, and a leased task queue.
It is a **library, not an application**: there is no `main`, no root package,
and no binary to run.

- Module path: `github.com/barnowlsnest/go-asynctasklib/v2` — the `/v2` suffix
  is part of every import path. Omitting it is the most common docs/example bug.
- Go version: **1.27** (`go.mod`). CI installs it via `go-version-file: go.mod`,
  so bumping `go.mod` is the single place the version changes.

## Layout

All code lives under `pkg/`. Dependencies flow one way — a package never imports
a package listed below it:

| Package | Purpose |
|---|---|
| `pkg/semaphore` | Channel-based counting semaphore. No internal deps. |
| `pkg/retry` | `Strategy` interface + `Linear` / `ExponentialBackoff`. No internal deps. |
| `pkg/yielder` | Generic one-shot value generator `Yielder[T comparable]`. No internal deps. |
| `pkg/task` | `Task`, `Definition`, `Builder`, `StateHooks`. Imports `retry`. |
| `pkg/taskgroup` | Bounded concurrent execution of `task.Definition`s. Imports `task`. |
| `pkg/workerpool` | Generic `WorkerPool[T]`, fixed-size or auto-scaling, claims dispatcher. |
| `pkg/taskqueue` | Leased queue (claim/ack/nack), DLQ, reaper, plus the standalone `Lobby`. Imports `yielder`. |

`workerpool` and `taskqueue` are the two most intricate packages; both have a
`doc.go` with a package-level overview worth reading before changing them.

## Commands

The project is driven by [Task](https://taskfile.dev) (`Taskfile.yaml`):

```bash
task sanity      # go mod tidy + clean + fmt + vet + lint + test  ← run this
task go-test     # go test -v -race -cover ./...
task go-build    # go build ./...
task go-lint     # golangci-lint run --fix
```

**Run `task sanity` after every implementation step, not just at the end.**

## CI

- `.github/workflows/build.yml` — `task go-build` + `task go-test`
- `.github/workflows/golangci-lint.yml` — golangci-lint v2.13

Both resolve the Go toolchain from `go.mod`. Note that CI sets
`GOTOOLCHAIN=local` (via `actions/setup-go`), so Go will **not** auto-download a
newer toolchain — a `go.mod` bump that outruns the installed Go fails the build.

## Conventions

### Lint (`.golangci.yaml`, `version: "2"`)

Strict by design. The limits that bite most often:

- `lll` line length **140**
- `funlen` 100 lines / 50 statements
- `gocyclo` min-complexity **15**
- `dupl` threshold 100
- `goconst` fires at **2** occurrences of a repeated literal
- `gocritic` with `diagnostic`, `experimental`, `opinionated`, `style`, `performance` all enabled
- `goimports` local prefix is `github.com/barnowlsnest/go-asynctasklib/v2` — module-local imports go in their own final group

`_test.go` files are exempt from `dupl`, `funlen`, `goconst`, `gocyclo`, `gosec`.

### Errors

Each package keeps its sentinels in `errors.go` and wraps rather than
reformats. `workerpool` nests them — `ErrNilJob` and `ErrNilCtx` are
`fmt.Errorf("%w: ...", ErrNil)`, so `errors.Is(err, ErrNil)` matches any
"nil X" condition. Follow that shape when adding new errors.

### Contexts

Every method taking a `ctx` must honor it — at minimum check `ctx.Err()`.
This is enforced by review, not by a linter.

### Tests

- `testify/suite` is the default; collapse same-shape cases into **table-driven**
  subtests rather than repeating near-identical test funcs.
- `workerpool` and `taskqueue` run under **goleak** via `TestMain`
  (`main_test.go`). A leaked goroutine fails the whole package — it usually
  means a missing `Close`/`Shutdown` path, not a flaky test.
- Naming: use `test...` (not `fake...`) for doubles, and `expected` (not `want`)
  for expectations.
- Helper funcs call `s.T().Helper()` / `t.Helper()` as their first line.
- No magic numbers — hoist them into named `const`s (see `testReapInterval`,
  `testMaxAttempts` in `pkg/taskqueue/queue_test.go`).
- Assert returned values; never discard them with `_`.

### Go version features

`go.mod` is on 1.27, so `wg.Go(func(){...})` is available and preferred over the
`wg.Add(1)` / `defer wg.Done()` pair.

## Documentation

`README.md` is the user-facing API reference and is expected to stay in sync
with the exported surface. When changing an exported signature, option, or
sentinel error, update the matching section of the README in the same change.
Every fenced `package main` block in the README is expected to compile.

## Dependencies

Three direct runtime dependencies, each confined to one package:

- `golang.org/x/sync` — errgroup, in `taskgroup`
- `golang.org/x/time` — rate limiter, in `workerpool`
- `github.com/google/uuid` — lease tokens, in `taskqueue`

Test-only: `stretchr/testify`, `go.uber.org/goleak`. Adding a new dependency is
a deliberate decision — prefer the standard library.
