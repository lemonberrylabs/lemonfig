// logger-zap demonstrates using Uber's zap logger with lemonfig.
package main

import (
	"context"
	"fmt"
	"log"

	"go.uber.org/zap"

	"github.com/lemonberrylabs/lemonfig"
)

// ZapAdapter wraps *zap.SugaredLogger to satisfy lemonfig.Logger.
type ZapAdapter struct {
	*zap.SugaredLogger
}

func (a ZapAdapter) Info(msg string, keysAndValues ...any) {
	a.SugaredLogger.Infow(msg, keysAndValues...)
}

func (a ZapAdapter) Error(msg string, keysAndValues ...any) {
	a.SugaredLogger.Errorw(msg, keysAndValues...)
}

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
	zapLogger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatal(err)
	}
	defer zapLogger.Sync()

	src := &staticSource{data: []byte("app_name: zap-demo\ndebug: true\n")}
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithLogger(ZapAdapter{zapLogger.Sugar()}),
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
