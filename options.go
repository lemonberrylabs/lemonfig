package lemonfig

import (
	"time"

	"github.com/spf13/viper"
)

// Option configures a [Manager].
type Option func(*managerConfig)

type managerConfig struct {
	configType     string
	validate       func(*viper.Viper) error
	cleanupGrace   time.Duration
	logger         Logger
	onReload       []func(old, new_ *viper.Viper)
	viperConfigure []func(*viper.Viper)
	secretResolver SecretResolver
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

// WithViperConfigure registers a function applied to each fresh Viper
// instance before the raw config bytes are read into it. Use it to set
// defaults, enable environment-variable overrides, or otherwise tune
// per-generation Viper behavior. Multiple functions run in registration order.
func WithViperConfigure(fn func(*viper.Viper)) Option {
	return func(c *managerConfig) { c.viperConfigure = append(c.viperConfigure, fn) }
}

// WithSecretResolver sets the function that turns a secret reference in the
// document into its value.
//
// A reference is the string "!secret NAME" in any config format; in YAML it
// can also be written as the tag `!secret NAME`. To use a literal string that
// starts with "!secret ", write "!!secret ...": one leading "!" is removed.
//
// The Manager owns what happens to a reference:
//
//   - It must land on a [Secret]-typed field of every [Load], [Struct] or
//     [Key] target that reads its path, and at least one target must read it.
//     Otherwise the reload fails with [ErrSecretRef], naming the path, and
//     nothing is resolved. References are followed through maps, slices, YAML
//     aliases and merge keys.
//   - r is called once per distinct name per reload, with up to 8 calls
//     running at once. A failure rejects the generation with
//     [ErrSecretResolveFailed] and is reported to the [Logger]. So does a
//     resolved value that is itself a reference: references are not chained.
//   - The value reaches the config only as a [Secret]. It is never a string
//     inside the Viper instance, and it prints and marshals as "!secret NAME".
//
// A Secret field may also hold a literal that is not a reference. A document
// with a reference and no resolver is rejected.
//
// To pick up a rotated secret while the document is unchanged, the source
// must implement [ChangeKeyer], as source.PollingSource does: each poll then
// resolves the document's secrets and a changed value triggers a reload.
// With other sources, call [Manager.Reload].
func WithSecretResolver(r SecretResolver) Option {
	return func(c *managerConfig) { c.secretResolver = r }
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
