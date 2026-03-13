package source_test

import (
	"context"
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
		ps.Watch(ctx, func() {
			called.Store(true)
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
		ps.Watch(ctx, func() {
			count.Add(1)
		})
		close(done)
	}()

	// Let several polls happen without changing data.
	time.Sleep(200 * time.Millisecond)

	if count.Load() != 0 {
		t.Errorf("onChange called %d times, expected 0", count.Load())
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
		ps.Watch(ctx, func() {})
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
