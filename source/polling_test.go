package source_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig/source"
)

type mockSource struct {
	mu   sync.Mutex
	data []byte
}

func (s *mockSource) Set(data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = []byte(data)
}

func (s *mockSource) Fetch(_ context.Context) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, "yaml", nil
}

func TestPollingSource_Interval(t *testing.T) {
	inner := &mockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called atomic.Bool
	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() error {
			called.Store(true)
			return nil
		})
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	inner.Set("v: 2")

	// Wait for at least one poll cycle.
	time.Sleep(100 * time.Millisecond)

	if !called.Load() {
		t.Error("onChange was not called after config change")
	}

	cancel()
	<-done
}

func TestPollingSource_NoChangeNoCallback(t *testing.T) {
	inner := &mockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() error {
			count.Add(1)
			return nil
		})
		close(done)
	}()

	// Let several polls happen without changing data.
	time.Sleep(200 * time.Millisecond)

	// Exactly the one apply Watch performs at setup; no change-driven calls.
	if count.Load() != 1 {
		t.Errorf("onChange called %d times, expected 1 (setup only)", count.Load())
	}

	cancel()
	<-done
}

func TestPollingSource_ContextCancel(t *testing.T) {
	inner := &mockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() error { return nil })
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Good, Watch exited.
	case <-time.After(time.Second):
		t.Fatal("Watch did not exit after context cancel")
	}
}

// With a change key installed, Watch compares key(content): a key that
// changes while the content does not is a change, and a key error skips the
// tick and is retried on the next one.
func TestPollingSource_ChangeKey(t *testing.T) {
	inner := &mockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 10*time.Millisecond)

	var extra atomic.Value // state outside the content
	extra.Store("a")
	var failing atomic.Bool
	var keyErrs atomic.Int32
	ps.SetChangeKey(func(_ context.Context, data []byte, _ string) ([]byte, error) {
		if failing.Load() {
			keyErrs.Add(1)
			return nil, errors.New("key unavailable")
		}
		return append(data, extra.Load().(string)...), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() error {
			count.Add(1)
			return nil
		})
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (onChange calls: %d)", what, count.Load())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	waitFor("setup apply", func() bool { return count.Load() == 1 })
	time.Sleep(60 * time.Millisecond)
	if n := count.Load(); n != 1 {
		t.Fatalf("onChange called %d times with an unchanged key, want 1", n)
	}

	extra.Store("b")
	waitFor("key change", func() bool { return count.Load() == 2 })

	failing.Store(true)
	extra.Store("c")
	waitFor("failed key attempts", func() bool { return keyErrs.Load() >= 3 })
	if n := count.Load(); n != 2 {
		t.Fatalf("onChange called %d times while the key was failing, want 2", n)
	}
	failing.Store(false)
	waitFor("retry after key recovery", func() bool { return count.Load() == 3 })
}
