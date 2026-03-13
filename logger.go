package lemonfig

// Logger is a minimal structured logging interface.
// Implementations can wrap slog, zap, zerolog, or any other logger.
type Logger interface {
	Info(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

// NoopLogger discards all log output.
type NoopLogger struct{}

func (NoopLogger) Info(string, ...any)  {}
func (NoopLogger) Error(string, ...any) {}
