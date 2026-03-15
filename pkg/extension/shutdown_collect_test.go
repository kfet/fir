package extension

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestManager_ShutdownAndCollect_OrderingBug is the regression test for the
// bug where handleReexecCommand called CollectSessionData() *before* emitting
// session_shutdown, so any extension that only stores data in its
// session_shutdown handler (like the schedule extension) would always produce
// an empty sidecar.
//
// The test uses a minimal Python extension that:
//   - Subscribes to session_shutdown
//   - Calls set_session_data("shutdown_key", "shutdown_value") when it fires
//   - Never calls set_session_data at any other time
//
// Invariants verified:
//  1. CollectSessionData() before shutdown → nil  (would be the bug if used in reexec)
//  2. ShutdownAndCollect()               → non-nil data  (the correct /reexec path)
func TestManager_ShutdownAndCollect_OrderingBug(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	// Extension that ONLY saves data inside its session_shutdown handler.
	// Uses raw JSON-RPC (no fir_ext SDK) so the test has zero external deps.
	//
	// Frontmatter has NO events: field so the Manager starts it eagerly
	// (lazy extensions only start on their first matching event, but here
	// we need the extension to already be running before ShutdownAndCollect
	// fires session_shutdown).  The handshake result still declares the
	// session_shutdown subscription so the bridge routes the event.
	script := strings.TrimSpace(`#!/usr/bin/env python3
# ---
# name: shutdown-saves-data-ext
# ---
import json, sys

def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()

def recv():
    line = sys.stdin.readline()
    if not line:
        return None
    try:
        return json.loads(line.strip())
    except Exception:
        return None

# Handshake: respond to init, subscribe to session_shutdown only.
msg = recv()
if msg is None:
    sys.exit(1)
send({"jsonrpc": "2.0", "id": msg["id"], "result": {
    "name": "shutdown-saves-data-ext",
    "tools": [],
    "events": ["session_shutdown"]
}})

# Message loop.
while True:
    msg = recv()
    if msg is None:
        break
    method = msg.get("method", "")
    if method == "event/session_shutdown":
        # Notification: call set_session_data back to the host.
        send({"jsonrpc": "2.0", "id": 100, "method": "set_session_data",
              "params": {"key": "shutdown_key", "value": "shutdown_value"}})
        resp = recv()   # consume the host's ok response
    elif "id" in msg:
        # Service any other inbound request generically.
        send({"jsonrpc": "2.0", "id": msg["id"], "result": {"ok": True}})
`) + "\n"

	// Write extension to a temp project dir.
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(extDir, "shutdown-saves-data-ext.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json")))
	// Trust the extension without prompting.
	hash, _ := ComputeHash(scriptPath)
	_ = mgr.trust.RecordTrust(projectDir, "shutdown-saves-data-ext", hash)

	api := &mockBridgeAPI{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, projectDir, projectDir, api); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	// Give bridges a moment to complete the handshake.
	time.Sleep(300 * time.Millisecond)

	mgr.mu.Lock()
	if len(mgr.bridges) == 0 {
		mgr.mu.Unlock()
		t.Fatal("no bridges started — extension did not start correctly")
	}
	mgr.mu.Unlock()

	// ── invariant 1: CollectSessionData alone (the old buggy path) is empty ──
	// The extension only stores data in its session_shutdown handler.
	// Calling CollectSessionData() without first emitting the event returns nil.
	// This is exactly what the old /reexec code did, causing schedules to vanish.
	if got := mgr.CollectSessionData(); got != nil {
		t.Errorf("CollectSessionData() before shutdown = %v, want nil\n"+
			"(old /reexec bug: data would be lost)", got)
	}

	// ── invariant 2: ShutdownAndCollect fires session_shutdown first ────────
	// The extension's handler calls set_session_data, the bridge services it,
	// then we collect — so the data is present.
	got := mgr.ShutdownAndCollect()
	if got == nil {
		t.Fatal("ShutdownAndCollect() returned nil — session_shutdown was not emitted before collecting.\n" +
			"This is the /reexec ordering bug: schedules are lost on reexec.")
	}
	extData, ok := got["shutdown-saves-data-ext"]
	if !ok {
		t.Fatalf("ShutdownAndCollect() missing 'shutdown-saves-data-ext' key; got keys: %v", mapKeys(got))
	}
	if extData["shutdown_key"] != "shutdown_value" {
		t.Errorf("ShutdownAndCollect() data = %v, want {shutdown_key: shutdown_value}", extData)
	}
	t.Logf("✓ ShutdownAndCollect returned %v", got)
}

func mapKeys(m map[string]map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
