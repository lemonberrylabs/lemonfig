# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`lemonfig` — a reactive, hot-reloadable configuration library for Go. OSS, module path `github.com/lemonberrylabs/lemonfig`. Targets Go 1.25+.

Uses Viper under the hood. Provides `Derived[T]` handles that always return the latest config value via atomic generation swaps.

## Commands

```bash
go build ./...                        # build all packages
go test ./... -count=1                # run all tests
go test -run TestName .               # run a single test in root package
go test -run TestName ./source/       # run a single test in source package
go test -race ./...                   # run tests with race detector
go vet ./...                          # static analysis
go test -bench=. ./...                # benchmarks
go build -gcflags='-m' ./... 2>&1     # check heap escapes
```

## Architecture

**Primary API:**

```go
mgr, _ := lemonfig.NewManager(source)
cfg := lemonfig.Load[Config](mgr)
env := lemonfig.Map(cfg, func(c Config) (Environment, error) { return c.Environment, nil })
mgr.Start(ctx)
```

**Advanced API:** `Key`/`Struct` for path-based access without a root struct.

- **Manager** (`manager.go`) — central orchestrator. Owns the `ConfigSource`, lifecycle (`Start`/`Stop`/`Reload`), and the DAG. Holds atomic pointer to current `generation`, serializes reloads via mutex.
- **Derived[T]** (`derived.go`) — read-only reactive value handle. `Get()` does one atomic pointer load + map lookup. `Load`, `Map`, `Combine`, etc. are package-level functions (Go does not support methods with additional type parameters).
- **Generation** (`generation.go`) — immutable snapshot: version + frozen Viper + `map[derivedID]any`. Atomically swapped via `atomic.Pointer`.
- **DAG nodes** (`derived.go`) — type-erased `derivedNode` interface. Root nodes (`keyNode`, `structNode`) extract from Viper; transform nodes (`mapNode`, `combineNode`, `combine3Node`) recompute only when parents are dirty.
- **Sources** (`source/`) — `ConfigSource` and `WatchableSource` interfaces in `source.go`. `FileSource` (fsnotify + debounce) and `PollingSource` (ticker + byte comparison) in `source/`.
- **Options** (`options.go`) — functional options pattern for Manager config.

Key invariant: all `Load`/`Map`/`Combine` registrations must happen before `mgr.Start()`. The DAG is frozen at Start.

## Go Style & Conventions

- **Idiomatic Go.** Follow Effective Go, the Go Code Review Comments wiki, and stdlib conventions.
- **Generics over `any`.** Use concrete types or type parameters. Avoid `any` / `interface{}` unless truly required by the API contract (e.g., the internal `generation.values` map). Every use of `any` must be justified.
- **Go 1.25 features.** Use the latest language and stdlib additions. When unsure about Go 1.25 capabilities, **check context7 for the latest Go documentation** before assuming a feature doesn't exist.
- **DRY & KISS.** No premature abstractions. No unnecessary indirection. Duplicate code only when the alternative is worse coupling.
- **Performance matters.** `Derived[T].Get()` must remain lock-free (single atomic load + map lookup). Prefer zero-allocation paths. Benchmark hot paths. Avoid unnecessary heap escapes — use `go build -gcflags='-m'` to check.
- **Error handling.** Return `error`; don't panic (except `ErrFrozen` on post-Start registration). Use `fmt.Errorf` with `%w` for wrapping. Sentinel errors are in `errors.go`.
- **Naming.** Short, unexported helpers. Exported names get a brief GoDoc comment. Package names are lowercase, single-word when possible.
- **No `init()` functions.** Explicit initialization only.
- **Tests live next to code** (`foo_test.go` beside `foo.go`). Use table-driven tests and `t.Parallel()` where safe. Test helpers (`staticSource`, `startWithSource`, `mustStart`) are in `manager_test.go` and shared across test files via `_test` package.
- **Minimal dependencies.** Only Viper and fsnotify beyond stdlib. Every new dependency is inherited by consumers.
