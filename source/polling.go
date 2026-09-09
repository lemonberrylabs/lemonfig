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
	lastData []byte
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

// Watch polls the inner source at the configured interval.
// It calls onChange when the fetched bytes differ from the last content the
// caller successfully applied. A failed onChange leaves lastData untouched, so
// the same content is offered again on the next tick until it applies — a
// transient failure (secret resolution, a dependency rebuild) cannot strand
// the process on a stale generation until the source happens to change again.
func (s *PollingSource) Watch(ctx context.Context, onChange func() error) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Capture initial state.
	data, _, err := s.inner.Fetch(ctx)
	if err == nil {
		s.lastData = data
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			data, _, err := s.inner.Fetch(ctx)
			if err != nil {
				continue // skip this tick on error
			}
			if !bytes.Equal(data, s.lastData) {
				if err := onChange(); err != nil {
					continue // retry the same content on the next tick
				}
				s.lastData = data
			}
		}
	}
}
