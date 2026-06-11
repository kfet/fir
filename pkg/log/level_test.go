package log

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceEnabled(t *testing.T) {
	resetLogger()
	defer resetLogger()

	path := filepath.Join(t.TempDir(), "debug.log")

	// At Info: Trace disabled.
	SetLevel(slog.LevelInfo)
	cleanup, err := Init(true, path, RotateConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if TraceEnabled() {
		t.Error("expected TraceEnabled=false at Info")
	}

	// At Debug: still disabled (Trace < Debug).
	SetLevel(slog.LevelDebug)
	if TraceEnabled() {
		t.Error("expected TraceEnabled=false at Debug")
	}

	// At Trace: enabled.
	SetLevel(LevelTrace)
	if !TraceEnabled() {
		t.Error("expected TraceEnabled=true at Trace")
	}
	cleanup()
}

func TestLevelMapping(t *testing.T) {
	cases := []struct {
		name      string
		level     slog.Level
		wantDebug bool
		wantTrace bool
	}{
		{"info", slog.LevelInfo, false, false},
		{"debug", slog.LevelDebug, true, false},
		{"trace", LevelTrace, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLogger()
			path := filepath.Join(t.TempDir(), "debug.log")
			SetLevel(tc.level)
			cleanup, err := Init(true, path, RotateConfig{})
			if err != nil {
				t.Fatal(err)
			}

			Info("info-msg")
			Debug("debug-msg")
			Trace("trace-msg")
			cleanup()

			data, _ := os.ReadFile(path)
			s := string(data)

			if !strings.Contains(s, "info-msg") {
				t.Error("info should always be emitted")
			}
			if got := strings.Contains(s, "debug-msg"); got != tc.wantDebug {
				t.Errorf("debug emission: got %v want %v", got, tc.wantDebug)
			}
			if got := strings.Contains(s, "trace-msg"); got != tc.wantTrace {
				t.Errorf("trace emission: got %v want %v\nlog:\n%s", got, tc.wantTrace, s)
			}

			if tc.wantTrace {
				// Verify TRACE label renders properly in JSON output.
				for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
					var m map[string]any
					if err := json.Unmarshal([]byte(line), &m); err != nil {
						continue
					}
					if m["msg"] == "trace-msg" {
						if m["level"] != "TRACE" {
							t.Errorf("trace level label: got %v want TRACE", m["level"])
						}
					}
				}
			}
		})
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"trace": LevelTrace,
		"DEBUG": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"-8":    LevelTrace,
		"0":     slog.LevelInfo,
	}
	for in, want := range cases {
		got, ok := ParseLevel(in)
		if !ok {
			t.Errorf("ParseLevel(%q): ok=false", in)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q): got %v want %v", in, got, want)
		}
	}
	if _, ok := ParseLevel("nope"); ok {
		t.Error("expected ParseLevel(nope)=false")
	}
}
