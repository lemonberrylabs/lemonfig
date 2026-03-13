# Contributing to lemonfig

Thanks for your interest in contributing to lemonfig! This document covers guidelines and workflows for contributing.

## Getting Started

```bash
git clone https://github.com/lemonberrylabs/lemonfig.git
cd lemonfig
go test ./...
```

Requires **Go 1.25+**.

## Development Workflow

1. Fork the repository and create a branch from `main`.
2. Make your changes.
3. Add or update tests as needed.
4. Run the full suite:
   ```bash
   go test -race -count=1 ./...
   go vet ./...
   ```
5. Open a pull request against `main`.

## Code Style

- Follow [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) wiki.
- Use generics with concrete types. Avoid `any`/`interface{}` unless the API contract requires it.
- Keep exports minimal. Every exported name needs a GoDoc comment.
- No `init()` functions.
- Error handling: return `error`, use `fmt.Errorf` with `%w` for wrapping.

## Tests

- Tests live next to the code they test (`foo_test.go` beside `foo.go`).
- Use table-driven tests and `t.Parallel()` where safe.
- Shared test helpers are in `manager_test.go`.
- Run benchmarks with `go test -bench=. ./...` if your change touches hot paths.
- Check for heap escapes with `go build -gcflags='-m' ./... 2>&1` on performance-sensitive code.

## Performance

`Derived[T].Get()` must remain lock-free (single atomic pointer load + map lookup). Any change that adds allocations, locks, or contention to the read path needs benchmarks proving it doesn't regress.

## Examples

Examples live in `examples/` as separate Go modules (own `go.mod` with a `replace` directive). They are run as integration tests in CI. If you add an example:

1. Create `examples/<name>/main.go` and `examples/<name>/go.mod`.
2. Add a `replace github.com/lemonberrylabs/lemonfig => ../..` directive to the `go.mod`.
3. Make sure the example exits non-zero on failure.
4. Add the example name to the matrix in `.github/workflows/ci.yaml`.

## Pull Requests

- Keep PRs focused on a single change.
- Include a clear description of what and why.
- All CI checks must pass before merge.
- Maintainers may request changes — this is collaborative, not adversarial.

## Dependencies

lemonfig intentionally has a minimal dependency footprint (only Viper and fsnotify beyond stdlib). New dependencies need strong justification since every dependency is inherited by consumers.

## Reporting Issues

Open an issue on [GitHub](https://github.com/lemonberrylabs/lemonfig/issues). Include:

- Go version (`go version`)
- lemonfig version or commit
- Minimal reproduction case
- Expected vs. actual behavior

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
