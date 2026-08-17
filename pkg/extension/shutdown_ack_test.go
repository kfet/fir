package extension

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A shutdown handler that takes real work to complete must not be cut off.
//
// The shutdown path used to emit session_end/session_shutdown fire-and-forget
// and then sleep a blind grace (250ms in Stop) before cancelling the bridge and
// killing the process. Any extension whose handler outlived that grace lost its
// work permanently — the builtin observe extension writes status="ended" to its
// sidecar in exactly that handler, so a clean exit could leave the sidecar
// reading "idle" forever, which observe's own crash detection then reports as a
// crashed session.
//
// The events are now awaited: the ack means the handler returned. This fixture's
// handler takes ~1s, far beyond any of the old grace periods, so this test fails
// deterministically against the fire-and-forget behaviour.
func TestStopAwaitsSlowShutdownHandler(t *testing.T) {
	requirePython(t)

	projectDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "shutdown-marker")

	// Uses the real Python SDK (not a hand-rolled protocol loop) so the SDK's
	// own ack path is under test too, not just the Go side.
	script := strings.TrimSpace(`#!/usr/bin/env python3
# ---
# name: slow-shutdown-ext
# ---
import time
import fir_ext

@fir_ext.on("session_shutdown")
def on_shutdown(params, ctx):
    # Simulates a handler doing real work (the observe extension writes a
    # sidecar here). Deliberately longer than every old grace period.
    time.sleep(1.0)
    with open(` + "`" + `MARKER` + "`" + `, "w") as f:
        f.write("ended")

fir_ext.run(name="slow-shutdown-ext")
`)
	script = strings.ReplaceAll(script, "`MARKER`", `"`+marker+`"`)

	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(extDir, "slow-shutdown-ext.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Generous: the fixture's 1s handler must not be clipped by the ack cap on a
	// loaded CI runner. Costs nothing on the happy path — the ack arrives as soon
	// as the handler returns.
	withShutdownAckTimeout(t, 15*time.Second)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json")))
	hash, _ := ComputeHash(scriptPath)
	_ = mgr.trust.RecordTrust(projectDir, "slow-shutdown-ext", hash)

	api := &mockBridgeAPI{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, projectDir, projectDir, api); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	waitForBridges(t, mgr, 1)

	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Stop returning is the synchronisation point: the ack has been received, so
	// the handler's write is already on disk. No polling.
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("shutdown handler was cut off before it could finish: %v\n"+
			"Stop must await the extension's acknowledgement, not sleep a fixed grace.", err)
	}
}

// waitForBridges blocks until the manager reports at least n running bridges.
// Generous cap: the extension handshake spawns a Python interpreter, which is
// slow on a loaded runner, and an early-exit poll pays nothing on success.
func waitForBridges(t *testing.T, mgr *Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		count := len(mgr.bridges)
		mgr.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d bridge(s) to start", n)
}

// withShutdownAckTimeout overrides the package-wide shutdown ack cap for one
// test and restores it afterwards. Always use this rather than assigning the var
// directly: it is shared by Stop, ReloadOne and ShutdownAndCollect, so a leaked
// override silently changes the shutdown semantics of every later test in the
// package (a zero value turns the awaited events back into fire-and-forget
// notifications).
func withShutdownAckTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := shutdownAckTimeout
	shutdownAckTimeout = d
	t.Cleanup(func() { shutdownAckTimeout = prev })
}
