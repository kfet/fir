//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestScheduleReexec_Tmux is an end-to-end test that drives a real fir binary
// inside a tmux session to verify that /schedule entries survive a /reexec.
//
// Flow:
//  1. Copy schedule.py into a temp project's .fir/extensions/
//  2. Start fir in a tmux pane (with the mock provider so no real API key is needed)
//  3. Send "/schedule 30m" — verify the "Scheduled" confirmation appears
//  4. Send "/reexec"        — fir replaces itself via syscall.Exec
//  5. After restart, verify "⏰" appears in the pane (schedule was restored)
//
// The test catches the bug where handleReexecCommand called CollectSessionData()
// before emitting session_shutdown, so the schedule extension's handler never
// ran and schedules were silently dropped from the sidecar.
func TestScheduleReexec_Tmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
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

	// ── tmux session name (unique per test run) ───────────────────────
	sessionName := fmt.Sprintf("fir-sched-reexec-%d", os.Getpid())

	tmuxRun := func(args ...string) error {
		cmd := exec.Command("tmux", args...)
		cmd.Stdout = os.Stderr // route to test stderr for debugging
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	capturePane := func() string {
		out, _ := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p").Output()
		return string(out)
	}

	waitFor := func(want string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(capturePane(), want) {
				return true
			}
			time.Sleep(300 * time.Millisecond)
		}
		return false
	}

	sendKeys := func(keys string) {
		if err := tmuxRun("send-keys", "-t", sessionName, keys, "Enter"); err != nil {
			t.Logf("send-keys %q: %v", keys, err)
		}
	}

	// ── create detached tmux session ──────────────────────────────────
	if err := tmuxRun(
		"new-session", "-d",
		"-s", sessionName,
		"-x", "220", "-y", "50",
	); err != nil {
		t.Fatalf("tmux new-session: %v", err)
	}
	t.Cleanup(func() {
		_ = tmuxRun("kill-session", "-t", sessionName)
	})

	// ── launch fir inside the tmux pane ───────────────────────────────
	// Use the mock provider so the TUI starts without a real API key.
	// Set FIR_AGENT_DIR so it uses the mock models.json from TestMain.
	// Pass --extension schedule to make sure the extension is activated
	// (schedule.py has builtin:false so it won't auto-start otherwise).
	firCmd := fmt.Sprintf(
		"FIR_AGENT_DIR=%s %s --provider mock --model mock-model --session-dir %s --extension schedule",
		mockAgentDir,
		firBinary,
		projectDir,
	)
	if err := tmuxRun("send-keys", "-t", sessionName, "cd "+projectDir+" && "+firCmd, "Enter"); err != nil {
		t.Fatalf("send fir start cmd: %v", err)
	}

	// Wait for the fir TUI to be ready.  The TUI enters alternate-screen mode
	// and renders its input prompt; we wait for the ">" prompt character that
	// fir always shows in its input area.  Shell prompts (bash $, zsh %) don't
	// contain ">", so this is specific enough.  20 s covers slow CI machines.
	if !waitFor(">", 20*time.Second) {
		t.Logf("pane content:\n%s", capturePane())
		t.Fatal("fir TUI did not appear within 20s")
	}
	t.Logf("fir started; pane:\n%s", capturePane())

	// Give extensions a moment to fully start (session_start fires lazily
	// and the Python process takes ~1 s to handshake).
	time.Sleep(2 * time.Second)

	// ── step 1: schedule 30 minutes from now ─────────────────────────
	sendKeys("/schedule 30m")

	if !waitFor("Scheduled", 10*time.Second) {
		t.Logf("pane content:\n%s", capturePane())
		t.Fatal("'/schedule 30m' did not produce a 'Scheduled' confirmation within 10s")
	}
	t.Logf("schedule created; pane:\n%s", capturePane())

	// The countdown thread will also emit ⏰ in the status bar.
	// Wait briefly for the first status update.
	time.Sleep(1500 * time.Millisecond)

	// ── step 2: reexec ────────────────────────────────────────────────
	sendKeys("/reexec")

	// After /reexec, fir calls syscall.Exec which replaces the process.
	// The new fir process then emits session_start with the saved schedule
	// data, the schedule extension restores the schedule, and set_status
	// is called with "⏰ ...".
	//
	// We wait for ⏰ to re-appear in the pane, which means the schedule
	// was successfully persisted through the reexec.
	if !waitFor("⏰", 20*time.Second) {
		t.Logf("pane content after /reexec:\n%s", capturePane())
		t.Fatal("schedule status '⏰' did not reappear after /reexec within 20s.\n" +
			"This means schedules were not saved to the reexec sidecar before exec.")
	}
	t.Logf("✓ schedule survived /reexec; pane:\n%s", capturePane())
}
