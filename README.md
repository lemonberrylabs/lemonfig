# lemonfig

Reactive, hot-reloadable configuration for Go. Instead of reading config values as plain structs, you get `Derived[T]` handles that always return the latest value. When config reloads, all derived values are atomically recomputed and swapped in — including heavy resources like DB pools, HTTP clients, and gRPC connections.

## Install

```bash
go get github.com/lemonberrylabs/lemonfig
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/lemonberrylabs/lemonfig"
    "github.com/lemonberrylabs/lemonfig/source"
)

type Config struct {
    Name   string       `mapstructure:"name"`
    Server ServerConfig `mapstructure:"server"`
}

type ServerConfig struct {
    Host string `mapstructure:"host"`
    Port int    `mapstructure:"port"`
}

func main() {
    src := source.NewFileSource("config.yaml")
    mgr, err := lemonfig.NewManager(src)
    if err != nil {
        log.Fatal(err)
    }

    // Load the full config struct.
    cfg := lemonfig.Load[Config](mgr)

    // Derive sub-fields or resources before Start.
    port := lemonfig.Map(cfg, func(c Config) (int, error) {
        return c.Server.Port, nil
    })

    if err := mgr.Start(context.Background()); err != nil {
        log.Fatal(err)
    }
    defer mgr.Stop()

    fmt.Println(cfg.Get().Name)   // always returns the latest value
    fmt.Println(port.Get())       // reactive sub-field
}
```

## Deriving Sub-Fields

Use `Map` to extract config sub-fields or transform them into derived resources.

> **Note:** `Map`, `Combine`, and other combinators are package-level functions (not methods)
> because Go does not support methods with additional type parameters.

```go
mgr, _ := lemonfig.NewManager(src)
cfg := lemonfig.Load[Config](mgr)

// Extract a sub-field.
env := lemonfig.Map(cfg, func(c Config) (Environment, error) {
    return c.Environment, nil
})

// Derive a heavy resource with cleanup.
pool := lemonfig.MapWithCleanup(cfg,
    func(c Config) (*pgxpool.Pool, error) {
        return pgxpool.New(context.Background(), c.Database.URL)
    },
    func(old *pgxpool.Pool) {
        old.Close()
    },
)

mgr.Start(ctx)
defer mgr.Stop()

pool.Get().QueryRow(ctx, "SELECT ...")
```

## Custom ConfigSource

Implement `ConfigSource` to load config from any backend:

```go
type HTTPSource struct {
    URL string
}

func (s *HTTPSource) Fetch(ctx context.Context) ([]byte, string, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
    if err != nil {
        return nil, "", err
    }
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, "", err
    }
    defer resp.Body.Close()
    data, err := io.ReadAll(resp.Body)
    return data, "json", err
}
```

Wrap it with polling for automatic reloads:

```go
src := source.NewPollingSource(&HTTPSource{URL: "https://config.internal/app"}, 30*time.Second)
mgr, _ := lemonfig.NewManager(src)
cfg := lemonfig.Load[Config](mgr)
```

## Atomicity Guarantees

All `Derived[T]` values within a generation are computed from the same config snapshot. A generation is swapped atomically via `atomic.Pointer` — there is no moment where some values reflect old config and others reflect new config.

`Derived[T].Get()` performs a single atomic pointer load, making it lock-free and safe for concurrent use from any goroutine.

**Note:** Two separate `.Get()` calls may observe different generations if a reload happens between them. For values that must be consistent with each other, use `Combine` to express the dependency explicitly.

## Cleanup and Resource Lifecycle

When a reload replaces a derived value that has a cleanup function (registered via `MapWithCleanup`), the old value's cleanup runs after a configurable grace period (default: 30 seconds). This lets in-flight requests finish before resources are torn down.

Cleanup runs in reverse topological order (leaves before roots) in a background goroutine. Panics in cleanup functions are caught and logged.

```go
lemonfig.WithCleanupGrace(10 * time.Second)
```

## Error Handling During Reload

Reload follows an all-or-nothing principle:

- **Fetch failure:** old generation preserved, error logged.
- **Parse failure:** old generation preserved, error logged.
- **Validation failure:** old generation preserved (use `WithValidation`).
- **Transform failure:** entire reload aborted, old generation preserved. No partial updates.

`Manager.Reload()` returns the error, so callers can handle it. When using `WatchableSource`, errors are logged via the configured `Logger`.

## Registration Must Happen Before Start

All `Load`, `Map`, `MapWithCleanup`, `Combine`, and `Combine3` calls must happen before `mgr.Start()`. After Start, the DAG is frozen and any attempt to register new derived values will panic. This keeps the implementation simple and avoids race conditions with concurrent reloads.

## Advanced API

For fine-grained control, use `NewManager` directly with `Key` and `Struct`:

```go
mgr, _ := lemonfig.NewManager(src)
host := lemonfig.Key[string](mgr, "redis.host")
port := lemonfig.Key[int](mgr, "redis.port")
addr := lemonfig.Combine(host, port, func(h string, p int) (string, error) {
    return fmt.Sprintf("%s:%d", h, p), nil
})
mgr.Start(ctx)
```

## Logging

Pass a logger via `WithLogger`. The library defines a minimal interface — wrap your preferred logger:

```go
type Logger interface {
    Info(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
}

// Example: wrap slog
type SlogAdapter struct{ *slog.Logger }

func (a SlogAdapter) Info(msg string, kv ...any)  { a.Logger.Info(msg, kv...) }
func (a SlogAdapter) Error(msg string, kv ...any) { a.Logger.Error(msg, kv...) }
```

## License

MIT
