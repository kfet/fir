// Package log provides structured debug logging for fir.
//
// All output goes to a file (never stdout/stderr) so it cannot interfere
// with TUI rendering, JSON-RPC, ACP, or piped output.
//
// When disabled (the default), every call is a zero-allocation no-op
// because slog short-circuits on the Enabled check.
//
// Three verbosity levels above Info are available:
//
//	Debug — per-turn / per-session high-level events (one per agent loop,
//	        per streaming request, per MCP connection, etc.)
//	Trace — per micro-op summaries (per cache prefix check, per MCP frame,
//	        per stream chunk). Recurring dozens+ of times per turn.
//
// Trace is implemented as a custom slog level (slog.LevelDebug - 4 = -8)
// so it sorts below Debug and is enabled by --verbose --verbose / -vv.
package log

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// LevelTrace is the most verbose level. It sits below slog.LevelDebug so
// that handlers configured at Debug suppress Trace records by default.
const LevelTrace slog.Level = slog.LevelDebug - 4 // -8

// levelDisabled is an internal sentinel above LevelError that turns the
// logger into a no-op (nothing is ever Enabled).
const levelDisabled slog.Level = slog.LevelError + 4 // 12

// levelVar holds the dynamic log threshold. The active slog handler is
// wired to read from this so SetLevel takes effect immediately.
var levelVar = new(slog.LevelVar)

func init() {
	levelVar.Set(levelDisabled)
}

// logger is the global debug logger, accessed atomically. It defaults to a
// discard handler so calls before Init (or when logging is disabled) are no-ops.
var logger atomic.Pointer[slog.Logger]

func init() {
	l := slog.New(discardHandler{})
	logger.Store(l)
	// Also redirect the global slog default so any stray slog.* call (or
	// third-party library using log/slog) cannot punch through to stderr
	// and corrupt the TUI. Tests that need to capture slog output may
	// still override via slog.SetDefault.
	slog.SetDefault(l)
}

// getLogger returns the current logger. Safe for concurrent use.
func getLogger() *slog.Logger { return logger.Load() }

// SetLevel adjusts the active log threshold. Calls below the threshold are
// dropped at the handler with zero allocation. Safe for concurrent use.
func SetLevel(level slog.Level) {
	levelVar.Set(level)
}

// CurrentLevel returns the active log threshold.
func CurrentLevel() slog.Level { return levelVar.Level() }

// ParseLevel parses a level name (case-insensitive) or its numeric slog value.
// Recognised names: "trace", "debug", "info", "warn", "error". Empty string
// returns (Info, false). Unknown input returns (Info, false).
func ParseLevel(s string) (slog.Level, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return slog.LevelInfo, false
	}
	switch s {
	case "trace":
		return LevelTrace, true
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	// Allow raw numeric levels too (matches slog's default text format).
	if n, err := strconv.Atoi(s); err == nil {
		return slog.Level(n), true
	}
	return slog.LevelInfo, false
}

// Init configures the global debug logger.
// When enabled is false, all log calls remain no-ops (zero allocation).
// When enabled is true, structured JSON logs are written to path at the
// current SetLevel threshold (defaults to Debug if SetLevel was not called).
// The file is created if it doesn't exist and appended to on each run,
// so concurrent or successive fir processes share the same log safely.
// Returns a cleanup function that flushes and closes the log file.
func Init(enabled bool, path string) (cleanup func(), err error) {
	if !enabled {
		levelVar.Set(levelDisabled)
		return func() {}, nil
	}

	// If caller hasn't set an explicit level yet, default to Debug.
	if levelVar.Level() == levelDisabled {
		levelVar.Set(slog.LevelDebug)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: levelVar,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Render our custom Trace level as "TRACE" instead of "DEBUG-4".
			if a.Key == slog.LevelKey {
				if lv, ok := a.Value.Any().(slog.Level); ok && lv == LevelTrace {
					a.Value = slog.StringValue("TRACE")
				}
			}
			return a
		},
	})
	l := slog.New(handler)
	logger.Store(l)
	// Mirror onto slog.Default so any stray slog.* call (ours or a
	// dependency's) lands in the debug log file instead of stderr.
	slog.SetDefault(l)

	return func() { f.Close() }, nil
}

// Trace logs at trace level (more verbose than Debug). No-op when not enabled.
func Trace(msg string, args ...any) {
	l := getLogger()
	l.Log(context.Background(), LevelTrace, msg, args...)
}

// TraceEnabled reports whether Trace-level logs are currently active. Useful
// for guarding expensive argument construction.
func TraceEnabled() bool {
	return getLogger().Enabled(context.Background(), LevelTrace)
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
func (discardHandler) WithGroup(string) slog.Handler             { return discardHandler{} }
