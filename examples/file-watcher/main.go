// file-watcher demonstrates lemonfig with a FileSource watching a YAML config file.
// It writes an initial config, starts watching, then updates the file to show
// hot-reload in action.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/lemonberrylabs/lemonfig"
	"github.com/lemonberrylabs/lemonfig/source"
)

type Config struct {
	AppName string       `mapstructure:"app_name"`
	Server  ServerConfig `mapstructure:"server"`
}

type ServerConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
}

func main() {
	// Write initial config file.
	dir, err := os.MkdirTemp("", "lemonfig-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	cfgPath := filepath.Join(dir, "config.yaml")
	writeConfig(cfgPath, `
app_name: my-service
server:
  host: localhost
  port: 8080
`)

	// Set up lemonfig with file watching.
	src := source.NewFileSource(cfgPath)
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		log.Fatal(err)
	}

	cfg := lemonfig.Load[Config](mgr)
	addr := lemonfig.Map(cfg, func(c Config) (string, error) {
		return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer mgr.Stop()

	fmt.Printf("initial: app=%s addr=%s\n", cfg.Get().AppName, addr.Get())

	// Simulate a config change after a short delay.
	time.AfterFunc(500*time.Millisecond, func() {
		writeConfig(cfgPath, `
app_name: my-service-v2
server:
  host: 0.0.0.0
  port: 9090
`)
	})

	// Poll and print until we see the change.
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			fmt.Println("timed out waiting for config change")
			return
		case <-ticker.C:
			c := cfg.Get()
			if c.Server.Port == 9090 {
				fmt.Printf("reloaded: app=%s addr=%s\n", c.AppName, addr.Get())
				return
			}
		}
	}
}

func writeConfig(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		log.Fatal(err)
	}
}
