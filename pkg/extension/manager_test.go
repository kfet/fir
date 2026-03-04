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

	// Only one tool should be registered (from allowed-ext).
	if n := pollToolCount(api, 1, 5*time.Second); n != 1 {
		t.Fatalf("expected 1 tool (allowed-ext only), got %d", n)
	}

	// Give a moment for any unexpected second registration.
	time.Sleep(100 * time.Millisecond)
	if n := api.toolCount(); n != 1 {
		t.Fatalf("expected exactly 1 tool, got %d (blocked-ext should have been skipped)", n)
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
