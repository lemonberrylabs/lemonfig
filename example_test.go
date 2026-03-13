package lemonfig_test

import (
	"context"
	"fmt"

	"github.com/lemonberrylabs/lemonfig"
)

type appConfig struct {
	Name        string       `mapstructure:"name"`
	Environment string       `mapstructure:"environment"`
	Server      serverConfig `mapstructure:"server"`
}

type serverConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func ExampleLoad() {
	src := newStaticSource(`
name: myapp
environment: prod
server:
  host: localhost
  port: 8080
`)
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		panic(err)
	}

	cfg := lemonfig.Load[appConfig](mgr)

	if err := mgr.Start(context.Background()); err != nil {
		panic(err)
	}
	defer mgr.Stop()

	fmt.Println(cfg.Get().Name)
	fmt.Println(cfg.Get().Server.Port)
	// Output:
	// myapp
	// 8080
}

func ExampleMap() {
	src := newStaticSource(`
name: myapp
server:
  host: localhost
  port: 8080
`)
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		panic(err)
	}

	cfg := lemonfig.Load[appConfig](mgr)

	// Extract a sub-field as its own reactive value.
	name := lemonfig.Map(cfg, func(c appConfig) (string, error) {
		return c.Name, nil
	})

	// Derive a resource from config.
	db := lemonfig.Map(cfg, func(c appConfig) (*mockDB, error) {
		return &mockDB{URL: fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)}, nil
	})

	if err := mgr.Start(context.Background()); err != nil {
		panic(err)
	}
	defer mgr.Stop()

	fmt.Println(name.Get())
	fmt.Println(db.Get().URL)
	// Output:
	// myapp
	// localhost:8080
}

func ExampleCombine() {
	src := newStaticSource("host: example.com\nport: 443")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		panic(err)
	}

	host := lemonfig.Key[string](mgr, "host")
	port := lemonfig.Key[int](mgr, "port")
	connStr := lemonfig.Combine(host, port, func(h string, p int) (string, error) {
		return fmt.Sprintf("%s:%d", h, p), nil
	})

	if err := mgr.Start(context.Background()); err != nil {
		panic(err)
	}
	defer mgr.Stop()

	fmt.Println(connStr.Get())
	// Output: example.com:443
}

func ExampleManager_Reload() {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		panic(err)
	}

	cfg := lemonfig.Load[testConfig](mgr)

	if err := mgr.Start(context.Background()); err != nil {
		panic(err)
	}
	defer mgr.Stop()

	fmt.Println("before:", cfg.Get().Name)

	src.Set("name: bob")
	if err := mgr.Reload(context.Background()); err != nil {
		panic(err)
	}

	fmt.Println("after:", cfg.Get().Name)
	// Output:
	// before: alice
	// after: bob
}
