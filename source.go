package lemonfig

import "context"

// ConfigSource provides raw config bytes from any backend.
type ConfigSource interface {
	// Fetch returns the raw config content and its format ("yaml", "json", "toml").
	Fetch(ctx context.Context) (data []byte, format string, err error)
}

// WatchableSource is a [ConfigSource] that can push change notifications.
type WatchableSource interface {
	ConfigSource
	// Watch blocks and calls onChange whenever the config changes.
	// It must respect context cancellation and return nil when the
	// context is done.
	Watch(ctx context.Context, onChange func()) error
}
