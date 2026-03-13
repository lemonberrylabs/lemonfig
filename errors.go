// Package lemonfig provides reactive, hot-reloadable configuration for Go applications.
//
// It uses Viper under the hood and provides [Derived] handles that always return
// the latest config value. When config reloads, all derived values are atomically
// recomputed and swapped in — including heavy resources like DB pools and HTTP clients.
package lemonfig

import "errors"

var (
	// ErrAlreadyStarted is returned when Start is called more than once.
	ErrAlreadyStarted = errors.New("lemonfig: manager already started")

	// ErrNotStarted is returned when Stop is called before Start.
	ErrNotStarted = errors.New("lemonfig: manager not started")

	// ErrFrozen is the panic message when registering derived values after Start.
	ErrFrozen = errors.New("lemonfig: cannot register derived values after Start")

	// ErrFetchFailed is returned when the config source fails to fetch.
	ErrFetchFailed = errors.New("lemonfig: fetch failed")

	// ErrParseFailed is returned when the config bytes cannot be parsed.
	ErrParseFailed = errors.New("lemonfig: config parse failed")

	// ErrValidationFailed is returned when the validation function rejects the config.
	ErrValidationFailed = errors.New("lemonfig: config validation failed")

	// ErrTransformFailed is returned when a Map/Combine transform returns an error during reload.
	ErrTransformFailed = errors.New("lemonfig: transform failed during reload")
)
