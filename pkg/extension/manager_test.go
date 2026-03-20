package extension

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// builtinToolCount is the number of tools registered by builtin extensions
// (aside=1, install=4). Tests must add their own extensions' tool counts on top.
const builtinToolCount = 5

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
# ---
# name: ` + name + `
# ---
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

func writeExtScriptWithModes(t *testing.T, dir, name, modes string) string {
	t.Helper()
	extDir := filepath.Join(dir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(extDir, name)
	content := `#!/bin/sh
# ---
# modes: ` + modes + `
# ---
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"name":"` + name + `","tools":[{"name":"test_tool","description":"a test tool"}],"events":["session_start","turn_end"]}}'
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

	if n := pollToolCount(api, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d registered tools, got %d", builtinToolCount+1, n)
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

	if n := api.toolCount(); n != builtinToolCount {
		t.Fatalf("expected %d tool (untrusted ext skipped, builtins only), got %d", builtinToolCount, n)
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

	pollToolCount(api, 2, 5*time.Second)

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

	if n := pollToolCount(api, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools before reload, got %d", builtinToolCount+1, n)
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

	if n := pollToolCount(api, builtinToolCount+2, 5*time.Second); n != builtinToolCount+2 {
		t.Fatalf("expected %d tools after reload, got %d", builtinToolCount+2, n)
	}
}

// trackingMockAPI embeds mockBridgeAPI and adds UnregisterExtensionTools support.
type trackingMockAPI struct {
	*mockBridgeAPI
	unregisterCalled int
}

func (t *trackingMockAPI) UnregisterExtensionTools() {
	t.unregisterCalled++
	t.clearTools()
}

func TestManager_ReloadCallsUnregister(t *testing.T) {
	dir := t.TempDir()
	scriptPath := writeExtScript(t, dir, "unreg-ext")

	trustPath := filepath.Join(dir, "trust.json")
	ts := NewTrustStoreWithPath(trustPath)
	hash, err := ComputeHash(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.RecordTrust(dir, "unreg-ext", hash); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := &trackingMockAPI{mockBridgeAPI: newMockAPI()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}

	if n := pollToolCount(api.mockBridgeAPI, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools before reload, got %d", builtinToolCount+1, n)
	}

	// Reload without manually clearing tools — UnregisterExtensionTools should handle it.
	reloadCtx, reloadCancel := context.WithCancel(context.Background())
	defer reloadCancel()

	if err := mgr.Reload(reloadCtx); err != nil {
		t.Fatal(err)
	}

	if api.unregisterCalled != 1 {
		t.Fatalf("expected UnregisterExtensionTools called once, got %d", api.unregisterCalled)
	}

	// Should still have exactly 2 tools (not duplicates).
	if n := pollToolCount(api.mockBridgeAPI, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools after reload (no duplicates), got %d", builtinToolCount+1, n)
	}
}

func TestManager_AllowedNames(t *testing.T) {
	dir := t.TempDir()

	// Write two extensions.
	script1 := writeExtScript(t, dir, "allowed-ext")
	script2 := writeExtScript(t, dir, "blocked-ext")

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	hash1, _ := ComputeHash(script1)
	hash2, _ := ComputeHash(script2)
	ts.RecordTrust(dir, "allowed-ext", hash1)
	ts.RecordTrust(dir, "blocked-ext", hash2)

	api := newMockAPI()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mgr := NewManager(logger)
	mgr.SetTrustStore(ts)
	// Allow only "allowed-ext"; "blocked-ext" should be skipped.
	mgr.AllowedNames = []string{"allowed-ext"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	// Only one tool should be registered (from allowed-ext; aside builtin filtered by AllowedNames).
	if n := pollToolCount(api, 1, 5*time.Second); n != 1 {
		t.Fatalf("expected 1 tool (allowed-ext only), got %d", n)
	}

	// Give a moment for any unexpected extra registration.
	time.Sleep(100 * time.Millisecond)
	if n := api.toolCount(); n != 1 {
		t.Fatalf("expected exactly 1 tool, got %d (blocked-ext should have been skipped)", n)
	}
}

func TestManager_ActiveMode(t *testing.T) {
	dir := t.TempDir()

	script1 := writeExtScriptWithModes(t, dir, "acp-ext", "acp")
	script2 := writeExtScriptWithModes(t, dir, "tui-ext", "tui")

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	hash1, _ := ComputeHash(script1)
	hash2, _ := ComputeHash(script2)
	ts.RecordTrust(dir, "acp-ext", hash1)
	ts.RecordTrust(dir, "tui-ext", hash2)

	api := newMockAPI()
	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)
	mgr.ActiveMode = "acp"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	if n := pollToolCount(api, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools in acp mode, got %d", builtinToolCount+1, n)
	}
	time.Sleep(100 * time.Millisecond)
	if n := api.toolCount(); n != builtinToolCount+1 {
		t.Fatalf("expected exactly %d tools after filtering by mode, got %d", builtinToolCount+1, n)
	}
}

// makeCmdBridge creates a Manager with one bridge that advertises commands.
// The returned cleanup func stops the simulated extension.
func makeCmdBridge(t *testing.T, mgr *Manager, cmds []CommandSpec, dispatch func(name string, args []string) CommandResult) context.CancelFunc {
	t.Helper()

	// fir-side pipes: fir reads from fR, writes to cW; ext reads from cR, writes to fW.
	cR, cW := io.Pipe()
	fR, fW := io.Pipe()

	proc := &Process{
		cfg:      ExtProcConfig{Name: "cmd-ext", Path: "/fake", Scope: "project"},
		stdin:    cW,
		codec:    NewCodec(fR, cW),
		waitDone: make(chan struct{}),
	}

	caps := &InitResult{
		Name:     "cmd-ext",
		Commands: cmds,
	}
	bridge := NewBridge(proc, caps)

	ctx, cancel := context.WithCancel(context.Background())

	// Simulate the extension: respond to hook/command calls.
	extCodec := NewCodec(cR, fW)
	go func() {
		defer cR.Close()
		defer fW.Close()
		for {
			msg, err := extCodec.ReadMessage()
			if err != nil {
				return
			}
			req, ok := msg.(*Request)
			if !ok {
				continue
			}
			if req.Method == "hook/command" {
				var p struct {
					Name string   `json:"name"`
					Args []string `json:"args"`
				}
				if req.Params != nil {
					_ = json.Unmarshal(*req.Params, &p)
				}
				result := dispatch(p.Name, p.Args)
				_ = extCodec.WriteResponse(req.ID, result, nil)
			}
		}
	}()

	go func() {
		if err := bridge.Run(ctx, newMockAPI()); err != nil && ctx.Err() == nil {
			t.Logf("bridge exited: %v", err)
		}
	}()

	mgr.mu.Lock()
	mgr.bridges = append(mgr.bridges, &managedBridge{
		cfg:    proc.cfg,
		proc:   proc,
		bridge: bridge,
		cancel: cancel,
	})
	mgr.mu.Unlock()

	return func() {
		cancel()
		cW.Close()
		fR.Close()
	}
}

func TestManager_GetCommands(t *testing.T) {
	mgr := NewManager(slog.Default())
	cmds := []CommandSpec{
		{Name: "hello", Description: "Say hello"},
		{Name: "status", Description: "Show status"},
	}
	cleanup := makeCmdBridge(t, mgr, cmds, func(name string, args []string) CommandResult {
		return CommandResult{}
	})
	defer cleanup()

	got := mgr.GetCommands()
	if len(got) != 2 {
		t.Fatalf("GetCommands: want 2, got %d", len(got))
	}
	if got[0].Spec.Name != "hello" || got[1].Spec.Name != "status" {
		t.Errorf("unexpected commands: %+v", got)
	}
	if got[0].ExtName != "cmd-ext" {
		t.Errorf("ExtName = %q, want cmd-ext", got[0].ExtName)
	}
}

func TestManager_DispatchCommand(t *testing.T) {
	mgr := NewManager(slog.Default())
	cmds := []CommandSpec{{Name: "greet", Description: "Greet"}}
	cleanup := makeCmdBridge(t, mgr, cmds, func(name string, args []string) CommandResult {
		msg := "hi"
		if len(args) > 0 {
			msg = "hi " + args[0]
		}
		return CommandResult{Message: msg}
	})
	defer cleanup()

	result, err := mgr.DispatchCommand("greet", []string{"alice"}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "hi alice" {
		t.Errorf("message = %q, want %q", result.Message, "hi alice")
	}
}

func TestManager_DispatchCommand_NotFound(t *testing.T) {
	mgr := NewManager(slog.Default())

	_, err := mgr.DispatchCommand("nonexistent", nil, time.Second)
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestManager_GetCommands_Empty(t *testing.T) {
	mgr := NewManager(slog.Default())
	if got := mgr.GetCommands(); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestManager_EnabledExtensionNames_FromAllowedNames(t *testing.T) {
	mgr := NewManager(slog.Default())
	mgr.AllowedNames = []string{"zeta", "alpha", "alpha", ""}

	got := mgr.EnabledExtensionNames()
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestManager_EnabledExtensionNames_FromRunningExtensions(t *testing.T) {
	mgr := NewManager(slog.Default())
	cleanup := makeCmdBridge(t, mgr, nil, func(name string, args []string) CommandResult {
		return CommandResult{}
	})
	defer cleanup()

	got := mgr.EnabledExtensionNames()
	if len(got) != 1 || got[0] != "cmd-ext" {
		t.Fatalf("EnabledExtensionNames = %v, want [cmd-ext]", got)
	}
}

// ---------------------------------------------------------------------------
// CollectSessionData / SeedSessionData tests
// ---------------------------------------------------------------------------

func TestManager_CollectSessionData(t *testing.T) {
	mgr := NewManager(slog.Default())
	cleanup := makeCmdBridge(t, mgr, nil, func(string, []string) CommandResult { return CommandResult{} })
	defer cleanup()

	// Verify empty store returns nil.
	if got := mgr.CollectSessionData(); got != nil {
		t.Fatalf("expected nil for empty store, got %v", got)
	}

	// Write directly to the bridge's store.
	mgr.mu.Lock()
	b := mgr.bridges[0].bridge
	mgr.mu.Unlock()

	b.SetSessionData("k1", "v1")
	b.SetSessionData("k2", "v2")

	data := mgr.CollectSessionData()
	if len(data) != 1 {
		t.Fatalf("expected 1 extension entry, got %d", len(data))
	}
	extData, ok := data["cmd-ext"]
	if !ok {
		t.Fatal("expected entry for 'cmd-ext'")
	}
	if extData["k1"] != "v1" || extData["k2"] != "v2" {
		t.Fatalf("unexpected data: %v", extData)
	}
}

func TestManager_SeedSessionData(t *testing.T) {
	mgr := NewManager(slog.Default())
	cleanup := makeCmdBridge(t, mgr, nil, func(string, []string) CommandResult { return CommandResult{} })
	defer cleanup()

	mgr.SeedSessionData(map[string]map[string]string{
		"cmd-ext": {"seeded": "yes"},
	})

	mgr.mu.Lock()
	b := mgr.bridges[0].bridge
	mgr.mu.Unlock()

	v, ok := b.GetSessionData("seeded")
	if !ok || v != "yes" {
		t.Fatalf("expected seeded=yes, got (%q, %v)", v, ok)
	}
}

func TestManager_SeedSessionData_UnknownExtIgnored(t *testing.T) {
	mgr := NewManager(slog.Default())
	cleanup := makeCmdBridge(t, mgr, nil, func(string, []string) CommandResult { return CommandResult{} })
	defer cleanup()

	// Should not panic or error for an extension name not in bridges.
	mgr.SeedSessionData(map[string]map[string]string{
		"nonexistent-ext": {"k": "v"},
	})
}
