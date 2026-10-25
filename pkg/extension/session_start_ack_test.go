package extension

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// prependRecorder is a BridgeAPI that records PrependContext calls.
type prependRecorder struct {
	mockBridgeAPI
	mu  sync.Mutex
	got []string
}

func (p *prependRecorder) PrependContext(c string) {
	p.mu.Lock()
	p.got = append(p.got, c)
	p.mu.Unlock()
}

func (p *prependRecorder) prepended() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.got...)
}

func withSessionStartAckTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := sessionStartAckTimeout
	sessionStartAckTimeout = d
	t.Cleanup(func() { sessionStartAckTimeout = old })
}

func startScriptExt(t *testing.T, name, script string, api BridgeAPI) *Manager {
	t.Helper()
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(extDir, name+".py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(NewTrustStoreWithPath(filepath.Join(t.TempDir(), "trust.json")))
	hash, _ := ComputeHash(scriptPath)
	_ = mgr.trust.RecordTrust(projectDir, name, hash)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := mgr.Start(ctx, projectDir, projectDir, api); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })
	waitForBridges(t, mgr, 1)
	return mgr
}

// session_start must be awaited: when EmitSessionStartWithData returns, the
// context a handler prepended has already landed, so a queued first prompt
// (fir -p, ACP session/prompt, a wt brief) is built with it. The handler
// sleeps first so a fire-and-forget emit fails this deterministically.
func TestSessionStart_AwaitsHandlerPrependContext(t *testing.T) {
	requirePython(t)
	withSessionStartAckTimeout(t, 15*time.Second)
	script := strings.TrimSpace(`#!/usr/bin/env python3
# ---
# name: start-prepender
# ---
import time
import fir_ext

@fir_ext.on("session_start")
def on_start(params, ctx):
    time.sleep(0.5)
    ctx.prepend("start-notes")

fir_ext.run(name="start-prepender")
`)
	api := &prependRecorder{}
	mgr := startScriptExt(t, "start-prepender", script, api)

	mgr.EmitSessionStartWithData(nil)

	if got := api.prepended(); len(got) != 1 || got[0] != "start-notes" {
		t.Fatalf("prepended = %q, want [start-notes] before EmitSessionStartWithData returns", got)
	}
}

// An extension that never acks session_start costs the bounded timeout once
// and is then left behind; startup must not hang.
func TestSessionStart_NonAckingExtensionIsBounded(t *testing.T) {
	requirePython(t)
	const budget = 500 * time.Millisecond
	withSessionStartAckTimeout(t, budget)
	script := strings.TrimSpace(`#!/usr/bin/env python3
# ---
# name: silent-start
# ---
import json, sys

msg = json.loads(sys.stdin.readline())
sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": {
    "name": "silent-start", "tools": [], "events": ["session_start"]}}) + "\n")
sys.stdout.flush()
while sys.stdin.readline():
    pass
`)
	mgr := startScriptExt(t, "silent-start", script, &mockBridgeAPI{})

	start := time.Now()
	mgr.EmitSessionStartWithData(nil)
	elapsed := time.Since(start)
	if elapsed < budget {
		t.Fatalf("returned in %v, before the %v budget — the event was not awaited", elapsed, budget)
	}
	if elapsed > budget+10*time.Second {
		t.Fatalf("took %v with a %v budget — wait is not bounded", elapsed, budget)
	}
}
