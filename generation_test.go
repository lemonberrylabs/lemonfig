package lemonfig_test

import (
	"context"
	"sync"
	"testing"

	"github.com/lemonberrylabs/lemonfig"
)

func TestGeneration_AtomicSwap(t *testing.T) {
	src := newStaticSource("val: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	val := lemonfig.Key[int](mgr, "val")
	mustStart(t, mgr)

	if got := val.Get(); got != 1 {
		t.Fatalf("val = %d, want 1", got)
	}

	// Concurrent reads during reload should never see a partially
	// constructed generation.
	var wg sync.WaitGroup
	ctx := context.Background()

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				got := val.Get()
				if got != 1 && got != 2 {
					t.Errorf("unexpected value %d", got)
				}
			}
		}()
	}

	src.Set("val: 2")
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	wg.Wait()

	if got := val.Get(); got != 2 {
		t.Errorf("val after reload = %d, want 2", got)
	}
}

func TestGeneration_VersionIncrement(t *testing.T) {
	src := newStaticSource("v: 1")
	mgr, err := lemonfig.NewManager(src)
	if err != nil {
		t.Fatal(err)
	}

	_ = lemonfig.Key[int](mgr, "v")
	mustStart(t, mgr)

	ctx := context.Background()
	for i := range 5 {
		src.Set("v: " + string(rune('2'+i)))
		if err := mgr.Reload(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Just verify no panics and reload works repeatedly.
}
