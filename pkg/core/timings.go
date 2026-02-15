// Ported from: packages/coding-agent/src/core/timings.ts
// Upstream hash: 1caadb2e
package core

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// timingEntry records a single timing measurement.
type timingEntry struct {
	Label string
	Ms    int64
}

var (
	timingsEnabled bool
	timingsMu      sync.Mutex
	timings        []timingEntry
	lastTime       time.Time
)

func init() {
	timingsEnabled = os.Getenv("TAU_TIMING") == "1"
	lastTime = time.Now()
}

// RecordTiming records a timing measurement.
// Only active when TAU_TIMING=1 is set.
func RecordTiming(label string) {
	if !timingsEnabled {
		return
	}
	timingsMu.Lock()
	defer timingsMu.Unlock()
	now := time.Now()
	timings = append(timings, timingEntry{Label: label, Ms: now.Sub(lastTime).Milliseconds()})
	lastTime = now
}

// PrintTimings outputs all recorded timings to stderr.
// Only active when TAU_TIMING=1 is set.
func PrintTimings() {
	if !timingsEnabled {
		return
	}
	timingsMu.Lock()
	defer timingsMu.Unlock()
	if len(timings) == 0 {
		return
	}

	fmt.Fprintln(os.Stderr, "\n--- Startup Timings ---")
	var total int64
	for _, t := range timings {
		fmt.Fprintf(os.Stderr, "  %s: %dms\n", t.Label, t.Ms)
		total += t.Ms
	}
	fmt.Fprintf(os.Stderr, "  TOTAL: %dms\n", total)
	fmt.Fprintln(os.Stderr, "------------------------")
}

// ResetTimings clears all recorded timings. Exported for testing.
func ResetTimings() {
	timingsMu.Lock()
	defer timingsMu.Unlock()
	timings = nil
	lastTime = time.Now()
}
