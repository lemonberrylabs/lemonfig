// Package source provides built-in [lemonfig.ConfigSource] implementations.
package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileSource reads configuration from a local file and watches it for changes.
// It implements [lemonfig.WatchableSource].
type FileSource struct {
	path     string
	debounce time.Duration
}

// FileOption configures a [FileSource].
type FileOption func(*FileSource)

// WithDebounce sets the debounce duration for file change events.
// Default is 100ms. Editors often write temp files then rename,
// causing rapid events that should be coalesced.
func WithDebounce(d time.Duration) FileOption {
	return func(s *FileSource) { s.debounce = d }
}

// NewFileSource creates a [FileSource] that reads from the given path.
func NewFileSource(path string, opts ...FileOption) *FileSource {
	s := &FileSource{
		path:     path,
		debounce: 100 * time.Millisecond,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Fetch reads the file and returns its contents. The format is inferred
// from the file extension.
func (s *FileSource) Fetch(_ context.Context) ([]byte, string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, "", err
	}
	ext := strings.TrimPrefix(filepath.Ext(s.path), ".")
	return data, ext, nil
}

// Watch uses fsnotify to watch the file's parent directory for changes.
// It debounces rapid events and calls onChange when the file is modified.
func (s *FileSource) Watch(ctx context.Context, onChange func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	dir := filepath.Dir(s.path)
	if err := watcher.Add(dir); err != nil {
		return err
	}

	base := filepath.Base(s.path)
	var timer *time.Timer

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if filepath.Base(event.Name) != base {
				continue
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) && !event.Has(fsnotify.Rename) {
				continue
			}
			// Debounce: reset the timer on each event.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(s.debounce, onChange)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return err
		}
	}
}
