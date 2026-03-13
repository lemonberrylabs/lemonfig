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
// It only calls onChange when the fetched bytes differ from the previous poll.
func (s *PollingSource) Watch(ctx context.Context, onChange func()) error {
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
				s.lastData = data
				onChange()
			}
		}
	}
}
