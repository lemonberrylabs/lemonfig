package lemonfig_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lemonberrylabs/lemonfig"
	"github.com/spf13/viper"
)

func TestWithViperConfigure_DefaultsAndEnv(t *testing.T) {
	t.Setenv("LFTEST_SERVER_HOST", "from-env")

	src := newStaticSource("server:\n  port: 1234\n")
	mgr, err := lemonfig.NewManager(src, lemonfig.WithViperConfigure(func(v *viper.Viper) {
		v.SetDefault("server.port", 9999)      // file must win
		v.SetDefault("server.name", "default") // default must show through
		v.SetEnvPrefix("LFTEST")
		v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
		v.AutomaticEnv()
	}))
	if err != nil {
		t.Fatal(err)
	}

	port := lemonfig.Key[int](mgr, "server.port")
	name := lemonfig.Key[string](mgr, "server.name")
	host := lemonfig.Key[string](mgr, "server.host")

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop() }()

	if got := port.Get(); got != 1234 {
		t.Errorf("file value should win over default: got %d, want 1234", got)
	}
	if got := name.Get(); got != "default" {
		t.Errorf("default should apply: got %q, want %q", got, "default")
	}
	if got := host.Get(); got != "from-env" {
		t.Errorf("env override should apply: got %q, want %q", got, "from-env")
	}

	// Reload must re-apply configuration to the fresh Viper.
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := name.Get(); got != "default" {
		t.Errorf("default should survive reload: got %q", got)
	}
	if got := host.Get(); got != "from-env" {
		t.Errorf("env override should survive reload: got %q", got)
	}
}

func TestWithViperConfigure_MultipleRunInOrder(t *testing.T) {
	src := newStaticSource("a: 1\n")
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithViperConfigure(func(v *viper.Viper) { v.SetDefault("b", "first") }),
		lemonfig.WithViperConfigure(func(v *viper.Viper) { v.SetDefault("b", "second") }),
	)
	if err != nil {
		t.Fatal(err)
	}
	b := lemonfig.Key[string](mgr, "b")
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop() }()

	// Viper keeps the last SetDefault for a key; later options run after
	// earlier ones, so the second registration wins.
	if got := b.Get(); got != "second" {
		t.Errorf("later configure func should run last: got %q, want %q", got, "second")
	}
}

func TestWithViperConfigure_StructUnmarshalSeesDefaults(t *testing.T) {
	type cfg struct {
		Name string `mapstructure:"name"`
		Port int    `mapstructure:"port"`
	}

	src := newStaticSource("port: 7\n")
	mgr, err := lemonfig.NewManager(src, lemonfig.WithViperConfigure(func(v *viper.Viper) {
		v.SetDefault("name", "svc")
	}))
	if err != nil {
		t.Fatal(err)
	}
	c := lemonfig.Load[cfg](mgr)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Stop() }()

	got := c.Get()
	if got.Name != "svc" || got.Port != 7 {
		t.Errorf("unmarshal should merge defaults with file: got %+v", got)
	}
}
