package lemonfig_test

import (
	"fmt"
	"testing"

	"github.com/lemonberrylabs/lemonfig"
)

func TestTestVal_String(t *testing.T) {
	v := lemonfig.TestVal("hello")
	if got := v.Get(); got != "hello" {
		t.Errorf("Get() = %q, want %q", got, "hello")
	}
}

func TestTestVal_Struct(t *testing.T) {
	type DB struct {
		Host string
		Port int
	}
	want := DB{Host: "localhost", Port: 5432}
	v := lemonfig.TestVal(want)
	if got := v.Get(); got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}
}

func TestTestVal_ZeroValue(t *testing.T) {
	v := lemonfig.TestVal(0)
	if got := v.Get(); got != 0 {
		t.Errorf("Get() = %d, want 0", got)
	}
}

func TestTestVal_Map(t *testing.T) {
	type Config struct {
		Host string
		Port int
	}
	cfg := lemonfig.TestVal(Config{Host: "localhost", Port: 8080})
	port := lemonfig.Map(cfg, func(c Config) (int, error) { return c.Port, nil })
	host := lemonfig.Map(cfg, func(c Config) (string, error) { return c.Host, nil })

	if got := port.Get(); got != 8080 {
		t.Errorf("port.Get() = %d, want 8080", got)
	}
	if got := host.Get(); got != "localhost" {
		t.Errorf("host.Get() = %q, want %q", got, "localhost")
	}
}

func TestTestVal_MapChain(t *testing.T) {
	cfg := lemonfig.TestVal(3000)
	addr := lemonfig.Map(cfg, func(p int) (string, error) {
		return fmt.Sprintf(":%d", p), nil
	})
	msg := lemonfig.Map(addr, func(a string) (string, error) {
		return "listening on " + a, nil
	})

	if got := msg.Get(); got != "listening on :3000" {
		t.Errorf("msg.Get() = %q, want %q", got, "listening on :3000")
	}
}

func TestTestVal_MapWithCleanup(t *testing.T) {
	v := lemonfig.TestVal("data")
	mapped := lemonfig.MapWithCleanup(v,
		func(s string) (string, error) { return "processed:" + s, nil },
		func(old string) {},
	)

	if got := mapped.Get(); got != "processed:data" {
		t.Errorf("Get() = %q, want %q", got, "processed:data")
	}
}

func TestTestVal_Combine(t *testing.T) {
	type Config struct {
		Host string
		Port int
	}
	cfg := lemonfig.TestVal(Config{Host: "db.local", Port: 5432})
	host := lemonfig.Map(cfg, func(c Config) (string, error) { return c.Host, nil })
	port := lemonfig.Map(cfg, func(c Config) (int, error) { return c.Port, nil })

	addr := lemonfig.Combine(host, port, func(h string, p int) (string, error) {
		return fmt.Sprintf("%s:%d", h, p), nil
	})

	if got := addr.Get(); got != "db.local:5432" {
		t.Errorf("addr.Get() = %q, want %q", got, "db.local:5432")
	}
}

func TestTestVal_Combine3(t *testing.T) {
	type Config struct {
		Scheme string
		Host   string
		Port   int
	}
	cfg := lemonfig.TestVal(Config{Scheme: "https", Host: "api.example.com", Port: 443})
	scheme := lemonfig.Map(cfg, func(c Config) (string, error) { return c.Scheme, nil })
	host := lemonfig.Map(cfg, func(c Config) (string, error) { return c.Host, nil })
	port := lemonfig.Map(cfg, func(c Config) (int, error) { return c.Port, nil })

	url := lemonfig.Combine3(scheme, host, port, func(s, h string, p int) (string, error) {
		return fmt.Sprintf("%s://%s:%d", s, h, p), nil
	})

	if got := url.Get(); got != "https://api.example.com:443" {
		t.Errorf("url.Get() = %q, want %q", got, "https://api.example.com:443")
	}
}

func TestTestVal_TransformPanicsOnError(t *testing.T) {
	v := lemonfig.TestVal("input")

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from failing transform")
		}
	}()
	lemonfig.Map(v, func(string) (int, error) {
		return 0, fmt.Errorf("broken")
	})
}
