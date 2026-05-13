package main

import (
	"log/slog"
	"os"

	firlog "github.com/kfet/fir/pkg/log"
)

// resolveLogLevel determines the effective slog level and whether file
// logging is enabled, based on (in precedence order, later wins):
//
//  1. FIR_DEBUG=1 env var          → Debug
//  2. FIR_LOG_LEVEL env var        → named level (info|debug|trace or numeric)
//  3. --debug flag                 → Debug
//  4. -v / -vv (clamped to 2)      → Debug / Trace
//
// FIR_LOG_LEVEL wins over FIR_DEBUG (explicit beats implicit). CLI flags
// always win over environment variables. The returned `enabled` flag controls
// whether the debug.log file is opened; once enabled, the slog handler reads
// its threshold from firlog.SetLevel.
func resolveLogLevel(args *Args) (level slog.Level, enabled bool) {
	// Default: Info, file logging disabled.
	level = slog.LevelInfo
	enabled = false

	if os.Getenv("FIR_DEBUG") == "1" {
		level = slog.LevelDebug
		enabled = true
	}
	if v := os.Getenv("FIR_LOG_LEVEL"); v != "" {
		if lv, ok := firlog.ParseLevel(v); ok {
			level = lv
			enabled = true
		}
	}
	if args.Debug {
		level = slog.LevelDebug
		enabled = true
	}

	vc := args.VerboseCount
	if vc > 2 {
		vc = 2
	}
	switch vc {
	case 1:
		level = slog.LevelDebug
		enabled = true
	case 2:
		level = firlog.LevelTrace
		enabled = true
	}

	return level, enabled
}
