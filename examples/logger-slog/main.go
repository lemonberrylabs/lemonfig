// logger-slog demonstrates using Go's standard log/slog with lemonfig.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/lemonberrylabs/lemonfig"
)

// SlogAdapter wraps *slog.Logger to satisfy lemonfig.Logger.
type SlogAdapter struct {
	*slog.Logger
}

func (a SlogAdapter) Info(msg string, keysAndValues ...any)  { a.Logger.Info(msg, keysAndValues...) }
func (a SlogAdapter) Error(msg string, keysAndValues ...any) { a.Logger.Error(msg, keysAndValues...) }

type Config struct {
	AppName string `mapstructure:"app_name"`
	Debug   bool   `mapstructure:"debug"`
}

// staticSource serves in-memory config bytes.
type staticSource struct{ data []byte }

func (s *staticSource) Fetch(_ context.Context) ([]byte, string, error) {
	return s.data, "yaml", nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	src := &staticSource{data: []byte("app_name: slog-demo\ndebug: true\n")}
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithLogger(SlogAdapter{logger}),
	)
	if err != nil {
		log.Fatal(err)
	}

	cfg := lemonfig.Load[Config](mgr)

	if err := mgr.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer mgr.Stop()

	fmt.Printf("app=%s debug=%v\n", cfg.Get().AppName, cfg.Get().Debug)
}
