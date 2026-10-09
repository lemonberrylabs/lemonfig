package source

import (
	"bytes"
	"context"
	"time"

	"github.com/lemonberrylabs/lemonfig"
)

// PollingSource wraps any [lemonfig.ConfigSource] with interval-based polling.
// It implements [lemonfig.WatchableSource] by comparing fetched bytes on each tick.
type PollingSource struct {
	inner    lemonfig.ConfigSource
	interval time.Duration
	key      func(ctx context.Context, data []byte, format string) ([]byte, error)
	lastKey  []byte
}

// NewPollingSource wraps the given source with polling at the specified interval.
func NewPollingSource(inner lemonfig.ConfigSource, interval time.Duration) *PollingSource {
	return &PollingSource{
		inner:    inner,
		interval: interval,
	}
}

// Fetch delegates to the inner source.
func (s *PollingSource) Fetch(ctx context.Context) ([]byte, string, error) {
	return s.inner.Fetch(ctx)
}

// SetChangeKey implements [lemonfig.ChangeKeyer]: Watch compares
// key(content, format) instead of the content itself.
func (s *PollingSource) SetChangeKey(key func(ctx context.Context, data []byte, format string) ([]byte, error)) {
	s.key = key
}

// fetchKey fetches the inner source and returns the value Watch compares.
func (s *PollingSource) fetchKey(ctx context.Context) ([]byte, error) {
	data, format, err := s.inner.Fetch(ctx)
	if err != nil || s.key == nil {
		return data, err
	}
	return s.key(ctx, data, format)
}

// Watch polls the inner source at the configured interval.
// It calls onChange when the fetched bytes differ from the last content the
// caller successfully applied. A failed onChange leaves lastKey untouched, so
// the same content is offered again on the next tick until it applies — a
// transient failure (secret resolution, a dependency rebuild) cannot strand
// the process on a stale generation until the source happens to change again.
func (s *PollingSource) Watch(ctx context.Context, onChange func() error) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Establish the baseline by applying once, per the WatchableSource
	// contract: the caller's own initial fetch happened before this Watch
	// started, so content that landed in between would otherwise be treated
	// as already seen and missed until the next change. If the apply fails,
	// lastKey stays nil and the first tick retries.
	if key, err := s.fetchKey(ctx); err == nil {
		if onChange() == nil {
			s.lastKey = key
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			key, err := s.fetchKey(ctx)
			if err != nil {
				continue // skip this tick on error
			}
			if !bytes.Equal(key, s.lastKey) {
				if err := onChange(); err != nil {
					continue // retry the same content on the next tick
				}
				s.lastKey = key
			}
		}
	}
}
