package source_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lemonberrylabs/lemonfig/source"
)

func TestFileSource_Fetch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("name: alice\nport: 8080\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path)
	data, format, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if format != "yaml" {
		t.Errorf("format = %q, want %q", format, "yaml")
	}
	if string(data) != string(content) {
		t.Errorf("data = %q", data)
	}
}

func TestFileSource_Fetch_JSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path)
	_, format, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if format != "json" {
		t.Errorf("format = %q, want %q", format, "json")
	}
}

func TestFileSource_Watch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("v: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path, source.WithDebounce(50*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var called atomic.Bool
	done := make(chan struct{})
	go func() {
		src.Watch(ctx, func() {
			called.Store(true)
		})
		close(done)
	}()

	// Give watcher time to start.
	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(path, []byte("v: 2"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for debounced callback.
	time.Sleep(200 * time.Millisecond)

	if !called.Load() {
		t.Error("onChange was not called after file write")
	}

	cancel()
	<-done
}

func TestFileSource_Watch_Debounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("v: 1"), 0644); err != nil {
		t.Fatal(err)
	}

	src := source.NewFileSource(path, source.WithDebounce(200*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var count atomic.Int32
	done := make(chan struct{})
	go func() {
		src.Watch(ctx, func() {
			count.Add(1)
		})
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	// Rapid writes — should coalesce into one callback.
	for i := range 5 {
		if err := os.WriteFile(path, []byte("v: "+string(rune('2'+i))), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for debounce to settle.
	time.Sleep(400 * time.Millisecond)

	got := count.Load()
	if got > 2 {
		t.Errorf("onChange called %d times, expected at most 2 (debounced)", got)
	}

	cancel()
	<-done
}
