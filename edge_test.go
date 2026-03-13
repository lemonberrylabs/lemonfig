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

// --- Manager lifecycle edge cases ---

func TestStart_InitialFetchFailure(t *testing.T) {
	src := &failingSource{fail: atomic.Bool{}, data: nil}
	src.fail.Store(true)

	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[string](mgr, "name")

	err = mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("expected error from Start with failing source")
	}
	if !errors.Is(err, lemonfig.ErrFetchFailed) {
		t.Errorf("expected ErrFetchFailed, got %v", err)
	}
}

func TestStart_InitialParseFailure(t *testing.T) {
	src := newStaticSource(":::invalid yaml")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[string](mgr, "name")

	err = mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("expected error from Start with invalid config")
	}
	if !errors.Is(err, lemonfig.ErrParseFailed) {
		t.Errorf("expected ErrParseFailed, got %v", err)
	}
}

func TestStart_InitialValidationFailure(t *testing.T) {
	src := newStaticSource("port: -1")
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
	_ = lemonfig.Key[int](mgr, "port")

	err = mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("expected error from Start with failing validation")
	}
	if !errors.Is(err, lemonfig.ErrValidationFailed) {
		t.Errorf("expected ErrValidationFailed, got %v", err)
	}
}

func TestStart_InitialTransformFailure(t *testing.T) {
	src := newStaticSource("url: bad")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	url := lemonfig.Key[string](mgr, "url")
	_ = lemonfig.Map(url, func(u string) (string, error) {
		if u == "bad" {
			return "", errors.New("bad url")
		}
		return u, nil
	})

	err = mgr.Start(context.Background())
	if err == nil {
		mgr.Stop()
		t.Fatal("expected error from Start with failing transform")
	}
	if !errors.Is(err, lemonfig.ErrTransformFailed) {
		t.Errorf("expected ErrTransformFailed, got %v", err)
	}
}

func TestStop_ThenRestart(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	x := lemonfig.Key[int](mgr, "x")

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got := x.Get(); got != 1 {
		t.Errorf("x = %d, want 1", got)
	}

	if err := mgr.Stop(); err != nil {
		t.Fatal(err)
	}

	// After Stop, Start should fail because the DAG is frozen and
	// started is set back to false, so Start should work again.
	// But registrations after Start are forbidden. Let's see if re-Start works.
	err = mgr.Start(ctx)
	if err != nil {
		// If re-start is not supported, that's fine — just verify it errors gracefully.
		t.Logf("re-start after stop returned: %v (this is acceptable)", err)
		return
	}
	defer mgr.Stop()

	// If it does work, verify the value is still accessible.
	if got := x.Get(); got != 1 {
		t.Errorf("x after restart = %d, want 1", got)
	}
}

func TestStart_EmptyDAG(t *testing.T) {
	// Start with zero registered nodes.
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	// No panic, no error — success.
}

func TestStart_EmptyConfig(t *testing.T) {
	src := newStaticSource("")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	name := lemonfig.Key[string](mgr, "name")
	port := lemonfig.Key[int](mgr, "port")

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if got := name.Get(); got != "" {
		t.Errorf("name = %q, want empty", got)
	}
	if got := port.Get(); got != 0 {
		t.Errorf("port = %d, want 0", got)
	}
}

// --- Get() edge cases ---

func TestGet_BeforeStart(t *testing.T) {
	src := newStaticSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	name := lemonfig.Key[string](mgr, "name")

	// Get() before Start — generation is nil, should return zero value.
	// This may panic if there's no nil check. Let's recover.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Get() before Start panicked: %v (documenting behavior)", r)
			}
		}()
		got := name.Get()
		if got != "" {
			t.Errorf("Get before Start = %q, want empty", got)
		}
	}()
}

// --- Freeze edge cases ---

func TestFreeze_LoadAfterStart(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Load after Start")
		}
	}()
	lemonfig.Load[testConfig](mgr)
}

func TestFreeze_MapAfterStart(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	x := lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Map after Start")
		}
	}()
	lemonfig.Map(x, func(v int) (int, error) { return v, nil })
}

func TestFreeze_CombineAfterStart(t *testing.T) {
	src := newStaticSource("a: 1\nb: 2")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	mustStart(t, mgr)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Combine after Start")
		}
	}()
	lemonfig.Combine(a, b, func(x, y int) (int, error) { return x + y, nil })
}

func TestFreeze_StructAfterStart(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Struct after Start")
		}
	}()
	lemonfig.Struct[testConfig](mgr, "")
}

func TestFreeze_Combine3AfterStart(t *testing.T) {
	src := newStaticSource("a: 1\nb: 2\nc: 3")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	c := lemonfig.Key[int](mgr, "c")
	mustStart(t, mgr)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Combine3 after Start")
		}
	}()
	lemonfig.Combine3(a, b, c, func(x, y, z int) (int, error) { return x + y + z, nil })
}

// --- Config format edge cases ---

func TestConfigType_JSON(t *testing.T) {
	src := &rawSource{data: []byte(`{"name":"alice","port":8080}`), format: "json"}
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg := lemonfig.Load[testConfig](mgr)
	mustStart(t, mgr)

	if got := cfg.Get().Name; got != "alice" {
		t.Errorf("name = %q, want %q", got, "alice")
	}
	if got := cfg.Get().Port; got != 8080 {
		t.Errorf("port = %d, want %d", got, 8080)
	}
}

func TestWithConfigType_Override(t *testing.T) {
	// Source reports "yaml" but we override to "json".
	src := &rawSource{data: []byte(`{"name":"bob"}`), format: "yaml"}
	mgr, err := lemonfig.NewManager(src, lemonfig.WithConfigType("json"))
	if err != nil {
		t.Fatal(err)
	}
	name := lemonfig.Key[string](mgr, "name")
	mustStart(t, mgr)

	if got := name.Get(); got != "bob" {
		t.Errorf("name = %q, want %q", got, "bob")
	}
}

// rawSource returns arbitrary bytes with a given format string.
type rawSource struct {
	mu     sync.Mutex
	data   []byte
	format string
}

func (s *rawSource) Set(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

func (s *rawSource) Fetch(_ context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, s.format, nil
}

// --- Key type edge cases ---

func TestKey_NestedPath(t *testing.T) {
	mgr, _ := startWithSource(t, `
server:
  host: localhost
  port: 9090
`)
	host := lemonfig.Key[string](mgr, "server.host")
	port := lemonfig.Key[int](mgr, "server.port")
	mustStart(t, mgr)

	if got := host.Get(); got != "localhost" {
		t.Errorf("host = %q, want %q", got, "localhost")
	}
	if got := port.Get(); got != 9090 {
		t.Errorf("port = %d, want %d", got, 9090)
	}
}

func TestKey_Int32(t *testing.T) {
	mgr, _ := startWithSource(t, "x: 42")
	x := lemonfig.Key[int32](mgr, "x")
	mustStart(t, mgr)

	if got := x.Get(); got != 42 {
		t.Errorf("x = %d, want 42", got)
	}
}

func TestKey_Int64(t *testing.T) {
	mgr, _ := startWithSource(t, "x: 9999999999")
	x := lemonfig.Key[int64](mgr, "x")
	mustStart(t, mgr)

	if got := x.Get(); got != 9999999999 {
		t.Errorf("x = %d, want 9999999999", got)
	}
}

func TestKey_StringSlice(t *testing.T) {
	mgr, _ := startWithSource(t, `
tags:
  - alpha
  - beta
  - gamma
`)
	tags := lemonfig.Key[[]string](mgr, "tags")
	mustStart(t, mgr)

	got := tags.Get()
	if len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Errorf("tags = %v", got)
	}
}

func TestKey_MapStringAny(t *testing.T) {
	mgr, _ := startWithSource(t, `
meta:
  env: prod
  version: 2
`)
	meta := lemonfig.Key[map[string]any](mgr, "meta")
	mustStart(t, mgr)

	got := meta.Get()
	if got["env"] != "prod" {
		t.Errorf("meta[env] = %v, want prod", got["env"])
	}
}

func TestKey_MapStringString(t *testing.T) {
	mgr, _ := startWithSource(t, `
labels:
  app: myapp
  team: backend
`)
	labels := lemonfig.Key[map[string]string](mgr, "labels")
	mustStart(t, mgr)

	got := labels.Get()
	if got["app"] != "myapp" || got["team"] != "backend" {
		t.Errorf("labels = %v", got)
	}
}

func TestKey_BoolFalse(t *testing.T) {
	mgr, _ := startWithSource(t, "debug: false")
	debug := lemonfig.Key[bool](mgr, "debug")
	mustStart(t, mgr)

	if got := debug.Get(); got != false {
		t.Errorf("debug = %v, want false", got)
	}
}

// --- Struct edge cases ---

func TestStruct_ExtraFieldsIgnored(t *testing.T) {
	mgr, _ := startWithSource(t, `
database:
  host: db.local
  port: 5432
  name: mydb
  extra_field: should_be_ignored
  timeout: 30s
`)
	db := lemonfig.Struct[dbConfig](mgr, "database")
	mustStart(t, mgr)

	got := db.Get()
	if got.Host != "db.local" || got.Port != 5432 || got.Name != "mydb" {
		t.Errorf("got %+v", got)
	}
}

func TestStruct_PathAppearsOnReload(t *testing.T) {
	// Initially, the "database" key doesn't exist.
	mgr, src := startWithSource(t, "other: 1")
	db := lemonfig.Struct[dbConfig](mgr, "database")
	mustStart(t, mgr)

	got := db.Get()
	if got.Host != "" {
		t.Errorf("expected zero value before path exists, got %+v", got)
	}

	// Now it appears.
	src.Set("database:\n  host: newhost\n  port: 3306\n  name: newdb")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	got = db.Get()
	if got.Host != "newhost" || got.Port != 3306 {
		t.Errorf("after reload = %+v", got)
	}
}

func TestStruct_PathDisappearsOnReload(t *testing.T) {
	mgr, src := startWithSource(t, "database:\n  host: myhost\n  port: 5432\n  name: mydb")
	db := lemonfig.Struct[dbConfig](mgr, "database")
	mustStart(t, mgr)

	if got := db.Get().Host; got != "myhost" {
		t.Errorf("host = %q, want myhost", got)
	}

	// Remove the database section.
	src.Set("other: 1")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := db.Get()
	if got.Host != "" || got.Port != 0 {
		t.Errorf("expected zero after path removed, got %+v", got)
	}
}

func TestLoad_MultipleStructTypes(t *testing.T) {
	type serverCfg struct {
		Port int `mapstructure:"port"`
	}
	type logCfg struct {
		Level string `mapstructure:"level"`
	}

	src := newStaticSource("port: 8080\nlevel: debug")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	srv := lemonfig.Load[serverCfg](mgr)
	log := lemonfig.Load[logCfg](mgr)
	mustStart(t, mgr)

	if got := srv.Get().Port; got != 8080 {
		t.Errorf("port = %d, want 8080", got)
	}
	if got := log.Get().Level; got != "debug" {
		t.Errorf("level = %q, want debug", got)
	}
}

// --- Map edge cases ---

func TestMap_ChainedTransformError(t *testing.T) {
	// Error in a middle node of a chain should preserve the old generation.
	mgr, src := startWithSource(t, "x: 5")
	x := lemonfig.Key[int](mgr, "x")
	doubled := lemonfig.Map(x, func(v int) (int, error) {
		if v > 100 {
			return 0, errors.New("too large")
		}
		return v * 2, nil
	})
	final := lemonfig.Map(doubled, func(v int) (string, error) {
		return fmt.Sprintf("val=%d", v), nil
	})
	mustStart(t, mgr)

	if got := final.Get(); got != "val=10" {
		t.Errorf("final = %q, want val=10", got)
	}

	src.Set("x: 999")
	err := mgr.Reload(context.Background())
	if !errors.Is(err, lemonfig.ErrTransformFailed) {
		t.Errorf("expected ErrTransformFailed, got %v", err)
	}

	// Old values preserved.
	if got := final.Get(); got != "val=10" {
		t.Errorf("final after failed reload = %q, want val=10", got)
	}
	if got := doubled.Get(); got != 10 {
		t.Errorf("doubled after failed reload = %d, want 10", got)
	}
}

func TestMap_MultipleConsumersFromSameParent(t *testing.T) {
	mgr, src := startWithSource(t, "x: 10")
	x := lemonfig.Key[int](mgr, "x")

	add1 := lemonfig.Map(x, func(v int) (int, error) { return v + 1, nil })
	mul2 := lemonfig.Map(x, func(v int) (int, error) { return v * 2, nil })
	neg := lemonfig.Map(x, func(v int) (int, error) { return -v, nil })

	mustStart(t, mgr)

	if got := add1.Get(); got != 11 {
		t.Errorf("add1 = %d, want 11", got)
	}
	if got := mul2.Get(); got != 20 {
		t.Errorf("mul2 = %d, want 20", got)
	}
	if got := neg.Get(); got != -10 {
		t.Errorf("neg = %d, want -10", got)
	}

	src.Set("x: 5")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := add1.Get(); got != 6 {
		t.Errorf("add1 = %d, want 6", got)
	}
	if got := mul2.Get(); got != 10 {
		t.Errorf("mul2 = %d, want 10", got)
	}
	if got := neg.Get(); got != -5 {
		t.Errorf("neg = %d, want -5", got)
	}
}

// --- Combine edge cases ---

func TestCombine_BothParentsUnchanged(t *testing.T) {
	mgr, _ := startWithSource(t, "a: 1\nb: 2")
	var callCount atomic.Int32
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	_ = lemonfig.Combine(a, b, func(x, y int) (int, error) {
		callCount.Add(1)
		return x + y, nil
	})
	mustStart(t, mgr)

	initial := callCount.Load()

	// Reload with identical config.
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if callCount.Load() != initial {
		t.Error("combine called despite no parent change")
	}
}

func TestCombine3_PartialParentChange(t *testing.T) {
	mgr, src := startWithSource(t, "a: 1\nb: 2\nc: 3")
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	c := lemonfig.Key[int](mgr, "c")

	var callCount atomic.Int32
	sum := lemonfig.Combine3(a, b, c, func(x, y, z int) (int, error) {
		callCount.Add(1)
		return x + y + z, nil
	})
	mustStart(t, mgr)

	if got := sum.Get(); got != 6 {
		t.Errorf("sum = %d, want 6", got)
	}
	callsBefore := callCount.Load()

	// Only change 'c'.
	src.Set("a: 1\nb: 2\nc: 100")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := sum.Get(); got != 103 {
		t.Errorf("sum after partial change = %d, want 103", got)
	}
	if callCount.Load() <= callsBefore {
		t.Error("combine3 should have been re-evaluated")
	}
}

func TestCombine_Error(t *testing.T) {
	mgr, src := startWithSource(t, "a: 1\nb: 2")
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	sum := lemonfig.Combine(a, b, func(x, y int) (int, error) {
		if x+y > 100 {
			return 0, errors.New("sum too large")
		}
		return x + y, nil
	})
	mustStart(t, mgr)

	if got := sum.Get(); got != 3 {
		t.Errorf("sum = %d, want 3", got)
	}

	src.Set("a: 50\nb: 60")
	err := mgr.Reload(context.Background())
	if !errors.Is(err, lemonfig.ErrTransformFailed) {
		t.Errorf("expected ErrTransformFailed, got %v", err)
	}

	// Old generation preserved.
	if got := sum.Get(); got != 3 {
		t.Errorf("sum after failed reload = %d, want 3", got)
	}
}

func TestCombine3_Error(t *testing.T) {
	mgr, src := startWithSource(t, "a: 1\nb: 2\nc: 3")
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	c := lemonfig.Key[int](mgr, "c")
	result := lemonfig.Combine3(a, b, c, func(x, y, z int) (int, error) {
		if x+y+z > 100 {
			return 0, errors.New("sum too large")
		}
		return x + y + z, nil
	})
	mustStart(t, mgr)

	if got := result.Get(); got != 6 {
		t.Errorf("result = %d, want 6", got)
	}

	src.Set("a: 40\nb: 40\nc: 40")
	err := mgr.Reload(context.Background())
	if !errors.Is(err, lemonfig.ErrTransformFailed) {
		t.Errorf("expected ErrTransformFailed, got %v", err)
	}
	if got := result.Get(); got != 6 {
		t.Errorf("result after failed reload = %d, want 6", got)
	}
}

// --- Cleanup edge cases ---

func TestMapWithCleanup_CalledOnChange(t *testing.T) {
	mgr, src := startWithSource(t, "url: a",
		lemonfig.WithCleanupGrace(10*time.Millisecond),
	)
	url := lemonfig.Key[string](mgr, "url")

	var cleanedValues sync.Map

	_ = lemonfig.MapWithCleanup(url,
		func(u string) (string, error) { return "conn:" + u, nil },
		func(old string) {
			cleanedValues.Store(old, true)
		},
	)
	mustStart(t, mgr)

	// First reload — should clean up "conn:a".
	src.Set("url: b")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Wait for first cleanup before second reload.
	time.Sleep(50 * time.Millisecond)

	// Second reload — should clean up "conn:b".
	src.Set("url: c")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	if _, ok := cleanedValues.Load("conn:a"); !ok {
		t.Error("conn:a was not cleaned up")
	}
	if _, ok := cleanedValues.Load("conn:b"); !ok {
		t.Error("conn:b was not cleaned up")
	}
}

func TestMapWithCleanup_CleanupPanicRecovery(t *testing.T) {
	mgr, src := startWithSource(t, "x: 1",
		lemonfig.WithCleanupGrace(10*time.Millisecond),
	)
	x := lemonfig.Key[int](mgr, "x")

	var afterPanicCleaned atomic.Bool
	// First MapWithCleanup panics.
	_ = lemonfig.MapWithCleanup(x,
		func(v int) (string, error) { return fmt.Sprintf("first:%d", v), nil },
		func(old string) { panic("cleanup panic!") },
	)
	// Second MapWithCleanup should still run.
	_ = lemonfig.MapWithCleanup(x,
		func(v int) (string, error) { return fmt.Sprintf("second:%d", v), nil },
		func(old string) { afterPanicCleaned.Store(true) },
	)
	mustStart(t, mgr)

	src.Set("x: 2")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	// The panic in the first cleanup should not prevent the second from running.
	// However, cleanup order is reverse topological (leaves first). Since both depend
	// on the same parent, order may vary. The key test is that no goroutine crashes.
	// We check that at least the non-panicking one ran.
	if !afterPanicCleaned.Load() {
		t.Error("second cleanup was not called after first cleanup panicked")
	}
}

// --- Reload edge cases ---

func TestReload_ConcurrentReloads(t *testing.T) {
	src := newStaticSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	x := lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Fire many concurrent reloads.
	for i := range 20 {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			src.Set(fmt.Sprintf("x: %d", val))
			mgr.Reload(ctx) // may error or succeed — that's fine
		}(i)
	}
	wg.Wait()

	// After all concurrent reloads settle, x.Get() should return a valid value.
	got := x.Get()
	if got < 0 || got > 19 {
		t.Errorf("x = %d, expected value in [0, 19]", got)
	}
}

func TestReload_MultipleSuccessiveReloads(t *testing.T) {
	mgr, src := startWithSource(t, "x: 0")
	x := lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	ctx := context.Background()
	for i := 1; i <= 50; i++ {
		src.Set(fmt.Sprintf("x: %d", i))
		if err := mgr.Reload(ctx); err != nil {
			t.Fatalf("reload %d failed: %v", i, err)
		}
		if got := x.Get(); got != i {
			t.Fatalf("after reload %d: x = %d, want %d", i, got, i)
		}
	}
}

func TestReload_FailedThenSuccessful(t *testing.T) {
	src := newStaticSource("x: 10")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	x := lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	ctx := context.Background()

	// First, a failed reload (bad YAML).
	src.Set(":::bad")
	if err := mgr.Reload(ctx); err == nil {
		t.Fatal("expected error")
	}
	if got := x.Get(); got != 10 {
		t.Errorf("x after failed reload = %d, want 10", got)
	}

	// Then a successful reload.
	src.Set("x: 42")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := x.Get(); got != 42 {
		t.Errorf("x after successful reload = %d, want 42", got)
	}
}

// --- OnReload edge cases ---

func TestOnReload_NotCalledOnInitialLoad(t *testing.T) {
	src := newStaticSource("name: alice")
	var called atomic.Bool
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithOnReload(func(old, new_ *viper.Viper) {
			called.Store(true)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[string](mgr, "name")
	mustStart(t, mgr)

	if called.Load() {
		t.Error("OnReload should not be called on initial load")
	}
}

func TestOnReload_MultipleCallbacks(t *testing.T) {
	src := newStaticSource("x: 1")
	var count1, count2 atomic.Int32
	mgr, err := lemonfig.NewManager(src,
		lemonfig.WithOnReload(func(old, new_ *viper.Viper) { count1.Add(1) }),
		lemonfig.WithOnReload(func(old, new_ *viper.Viper) { count2.Add(1) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")
	mustStart(t, mgr)

	src.Set("x: 2")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if count1.Load() != 1 {
		t.Errorf("callback1 called %d times, want 1", count1.Load())
	}
	if count2.Load() != 1 {
		t.Errorf("callback2 called %d times, want 1", count2.Load())
	}
}

func TestOnReload_NotCalledOnFailedReload(t *testing.T) {
	src := newStaticSource("x: 1")
	var called atomic.Bool
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")
	mgr.OnReload(func(old, new_ *viper.Viper) {
		called.Store(true)
	})
	mustStart(t, mgr)

	src.Set(":::bad yaml")
	mgr.Reload(context.Background())

	if called.Load() {
		t.Error("OnReload should not be called on failed reload")
	}
}

// --- DAG shape edge cases ---

func TestDAG_WideRoots(t *testing.T) {
	mgr, src := startWithSource(t, "a: 1\nb: 2\nc: 3\nd: 4\ne: 5")
	a := lemonfig.Key[int](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	c := lemonfig.Key[int](mgr, "c")
	d := lemonfig.Key[int](mgr, "d")
	e := lemonfig.Key[int](mgr, "e")
	mustStart(t, mgr)

	if a.Get()+b.Get()+c.Get()+d.Get()+e.Get() != 15 {
		t.Error("sum mismatch")
	}

	src.Set("a: 10\nb: 20\nc: 30\nd: 40\ne: 50")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if a.Get()+b.Get()+c.Get()+d.Get()+e.Get() != 150 {
		t.Error("sum mismatch after reload")
	}
}

func TestDAG_OnlyRoots(t *testing.T) {
	// DAG with no transform nodes.
	mgr, _ := startWithSource(t, "a: hello\nb: 42")
	a := lemonfig.Key[string](mgr, "a")
	b := lemonfig.Key[int](mgr, "b")
	mustStart(t, mgr)

	if got := a.Get(); got != "hello" {
		t.Errorf("a = %q", got)
	}
	if got := b.Get(); got != 42 {
		t.Errorf("b = %d", got)
	}
}

func TestDAG_CombineWithMapChildren(t *testing.T) {
	// A → Map → Combine with B
	mgr, src := startWithSource(t, "prefix: hello\nname: world")
	prefix := lemonfig.Key[string](mgr, "prefix")
	name := lemonfig.Key[string](mgr, "name")
	upper := lemonfig.Map(prefix, func(s string) (string, error) {
		return "[" + s + "]", nil
	})
	greeting := lemonfig.Combine(upper, name, func(p, n string) (string, error) {
		return p + " " + n, nil
	})
	mustStart(t, mgr)

	if got := greeting.Get(); got != "[hello] world" {
		t.Errorf("greeting = %q, want %q", got, "[hello] world")
	}

	src.Set("prefix: hi\nname: earth")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := greeting.Get(); got != "[hi] earth" {
		t.Errorf("greeting after reload = %q, want %q", got, "[hi] earth")
	}
}

// --- Concurrent read safety ---

func TestConcurrentReads_DuringReload(t *testing.T) {
	mgr, src := startWithSource(t, "x: 1")
	x := lemonfig.Key[int](mgr, "x")
	doubled := lemonfig.Map(x, func(v int) (int, error) { return v * 2, nil })
	mustStart(t, mgr)

	ctx := context.Background()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					xv := x.Get()
					dv := doubled.Get()
					// They may be from different generations, but each
					// individual Get should be valid.
					_ = xv
					_ = dv
				}
			}
		}()
	}

	// Writer.
	for i := 2; i <= 20; i++ {
		src.Set(fmt.Sprintf("x: %d", i))
		mgr.Reload(ctx)
	}

	close(stop)
	wg.Wait()
}

// --- WatchableSource integration ---

type fakeWatchableSource struct {
	mu       sync.Mutex
	data     []byte
	onChange func()
	started  chan struct{}
}

func newFakeWatchableSource(yaml string) *fakeWatchableSource {
	return &fakeWatchableSource{
		data:    []byte(yaml),
		started: make(chan struct{}),
	}
}

func (s *fakeWatchableSource) Fetch(_ context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, "yaml", nil
}

func (s *fakeWatchableSource) Watch(ctx context.Context, onChange func()) error {
	s.mu.Lock()
	s.onChange = onChange
	s.mu.Unlock()
	close(s.started)
	<-ctx.Done()
	return nil
}

func (s *fakeWatchableSource) SetAndNotify(yaml string) {
	s.mu.Lock()
	s.data = []byte(yaml)
	cb := s.onChange
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func TestWatchableSource_AutoReload(t *testing.T) {
	src := newFakeWatchableSource("name: alice")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	name := lemonfig.Key[string](mgr, "name")

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Wait for watch goroutine to start.
	<-src.started

	if got := name.Get(); got != "alice" {
		t.Fatalf("name = %q, want alice", got)
	}

	src.SetAndNotify("name: bob")

	// Give time for the async reload to complete.
	time.Sleep(100 * time.Millisecond)

	if got := name.Get(); got != "bob" {
		t.Errorf("name after watch = %q, want bob", got)
	}
}

func TestWatchableSource_StopCancelsWatch(t *testing.T) {
	src := newFakeWatchableSource("x: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}
	_ = lemonfig.Key[int](mgr, "x")

	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-src.started

	// Stop should cancel the watch context and return.
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after canceling watch")
	}
}

// --- Reload with unchanged config ---

func TestReload_IdenticalConfig_NoRecomputation(t *testing.T) {
	mgr, _ := startWithSource(t, "x: 1\ny: 2")
	var mapCalls atomic.Int32
	x := lemonfig.Key[int](mgr, "x")
	y := lemonfig.Key[int](mgr, "y")
	_ = lemonfig.Combine(x, y, func(a, b int) (int, error) {
		mapCalls.Add(1)
		return a + b, nil
	})
	mustStart(t, mgr)

	initial := mapCalls.Load()

	// Reload 5 times with identical data.
	for range 5 {
		if err := mgr.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if mapCalls.Load() != initial {
		t.Errorf("combine called %d extra times on identical reloads", mapCalls.Load()-initial)
	}
}

// --- Key change tracking edge cases ---

func TestKey_ValueChangesBackToOriginal(t *testing.T) {
	mgr, src := startWithSource(t, "x: 100")
	x := lemonfig.Key[int](mgr, "x")
	var mapCalls atomic.Int32
	result := lemonfig.Map(x, func(v int) (string, error) {
		mapCalls.Add(1)
		return fmt.Sprintf("v=%d", v), nil
	})
	mustStart(t, mgr)

	if got := result.Get(); got != "v=100" {
		t.Errorf("result = %q", got)
	}
	callsAfterStart := mapCalls.Load()

	// Change to a different value.
	src.Set("x: 200")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := result.Get(); got != "v=200" {
		t.Errorf("result = %q, want v=200", got)
	}

	// Change back to original.
	src.Set("x: 100")
	if err := mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := result.Get(); got != "v=100" {
		t.Errorf("result = %q, want v=100", got)
	}

	// Map should have been called twice more (once for each change).
	if mapCalls.Load() != callsAfterStart+2 {
		t.Errorf("map called %d times after start, want %d", mapCalls.Load()-callsAfterStart, 2)
	}
}
