// Package log provides structured debug logging for fir.
//
// All output goes to a file (never stdout/stderr) so it cannot interfere
// with TUI rendering, JSON-RPC, ACP, or piped output.
//
// When disabled (the default), every call is a zero-allocation no-op
// because slog short-circuits on the Enabled check.
package log

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
)

// logger is the global debug logger, accessed atomically. It defaults to a
// discard handler so calls before Init (or when debug is disabled) are no-ops.
var logger atomic.Pointer[slog.Logger]

func init() {
	l := slog.New(discardHandler{})
	logger.Store(l)
}

// getLogger returns the current logger. Safe for concurrent use.
func getLogger() *slog.Logger { return logger.Load() }

// Init configures the global debug logger.
// When enabled is false, all log calls remain no-ops (zero allocation).
// When enabled is true, structured JSON logs are written to path.
// The file is created if it doesn't exist and appended to on each run,
// so concurrent or successive fir processes share the same log safely.
// Returns a cleanup function that flushes and closes the log file.
func Init(enabled bool, path string) (cleanup func(), err error) {
	if !enabled {
		return func() {}, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	logger.Store(slog.New(handler))

	return func() { f.Close() }, nil
}

// Debug logs at debug level. No-op when disabled.
func Debug(msg string, args ...any) {
	getLogger().Debug(msg, args...)
}

// Info logs at info level. No-op when disabled.
func Info(msg string, args ...any) {
	getLogger().Info(msg, args...)
}

// Warn logs at warn level. No-op when disabled.
func Warn(msg string, args ...any) {
	getLogger().Warn(msg, args...)
}

// Error logs at error level. No-op when disabled.
func Error(msg string, args ...any) {
	getLogger().Error(msg, args...)
}

// With returns a sub-logger with pre-set attributes.
// Useful for per-component loggers:
//
//	toolLog := log.With("component", "bash")
//	toolLog.Debug("exec", "cmd", cmd)
func With(args ...any) *slog.Logger {
	return getLogger().With(args...)
}

// discardHandler is an slog.Handler that discards everything.
// Enabled returns false so slog skips argument formatting entirely.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler              { return discardHandler{} }
