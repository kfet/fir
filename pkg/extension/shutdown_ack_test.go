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

// A legacy extension that never acks must delay shutdown once, not once per
// event. Both shutdown events share a single shutdownAckTimeout budget; without
// that, a non-acking extension subscribed to both costs 2x the timeout on every
// exit. Timed against awaitShutdownEvents directly rather than Stop, so process
// teardown (which has its own multi-second SIGTERM/SIGKILL bounds) cannot swamp
// the signal.
func TestNonAckingExtensionCostsOneBudgetNotTwo(t *testing.T) {
	requirePython(t)

	// Hand-rolled protocol: subscribes to both shutdown events and deliberately
	// never replies, exactly as an extension built against an older SDK behaves.
	script := strings.TrimSpace(`#!/usr/bin/env python3
# ---
# name: silent-ext
# ---
import json, sys

def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()

msg = json.loads(sys.stdin.readline())
send({"jsonrpc": "2.0", "id": msg["id"], "result": {
    "name": "silent-ext",
    "tools": [],
    "events": ["session_end", "session_shutdown"]
}})

# Read forever, answer nothing.
while sys.stdin.readline():
    pass
`)

	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(extDir, "silent-ext.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	const budget = 1 * time.Second
	withShutdownAckTimeout(t, budget)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json")))
	hash, _ := ComputeHash(scriptPath)
	_ = mgr.trust.RecordTrust(projectDir, "silent-ext", hash)

	api := &mockBridgeAPI{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx, projectDir, projectDir, api); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })
	waitForBridges(t, mgr, 1)

	mgr.mu.Lock()
	bridges := append([]*managedBridge(nil), mgr.bridges...)
	mgr.mu.Unlock()

	start := time.Now()
	awaitShutdownEvents(bridges, SessionEndPayload{Reason: "test"})
	elapsed := time.Since(start)

	// Correct: ~1 budget. Regression (per-event budgets): ~2. The gap is wide
	// because nothing but goroutine scheduling sits between the two waits.
	if elapsed > budget*9/5 {
		t.Errorf("awaitShutdownEvents took %v with a %v budget — the shutdown events "+
			"are not sharing one budget (a non-acking extension is being waited on twice)",
			elapsed, budget)
	}
	// Sanity: it really did wait, i.e. the extension genuinely never acked and we
	// are not measuring an early return that would make the bound meaningless.
	if elapsed < budget/2 {
		t.Errorf("awaitShutdownEvents returned in %v, well under the %v budget — "+
			"the fixture must not be acking; this assertion is not testing anything",
			elapsed, budget)
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
