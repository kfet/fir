// Package debug provides a global debug logger for fir.
//
// Debug logging is enabled via the --debug CLI flag or the FIR_DEBUG=1
// environment variable. When disabled, all debug calls are no-ops.
// Output goes to stderr so it never interferes with stdout-based
// protocols (RPC, ACP, JSON mode).
package debug

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

var enabled atomic.Bool

func init() {
	if os.Getenv("FIR_DEBUG") != "" {
		enabled.Store(true)
	}
}

// Enable turns debug logging on.
func Enable() { enabled.Store(true) }

// Disable turns debug logging off.
func Disable() { enabled.Store(false) }

// Enabled reports whether debug logging is active.
func Enabled() bool { return enabled.Load() }

// Log writes a timestamped debug message to stderr.
// It is a no-op when debug logging is disabled.
func Log(format string, args ...any) {
	if !enabled.Load() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05.000")
	fmt.Fprintf(os.Stderr, "[DEBUG %s] %s\n", ts, msg)
}
