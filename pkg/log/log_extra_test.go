package log

import (
	"context"
	"log/slog"
	"testing"
)

// TestDebugEnvSet pins the startup rule: any non-empty FIR_DEBUG turns debug
// logging on, an unset or empty one leaves it off. "0" deliberately counts as
// on — the variable is a presence flag, not a boolean, and silently treating
// FIR_DEBUG=0 as off would surprise anyone who set it to disable logging.
func TestDebugEnvSet(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false},
		{"1", true},
		{"true", true},
		{"0", true},
	} {
		t.Setenv("FIR_DEBUG", tc.val)
		if got := debugEnvSet(); got != tc.want {
			t.Errorf("FIR_DEBUG=%q: debugEnvSet() = %v, want %v", tc.val, got, tc.want)
		}
	}
}

// TestCurrentLevelReflectsSetLevel pins that the accessor reads the same
// dynamic level the active handler is wired to, so a caller can check the
// threshold it just set.
func TestCurrentLevelReflectsSetLevel(t *testing.T) {
	orig := CurrentLevel()
	t.Cleanup(func() { SetLevel(orig) })

	for _, lv := range []slog.Level{LevelTrace, slog.LevelDebug, slog.LevelInfo, slog.LevelError} {
		SetLevel(lv)
		if got := CurrentLevel(); got != lv {
			t.Errorf("after SetLevel(%v), CurrentLevel() = %v", lv, got)
		}
	}
}

// TestParseLevelEmpty pins that an empty value is reported as "not set"
// rather than as a valid Info request — callers use the ok flag to decide
// whether a config value was supplied at all.
func TestParseLevelEmpty(t *testing.T) {
	for _, s := range []string{"", "   ", "\t"} {
		lv, ok := ParseLevel(s)
		if ok {
			t.Errorf("ParseLevel(%q) reported ok=true", s)
		}
		if lv != slog.LevelInfo {
			t.Errorf("ParseLevel(%q) = %v, want the Info fallback", s, lv)
		}
	}
}

// TestDiscardHandlerStaysDisabled pins the property that makes disabled
// logging free: the discard handler reports every level disabled, drops
// records, and — the part that could actually regress — every derived
// handler is still a discard handler. If WithAttrs or WithGroup ever
// returned something else, `log.With(...)` would punch a hole straight
// through the no-op guarantee.
func TestDiscardHandlerStaysDisabled(t *testing.T) {
	ctx := context.Background()
	levels := []slog.Level{LevelTrace, slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError, levelDisabled}

	var h slog.Handler = discardHandler{}
	for _, lv := range levels {
		if h.Enabled(ctx, lv) {
			t.Errorf("discardHandler.Enabled(%v) = true, want false", lv)
		}
	}
	if err := h.Handle(ctx, slog.Record{}); err != nil {
		t.Errorf("discardHandler.Handle: %v", err)
	}

	derived := []slog.Handler{
		h.WithAttrs([]slog.Attr{slog.String("component", "bash")}),
		h.WithGroup("group"),
		h.WithAttrs(nil).WithGroup("g").WithAttrs([]slog.Attr{slog.Int("n", 1)}),
	}
	for i, d := range derived {
		if _, ok := d.(discardHandler); !ok {
			t.Errorf("derived handler %d is %T, want discardHandler", i, d)
		}
		for _, lv := range levels {
			if d.Enabled(ctx, lv) {
				t.Errorf("derived handler %d reports %v enabled", i, lv)
			}
		}
		if err := d.Handle(ctx, slog.Record{}); err != nil {
			t.Errorf("derived handler %d Handle: %v", i, err)
		}
	}
}

// TestWithOnDiscardLoggerIsSafe pins the same guarantee through the public
// API: a component logger taken before Init must stay a no-op.
func TestWithOnDiscardLoggerIsSafe(t *testing.T) {
	resetLogger()
	t.Cleanup(resetLogger)

	component := With("component", "bash")
	if component.Enabled(context.Background(), slog.LevelError) {
		t.Error("component logger is enabled on a discard handler")
	}
	grouped := component.WithGroup("exec")
	if grouped.Enabled(context.Background(), slog.LevelError) {
		t.Error("grouped component logger is enabled on a discard handler")
	}
	// These must not panic or write anywhere.
	component.Error("dropped", "cmd", "ls")
	grouped.Error("dropped")
}
