// debug.go contains the legacy debug.Log API, now merged into pkg/log.
//
// Debug logging is enabled via the --debug CLI flag or the FIR_DEBUG=1
// environment variable. When disabled, all debug calls are no-ops.
// Output goes to stderr so it never interferes with stdout-based
// protocols (RPC, ACP, JSON mode).
package log

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

var debugEnabled atomic.Bool

func init() {
	if os.Getenv("FIR_DEBUG") != "" {
		debugEnabled.Store(true)
	}
}

// Enable turns debug logging on.
func Enable() { debugEnabled.Store(true) }

// Disable turns debug logging off.
func Disable() { debugEnabled.Store(false) }

// Enabled reports whether debug logging is active.
func Enabled() bool { return debugEnabled.Load() }

// Log writes a timestamped debug message to stderr.
// It is a no-op when debug logging is disabled.
func Log(format string, args ...any) {
	if !debugEnabled.Load() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05.000")
	fmt.Fprintf(os.Stderr, "[DEBUG %s] %s\n", ts, msg)
}
