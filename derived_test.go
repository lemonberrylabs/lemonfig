package lemonfig_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig"
)

func startWithSource(t *testing.T, yaml string, opts ...lemonfig.Option) (*lemonfig.Manager, *staticSource) {
	t.Helper()
	src := newStaticSource(yaml)
	mgr, err := lemonfig.NewManager(src, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return mgr, src
}

func mustStart(t *testing.T, mgr *lemonfig.Manager) {
	t.Helper()
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Stop() })
}

func TestKey_Primitives(t *testing.T) {
	mgr, _ := startWithSource(t, `
name: alice
port: 8080
debug: true
rate: 3.14
`)
	name := lemonfig.Key[string](mgr, "name")
	port := lemonfig.Key[int](mgr, "port")
	debug := lemonfig.Key[bool](mgr, "debug")
	rate := lemonfig.Key[float64](mgr, "rate")

	mustStart(t, mgr)

	if got := name.Get(); got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}
	if got := port.Get(); got != 8080 {
		t.Errorf("port = %d, want %d", got, 8080)
	}
	if got := debug.Get(); got != true {
		t.Errorf("debug = %v, want true", got)
	}
	if got := rate.Get(); got != 3.14 {
		t.Errorf("rate = %f, want 3.14", got)
	}
}

func TestKey_MissingPath(t *testing.T) {
	mgr, _ := startWithSource(t, "name: alice")
	missing := lemonfig.Key[string](mgr, "nonexistent")
	mustStart(t, mgr)

	if got := missing.Get(); got != "" {
		t.Errorf("missing key = %q, want empty string", got)
	}
}

func TestKey_MissingPathInt(t *testing.T) {
	mgr, _ := startWithSource(t, "name: alice")
	missing := lemonfig.Key[int](mgr, "nonexistent")
	mustStart(t, mgr)

	if got := missing.Get(); got != 0 {
		t.Errorf("missing key = %d, want 0", got)
	}
}

type dbConfig struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	Name string `mapstructure:"name"`
}

func TestStruct_Unmarshal(t *testing.T) {
	mgr, _ := startWithSource(t, `
database:
  host: localhost
  port: 5432
  name: mydb
`)
	db := lemonfig.Struct[dbConfig](mgr, "database")
	mustStart(t, mgr)

	got := db.Get()
	if got.Host != "localhost" || got.Port != 5432 || got.Name != "mydb" {
		t.Errorf("got %+v", got)
	}
}

func TestMap_BasicTransform(t *testing.T) {
	mgr, _ := startWithSource(t, "port: 8080")
	port := lemonfig.Key[int](mgr, "port")
	addr := lemonfig.Map(port, func(p int) (string, error) {
		return fmt.Sprintf(":%d", p), nil
	})
	mustStart(t, mgr)

	if got := addr.Get(); got != ":8080" {
		t.Errorf("addr = %q, want %q", got, ":8080")
	}
}

type mockDB struct {
	URL    string
	Closed bool
}

func TestMap_HeavyResource(t *testing.T) {
	mgr, src := startWithSource(t, "db_url: postgres://localhost/a")
	url := lemonfig.Key[string](mgr, "db_url")
	db := lemonfig.Map(url, func(u string) (*mockDB, error) {
		return &mockDB{URL: u}, nil
	})
	mustStart(t, mgr)

	if got := db.Get().URL; got != "postgres://localhost/a" {
		t.Errorf("db.URL = %q", got)
	}

	src.Set("db_url: postgres://localhost/b")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := db.Get().URL; got != "postgres://localhost/b" {
		t.Errorf("db.URL = %q after reload", got)
	}
}

func TestMap_NoChangeNoPropagation(t *testing.T) {
	mgr, _ := startWithSource(t, "port: 8080")
	var callCount atomic.Int32
	port := lemonfig.Key[int](mgr, "port")
	_ = lemonfig.Map(port, func(p int) (string, error) {
		callCount.Add(1)
		return fmt.Sprintf(":%d", p), nil
	})
	mustStart(t, mgr)

	initial := callCount.Load()

	// Reload with same config.
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if callCount.Load() != initial {
		t.Error("Map function was called again despite no change")
	}
}

func TestMapWithCleanup(t *testing.T) {
	mgr, src := startWithSource(t, "url: a",
		lemonfig.WithCleanupGrace(10*time.Millisecond),
	)
	url := lemonfig.Key[string](mgr, "url")

	var cleaned atomic.Value
	db := lemonfig.MapWithCleanup(url,
		func(u string) (*mockDB, error) { return &mockDB{URL: u}, nil },
		func(old *mockDB) {
			old.Closed = true
			cleaned.Store(old)
		},
	)
	mustStart(t, mgr)

	oldDB := db.Get()

	src.Set("url: b")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Wait for cleanup.
	time.Sleep(50 * time.Millisecond)

	got := cleaned.Load()
	if got == nil {
		t.Fatal("cleanup was not called")
	}
	cleanedDB := got.(*mockDB)
	if cleanedDB.URL != oldDB.URL {
		t.Errorf("cleaned wrong DB: %q vs %q", cleanedDB.URL, oldDB.URL)
	}
	if !cleanedDB.Closed {
		t.Error("old DB was not closed")
	}
}

func TestCombine_TwoParents(t *testing.T) {
	mgr, _ := startWithSource(t, "host: localhost\nport: 8080")
	host := lemonfig.Key[string](mgr, "host")
	port := lemonfig.Key[int](mgr, "port")
	addr := lemonfig.Combine(host, port, func(h string, p int) (string, error) {
		return fmt.Sprintf("%s:%d", h, p), nil
	})
	mustStart(t, mgr)

	if got := addr.Get(); got != "localhost:8080" {
		t.Errorf("addr = %q, want %q", got, "localhost:8080")
	}
}

func TestCombine_OneParentChanges(t *testing.T) {
	mgr, src := startWithSource(t, "host: localhost\nport: 8080")
	host := lemonfig.Key[string](mgr, "host")
	port := lemonfig.Key[int](mgr, "port")
	addr := lemonfig.Combine(host, port, func(h string, p int) (string, error) {
		return fmt.Sprintf("%s:%d", h, p), nil
	})
	mustStart(t, mgr)

	src.Set("host: localhost\nport: 9090")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := addr.Get(); got != "localhost:9090" {
		t.Errorf("addr = %q, want %q", got, "localhost:9090")
	}
}

func TestCombine3(t *testing.T) {
	mgr, _ := startWithSource(t, "scheme: https\nhost: example.com\nport: 443")
	scheme := lemonfig.Key[string](mgr, "scheme")
	host := lemonfig.Key[string](mgr, "host")
	port := lemonfig.Key[int](mgr, "port")
	url := lemonfig.Combine3(scheme, host, port, func(s, h string, p int) (string, error) {
		return fmt.Sprintf("%s://%s:%d", s, h, p), nil
	})
	mustStart(t, mgr)

	if got := url.Get(); got != "https://example.com:443" {
		t.Errorf("url = %q", got)
	}
}

func TestDAG_DeepChain(t *testing.T) {
	mgr, src := startWithSource(t, "x: 1")
	x := lemonfig.Key[int](mgr, "x")
	a := lemonfig.Map(x, func(v int) (int, error) { return v * 2, nil })
	b := lemonfig.Map(a, func(v int) (int, error) { return v + 10, nil })
	c := lemonfig.Map(b, func(v int) (int, error) { return v * 3, nil })
	mustStart(t, mgr)

	// x=1 → a=2 → b=12 → c=36
	if got := c.Get(); got != 36 {
		t.Errorf("c = %d, want 36", got)
	}

	src.Set("x: 5")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	// x=5 → a=10 → b=20 → c=60
	if got := c.Get(); got != 60 {
		t.Errorf("c = %d after reload, want 60", got)
	}
}

func TestDAG_Diamond(t *testing.T) {
	mgr, _ := startWithSource(t, "x: 10")
	var evalCount atomic.Int32
	x := lemonfig.Key[int](mgr, "x")

	// Two independent transforms of the same parent.
	doubled := lemonfig.Map(x, func(v int) (int, error) {
		evalCount.Add(1)
		return v * 2, nil
	})
	tripled := lemonfig.Map(x, func(v int) (int, error) {
		evalCount.Add(1)
		return v * 3, nil
	})
	sum := lemonfig.Combine(doubled, tripled, func(d, t int) (int, error) {
		return d + t, nil
	})
	mustStart(t, mgr)

	// doubled=20, tripled=30, sum=50
	if got := sum.Get(); got != 50 {
		t.Errorf("sum = %d, want 50", got)
	}
	// The two Map functions should each have been called once during initial load.
	if got := evalCount.Load(); got != 2 {
		t.Errorf("evalCount = %d, want 2", got)
	}
}

func TestStruct_MissingPath(t *testing.T) {
	mgr, _ := startWithSource(t, "other: value")
	db := lemonfig.Struct[dbConfig](mgr, "database")
	mustStart(t, mgr)

	got := db.Get()
	if got.Host != "" || got.Port != 0 {
		t.Errorf("expected zero value, got %+v", got)
	}
}
