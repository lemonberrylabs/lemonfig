package lemonfig

import "context"

// ConfigSource provides raw config bytes from any backend.
type ConfigSource interface {
	// Fetch returns the raw config content and its format ("yaml", "json", "toml").
	Fetch(ctx context.Context) (data []byte, format string, err error)
}

// ChangeKeyer is implemented by a [WatchableSource] that detects change by
// comparing fetched content. SetChangeKey replaces the compared value with
// key(content, format), so that state the content only refers to counts as a
// change. A [Manager] installs a key that covers the document's resolved
// secrets, which is how a rotated secret triggers a reload.
//
// SetChangeKey is called before Watch. When key returns an error the source
// must treat the observation as failed and repeat it, as it does for a failed
// fetch.
type ChangeKeyer interface {
	SetChangeKey(key func(ctx context.Context, data []byte, format string) ([]byte, error))
}

// WatchableSource is a [ConfigSource] that can push change notifications.
type WatchableSource interface {
	ConfigSource
	// Watch blocks and calls onChange whenever the config changes.
	// It must respect context cancellation and return nil when the
	// context is done.
	//
	// onChange reports whether the caller applied the change. A non-nil
	// error means the new content was NOT applied (fetch, parse, validation
	// or a transform failed) and the previous generation is still live;
	// sources that detect change by comparing against the last content
	// must keep comparing against the last APPLIED content, so the next
	// observation retries instead of treating the failed content as seen.
	//
	// Implementations whose change detection has a setup window (e.g.
	// filesystem watchers) should invoke onChange once as soon as watching
	// is established, so a change landing between the caller's initial
	// fetch and watch setup is not missed. Spurious invocations are safe:
	// the caller re-fetches, and unchanged content produces no downstream
	// change.
	Watch(ctx context.Context, onChange func() error) error
}
