package source_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig/source"
)

// --- FileSource edge cases ---

func TestFileSource_Fetch_MissingFile(t *testing.T) {
	src := source.NewFileSource("/nonexistent/path/config.yaml")
	_, _, err := src.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileSource_Fetch_TOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nport = 8080\n"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path)
	_, format, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if format != "toml" {
		t.Errorf("format = %q, want toml", format)
	}
}

func TestFileSource_Fetch_NoExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("x: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path)
	_, format, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// No extension → empty format string.
	if format != "" {
		t.Errorf("format = %q, want empty", format)
	}
}

func TestFileSource_Fetch_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path)
	data, format, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty data, got %q", data)
	}
	if format != "yaml" {
		t.Errorf("format = %q", format)
	}
}

func TestFileSource_Watch_FileCreatedAfterWatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// File doesn't exist yet — but the directory does, so watch should work.
	// We need the file to exist for fsnotify to watch the dir properly.
	// Actually, FileSource watches the parent directory. So this should work.
	// Let's first create the file, start watching, then modify it.
	if err := os.WriteFile(path, []byte("v: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path, source.WithDebounce(50*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called atomic.Bool
	done := make(chan struct{})
	go func() {
		src.Watch(ctx, func() { called.Store(true) })
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	// Delete the file and recreate it (simulating atomic file replacement).
	os.Remove(path)
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, []byte("v: 2"), 0644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	if !called.Load() {
		t.Error("onChange was not called after file recreate")
	}

	cancel()
	<-done
}

func TestFileSource_Watch_ContextAlreadyCanceled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("v: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already canceled.

	done := make(chan struct{})
	go func() {
		src.Watch(ctx, func() {})
		close(done)
	}()

	select {
	case <-done:
		// Watch should exit quickly.
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not exit with already-canceled context")
	}
}

func TestFileSource_Watch_IgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	otherPath := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(configPath, []byte("v: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(configPath, source.WithDebounce(50*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called atomic.Bool
	done := make(chan struct{})
	go func() {
		src.Watch(ctx, func() { called.Store(true) })
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	// Write to a different file in the same directory.
	if err := os.WriteFile(otherPath, []byte("other: data"), 0644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	if called.Load() {
		t.Error("onChange was called for a different file")
	}

	cancel()
	<-done
}

func TestFileSource_Watch_RapidWritesCoalesce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("v: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path, source.WithDebounce(300*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		src.Watch(ctx, func() { count.Add(1) })
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	// Many rapid writes within debounce window.
	for i := range 10 {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("v: %d", i+2)), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to settle.
	time.Sleep(500 * time.Millisecond)

	got := count.Load()
	if got > 2 {
		t.Errorf("onChange called %d times, expected at most 2 due to debounce", got)
	}
	if got == 0 {
		t.Error("onChange was never called")
	}

	cancel()
	<-done
}

// --- PollingSource edge cases ---

type failingMockSource struct {
	mu   sync.Mutex
	data []byte
	fail atomic.Bool
}

func (s *failingMockSource) Set(data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = []byte(data)
}

func (s *failingMockSource) Fetch(_ context.Context) ([]byte, string, error) {
	if s.fail.Load() {
		return nil, "", errors.New("fetch error")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, "yaml", nil
}

func TestPollingSource_InnerFetchError_SkipsTick(t *testing.T) {
	inner := &failingMockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called atomic.Bool
	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() { called.Store(true) })
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)

	// Enable failures.
	inner.fail.Store(true)
	inner.Set("v: 2") // This change should not trigger because fetch fails.

	time.Sleep(200 * time.Millisecond)

	if called.Load() {
		t.Error("onChange should not be called when inner.Fetch fails")
	}

	// Disable failures — next poll should detect the change.
	inner.fail.Store(false)
	time.Sleep(200 * time.Millisecond)

	if !called.Load() {
		t.Error("onChange should have been called after fetch recovered")
	}

	cancel()
	<-done
}

func TestPollingSource_MultipleChangesBetweenPolls(t *testing.T) {
	inner := &mockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() { count.Add(1) })
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Make multiple changes between polls.
	inner.Set("v: 2")
	time.Sleep(10 * time.Millisecond)
	inner.Set("v: 3")
	time.Sleep(10 * time.Millisecond)
	inner.Set("v: 4")

	// Wait for at least one poll.
	time.Sleep(300 * time.Millisecond)

	// Should detect exactly one change (the latest state differs from last known).
	got := count.Load()
	if got != 1 {
		t.Errorf("onChange called %d times, expected 1", got)
	}

	cancel()
	<-done
}

func TestPollingSource_Fetch_DelegatesToInner(t *testing.T) {
	inner := &mockSource{data: []byte("hello: world")}
	ps := source.NewPollingSource(inner, time.Second)

	data, format, err := ps.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello: world" {
		t.Errorf("data = %q", data)
	}
	if format != "yaml" {
		t.Errorf("format = %q", format)
	}
}

func TestPollingSource_ChangeBackToOriginal(t *testing.T) {
	inner := &mockSource{data: []byte("v: 1")}
	ps := source.NewPollingSource(inner, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		ps.Watch(ctx, func() { count.Add(1) })
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)

	// Change to v: 2.
	inner.Set("v: 2")
	time.Sleep(100 * time.Millisecond)

	// Change back to v: 1.
	inner.Set("v: 1")
	time.Sleep(100 * time.Millisecond)

	// Should have detected both changes.
	got := count.Load()
	if got != 2 {
		t.Errorf("onChange called %d times, want 2", got)
	}

	cancel()
	<-done
}

