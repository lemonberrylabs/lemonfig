package lemonfig

import (
	"time"

	"github.com/spf13/viper"
)

// Option configures a [Manager].
type Option func(*managerConfig)

type managerConfig struct {
	configType   string
	validate     func(*viper.Viper) error
	cleanupGrace time.Duration
	logger       Logger
	onReload     []func(old, new_ *viper.Viper)
}

func defaultConfig() managerConfig {
	return managerConfig{
		cleanupGrace: 30 * time.Second,
		logger:       NoopLogger{},
	}
}

// WithConfigType sets the config format ("yaml", "json", "toml").
// If not set, the format returned by [ConfigSource.Fetch] is used.
func WithConfigType(t string) Option {
	return func(c *managerConfig) { c.configType = t }
}

// WithValidation registers a function that validates a parsed config.
// If it returns an error, the reload is aborted and the old generation is kept.
func WithValidation(fn func(*viper.Viper) error) Option {
	return func(c *managerConfig) { c.validate = fn }
}

// WithCleanupGrace sets the grace period before cleaning up old generation
// resources after a swap. Default is 30 seconds.
func WithCleanupGrace(d time.Duration) Option {
	return func(c *managerConfig) { c.cleanupGrace = d }
}

// WithLogger sets a structured logger for the manager.
func WithLogger(l Logger) Option {
	return func(c *managerConfig) { c.logger = l }
}

// WithOnReload registers a callback that fires after a successful reload.
func WithOnReload(fn func(old, new_ *viper.Viper)) Option {
	return func(c *managerConfig) { c.onReload = append(c.onReload, fn) }
}
