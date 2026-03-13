package lemonfig_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig"
	"github.com/spf13/viper"
)

// staticSource is a test helper that serves static YAML bytes.
type staticSource struct {
	mu   sync.Mutex
	data []byte
}

func newStaticSource(yaml string) *staticSource {
	return &staticSource{data: []byte(yaml)}
}

func (s *staticSource) Set(yaml string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = []byte(yaml)
}

func (s *staticSource) Fetch(_ context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, "yaml", nil
}

// failingSource fails on Fetch.
type failingSource struct {
	fail atomic.Bool
	data []byte
}

func (s *failingSource) Fetch(_ context.Context) ([]byte, string, error) {
	if s.fail.Load() {
		return nil, "", errors.New("fetch error")
	}
	return s.data, "yaml", nil
}

type testConfig struct {
	Name string `mapstructure:"name"`
	Port int    `mapstructure:"port"`
}

func TestLoad(t *testing.T) {
	src := newStaticSource("name: alice\nport: 8080")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	cfg := lemonfig.Load[testConfig](mgr)
	name := lemonfig.Map(cfg, func(c testConfig) (string, error) { return c.Name, nil })

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if got := cfg.Get().Name; got != "alice" {
		t.Errorf("cfg.Name = %q, want %q", got, "alice")
	}
	if got := cfg.Get().Port; got != 8080 {
		t.Errorf("cfg.Port = %d, want %d", got, 8080)
	}
	if got := name.Get(); got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}
}

func TestLoad_Reload(t *testing.T) {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	cfg := lemonfig.Load[testConfig](mgr)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	src.Set("name: bob")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	if got := cfg.Get().Name; got != "bob" {
		t.Errorf("name = %q, want %q", got, "bob")
	}
}

func TestLoad_MapSubField(t *testing.T) {
	src := newStaticSource("name: myapp\nport: 3000")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	cfg := lemonfig.Load[testConfig](mgr)
	port := lemonfig.Map(cfg, func(c testConfig) (int, error) { return c.Port, nil })
	addr := lemonfig.Map(port, func(p int) (string, error) {
		return fmt.Sprintf(":%d", p), nil
	})

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if got := addr.Get(); got != ":3000" {
		t.Errorf("addr = %q, want %q", got, ":3000")
	}

	src.Set("name: myapp\nport: 9090")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := addr.Get(); got != ":9090" {
		t.Errorf("addr = %q, want %q", got, ":9090")
	}
}

func TestNewManager(t *testing.T) {
	src := newStaticSource("name: alice\nport: 8080")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	name := lemonfig.Key[string](mgr, "name")
	port := lemonfig.Key[int](mgr, "port")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if got := name.Get(); got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}
	if got := port.Get(); got != 8080 {
		t.Errorf("port = %d, want %d", got, 8080)
	}
}

func TestReload_Success(t *testing.T) {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	name := lemonfig.Key[string](mgr, "name")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if got := name.Get(); got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}

	src.Set("name: bob")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	if got := name.Get(); got != "bob" {
		t.Errorf("name = %q, want %q", got, "bob")
	}
}

func TestReload_ParseError(t *testing.T) {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	name := lemonfig.Key[string](mgr, "name")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	src.Set(":::invalid yaml")
	err = mgr.Reload(ctx)
	if !errors.Is(err, lemonfig.ErrParseFailed) {
		t.Errorf("expected ErrParseFailed, got %v", err)
	}

	// Old generation preserved.
	if got := name.Get(); got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}
}

func TestReload_ValidationError(t *testing.T) {
	src := newStaticSource("name: alice\nport: 8080")
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithValidation(func(v *viper.Viper) error {
			if v.GetInt("port") <= 0 {
				return errors.New("port must be positive")
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	port := lemonfig.Key[int](mgr, "port")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	src.Set("name: bob\nport: -1")
	err = mgr.Reload(ctx)
	if !errors.Is(err, lemonfig.ErrValidationFailed) {
		t.Errorf("expected ErrValidationFailed, got %v", err)
	}

	if got := port.Get(); got != 8080 {
		t.Errorf("port = %d, want %d", got, 8080)
	}
}

func TestReload_TransformError(t *testing.T) {
	src := newStaticSource("url: good")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	url := lemonfig.Key[string](mgr, "url")
	_ = lemonfig.Map(url, func(u string) (string, error) {
		if u == "bad" {
			return "", errors.New("bad url")
		}
		return "connected:" + u, nil
	})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	src.Set("url: bad")
	err = mgr.Reload(ctx)
	if !errors.Is(err, lemonfig.ErrTransformFailed) {
		t.Errorf("expected ErrTransformFailed, got %v", err)
	}

	// Old generation preserved.
	if got := url.Get(); got != "good" {
		t.Errorf("url = %q, want %q", got, "good")
	}
}

func TestReload_Atomicity(t *testing.T) {
	// Verify that a Combine node always sees a consistent pair of values,
	// since its inputs are read from a single generation during recompute.
	src := newStaticSource("a: 1\nb: 2")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")

	// Combine captures both values atomically within a single generation.
	var inconsistent atomic.Bool
	sum := lemonfig.Combine(a, b, func(av, bv int) (int, error) {
		s := av + bv
		// Only valid sums: 3 (1+2) or 30 (10+20).
		if s != 3 && s != 30 {
			inconsistent.Store(true)
		}
		return s, nil
	})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if got := sum.Get(); got != 3 {
		t.Errorf("sum = %d, want 3", got)
	}

	src.Set("a: 10\nb: 20")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	if got := sum.Get(); got != 30 {
		t.Errorf("sum = %d, want 30", got)
	}
	if inconsistent.Load() {
		t.Error("combine function saw inconsistent values from different generations")
	}

	// Also verify concurrent reads see valid values.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					got := sum.Get()
					if got != 3 && got != 30 {
						inconsistent.Store(true)
					}
				}
			}
		}()
	}

	// Reload back to original.
	src.Set("a: 1\nb: 2")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	close(stop)
	wg.Wait()

	if inconsistent.Load() {
		t.Error("concurrent reads saw inconsistent combined value")
	}
}

func TestStart_FreezeRegistration(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on registering after Start")
		}
	}()
	lemonfig.Key[int](mgr, "y")
}

func TestOnReload_Callback(t *testing.T) {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[string](mgr, "name")

	var called atomic.Bool
	var oldName, newName string
	mgr.OnReload(func(old, new_ *viper.Viper) {
		oldName = old.GetString("name")
		newName = new_.GetString("name")
		called.Store(true)
	})

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	src.Set("name: bob")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	if !called.Load() {
		t.Fatal("OnReload callback was not called")
	}
	if oldName != "alice" {
		t.Errorf("oldName = %q, want %q", oldName, "alice")
	}
	if newName != "bob" {
		t.Errorf("newName = %q, want %q", newName, "bob")
	}
}

func TestReload_FetchError(t *testing.T) {
	src := &failingSource{data: []byte("name: alice")}
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	name := lemonfig.Key[string](mgr, "name")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	src.fail.Store(true)
	err = mgr.Reload(ctx)
	if !errors.Is(err, lemonfig.ErrFetchFailed) {
		t.Errorf("expected ErrFetchFailed, got %v", err)
	}

	if got := name.Get(); got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}
}

func TestStart_AlreadyStarted(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if err := mgr.Start(ctx); !errors.Is(err, lemonfig.ErrAlreadyStarted) {
		t.Errorf("expected ErrAlreadyStarted, got %v", err)
	}
}

func TestStop_NotStarted(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(); !errors.Is(err, lemonfig.ErrNotStarted) {
		t.Errorf("expected ErrNotStarted, got %v", err)
	}
}

func TestReload_NoChangeNoCleanup(t *testing.T) {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src, lemonfig.WithCleanupGrace(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	name := lemonfig.Key[string](mgr, "name")
	var cleaned atomic.Bool
	_ = lemonfig.MapWithCleanup(name,
		func(n string) (string, error) { return "hi " + n, nil },
		func(old string) { cleaned.Store(true) },
	)

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Reload with same data — no change should mean no cleanup.
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	if cleaned.Load() {
		t.Error("cleanup should not have been called when value didn't change")
	}
}
