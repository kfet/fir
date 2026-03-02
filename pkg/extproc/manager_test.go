package extproc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Write a test extension script that responds to the init handshake
// and then stays alive reading from stdin.
func writeExtScript(t *testing.T, dir, name string) string {
	t.Helper()
	extDir := filepath.Join(dir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(extDir, name)
	content := `#!/bin/sh
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"name":"` + name + `","tools":[{"name":"test_tool","description":"a test tool"}],"events":["session_start","turn_end"]}}'
# Stay alive reading stdin until it closes
cat >/dev/null
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// pollToolCount waits until the mock API has at least n tools registered,
// or the deadline expires.
func pollToolCount(api *mockBridgeAPI, n int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if api.toolCount() >= n {
			return api.toolCount()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return api.toolCount()
}

func TestManager_StartStop(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeExtScript(t, dir, "test-ext")

	trustPath := filepath.Join(dir, "trust.json")
	ts := NewTrustStoreWithPath(trustPath)
	hash, err := ComputeHash(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.RecordTrust(dir, "test-ext", hash); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}

	if n := pollToolCount(api, 1, 5*time.Second); n != 1 {
		t.Fatalf("expected 1 registered tool, got %d", n)
	}

	if err := mgr.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestManager_UntrustedSkipped(t *testing.T) {
	dir := t.TempDir()
	writeExtScript(t, dir, "untrusted-ext")

	trustPath := filepath.Join(dir, "trust.json")
	ts := NewTrustStoreWithPath(trustPath)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}

	if n := api.toolCount(); n != 0 {
		t.Fatalf("expected 0 tools (untrusted), got %d", n)
	}

	mgr.Stop()
}

func TestManager_EmitEvent(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeExtScript(t, dir, "evt-ext")

	trustPath := filepath.Join(dir, "trust.json")
	ts := NewTrustStoreWithPath(trustPath)
	hash, _ := ComputeHash(scriptPath)
	ts.RecordTrust(dir, "evt-ext", hash)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}

	pollToolCount(api, 1, 5*time.Second)

	// EmitEvent should not panic; "session_start" is subscribed.
	mgr.EmitEvent("session_start", map[string]string{"test": "data"})

	// Unsubscribed event should be silently ignored.
	mgr.EmitEvent("unsubscribed_event", nil)

	mgr.Stop()
}

func TestManager_NoExtensions(t *testing.T) {
	dir := t.TempDir()

	mgr := NewManager(slog.Default())
	api := newMockAPI()
	ctx := context.Background()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestManager_ReloadBeforeStart(t *testing.T) {
	mgr := NewManager(slog.Default())
	err := mgr.Reload(context.Background())
	if err == nil {
		t.Fatal("expected error from Reload before Start")
	}
}

func TestManager_Reload(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeExtScript(t, dir, "reload-ext")

	trustPath := filepath.Join(dir, "trust.json")
	ts := NewTrustStoreWithPath(trustPath)
	hash, err := ComputeHash(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.RecordTrust(dir, "reload-ext", hash); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}

	if n := pollToolCount(api, 1, 5*time.Second); n != 1 {
		t.Fatalf("expected 1 tool before reload, got %d", n)
	}

	// Add a second extension before reload.
	script2 := writeExtScript(t, dir, "reload-ext2")
	hash2, _ := ComputeHash(script2)
	ts.RecordTrust(dir, "reload-ext2", hash2)

	// Reload with a fresh context.
	reloadCtx, reloadCancel := context.WithCancel(context.Background())
	defer reloadCancel()

	api.clearTools()

	if err := mgr.Reload(reloadCtx); err != nil {
		t.Fatal(err)
	}

	if n := pollToolCount(api, 2, 5*time.Second); n != 2 {
		t.Fatalf("expected 2 tools after reload, got %d", n)
	}
}
