package core

import (
	"os"
	"testing"
	"time"
)

func TestRecordTiming_Disabled(t *testing.T) {
	// Ensure timings are disabled
	oldEnabled := timingsEnabled
	timingsEnabled = false
	defer func() { timingsEnabled = oldEnabled }()

	ResetTimings()
	RecordTiming("test")

	timingsMu.Lock()
	count := len(timings)
	timingsMu.Unlock()

	if count != 0 {
		t.Error("expected no timings when disabled")
	}
}

func TestRecordTiming_Enabled(t *testing.T) {
	oldEnabled := timingsEnabled
	timingsEnabled = true
	defer func() { timingsEnabled = oldEnabled }()

	ResetTimings()
	RecordTiming("step1")
	time.Sleep(5 * time.Millisecond)
	RecordTiming("step2")

	timingsMu.Lock()
	count := len(timings)
	step1 := timings[0]
	step2 := timings[1]
	timingsMu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 timings, got %d", count)
	}
	if step1.Label != "step1" {
		t.Errorf("expected 'step1', got %q", step1.Label)
	}
	if step2.Label != "step2" {
		t.Errorf("expected 'step2', got %q", step2.Label)
	}
}

func TestPrintTimings_Disabled(t *testing.T) {
	oldEnabled := timingsEnabled
	timingsEnabled = false
	defer func() { timingsEnabled = oldEnabled }()

	// Should not panic
	PrintTimings()
}

func TestPrintTimings_Enabled(t *testing.T) {
	oldEnabled := timingsEnabled
	timingsEnabled = true
	defer func() { timingsEnabled = oldEnabled }()

	ResetTimings()
	RecordTiming("test-timing")

	// Capture stderr
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	PrintTimings()

	w.Close()
	os.Stderr = oldStderr

	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if len(output) == 0 {
		t.Error("expected timing output on stderr")
	}
}

func TestResetTimings(t *testing.T) {
	oldEnabled := timingsEnabled
	timingsEnabled = true
	defer func() { timingsEnabled = oldEnabled }()

	RecordTiming("before-reset")
	ResetTimings()

	timingsMu.Lock()
	count := len(timings)
	timingsMu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 timings after reset, got %d", count)
	}
}
