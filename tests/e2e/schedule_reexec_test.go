//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestScheduleReexec verifies that /schedule entries survive a /reexec.
//
// Flow:
//  1. Copy schedule.py into a temp project's .fir/extensions/
//  2. Start fir in a PTY (with the mock provider so no real API key is needed)
//  3. Send "/schedule 30m" — verify the "Scheduled" confirmation appears
//  4. Send "/reexec"        — fir replaces itself via syscall.Exec
//  5. After restart, verify "⏰" appears (schedule was restored)
//
// The test catches the bug where handleReexecCommand called CollectSessionData()
// before emitting session_shutdown, so the schedule extension's handler never
// ran and schedules were silently dropped from the sidecar.
func TestScheduleReexec(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		if _, err2 := os.Stat("/usr/local/bin/python3"); err2 != nil {
			t.Skip("python3 not available")
		}
	}

	// ── locate schedule.py in the source tree ─────────────────────────
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	scheduleExt := filepath.Join(repoRoot, "pkg", "resources", "builtin_extensions", "schedule.py")
	if _, err := os.Stat(scheduleExt); err != nil {
		t.Fatalf("schedule.py not found at %s: %v", scheduleExt, err)
	}

	// ── set up temp project dir with the schedule extension ───────────
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcData, err := os.ReadFile(scheduleExt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "schedule.py"), srcData, 0o755); err != nil {
		t.Fatal(err)
	}

	// ── launch fir in a PTY ───────────────────────────────────────────
	fp := StartFirPTY(t, projectDir, nil,
		"--session-dir", projectDir,
		"--extension", "schedule",
	)

	// Wait for the TUI prompt.
	fp.WaitForPrompt(20 * time.Second)
	t.Logf("fir started")

	// Give extensions a moment to fully start (session_start fires lazily
	// and the Python process takes ~1 s to handshake).
	time.Sleep(2 * time.Second)

	// ── step 1: schedule 30 minutes from now ─────────────────────────
	fp.Send("/schedule 30m")
	fp.RequireWaitFor("Scheduled", 10*time.Second,
		"'/schedule 30m' did not produce a 'Scheduled' confirmation")
	t.Logf("schedule created")

	// Wait briefly for the first status update.
	time.Sleep(1500 * time.Millisecond)

	// ── step 2: reexec ────────────────────────────────────────────────
	fp.Send("/reexec")

	// After /reexec, fir calls syscall.Exec which replaces the process.
	// The new fir process emits session_start with the saved schedule data,
	// the schedule extension restores the schedule, and set_status is called
	// with "⏰ ...".
	fp.RequireWaitFor("⏰", 20*time.Second,
		"schedule status '⏰' did not reappear after /reexec — "+
			"schedules were not saved to the reexec sidecar before exec")
	t.Logf("✓ schedule survived /reexec")
}
