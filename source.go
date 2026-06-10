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
	//
	// Implementations whose change detection has a setup window (e.g.
	// filesystem watchers) should invoke onChange once as soon as watching
	// is established, so a change landing between the caller's initial
	// fetch and watch setup is not missed. Spurious invocations are safe:
	// the caller re-fetches, and unchanged content produces no downstream
	// change.
	Watch(ctx context.Context, onChange func()) error
}
