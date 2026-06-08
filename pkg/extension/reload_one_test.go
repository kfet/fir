package extension

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/agent"
)

// writeExtScriptTool writes a test extension that registers a single tool
// with the given name, then stays alive reading stdin. Distinct tool names
// let reload tests tell one extension's tools apart from another's.
func writeExtScriptTool(t *testing.T, dir, name, toolName string) string {
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
echo '{"jsonrpc":"2.0","id":1,"result":{"name":"` + name + `","tools":[{"name":"` + toolName + `","description":"a test tool"}],"events":["session_start"]}}'
cat >/dev/null
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func trustExt(t *testing.T, ts *TrustStore, dir, name, path string) {
	t.Helper()
	hash, err := ComputeHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.RecordTrust(dir, name, hash); err != nil {
		t.Fatal(err)
	}
}

// TestManager_ReloadOne_RefreshesOnlyTargetTools creates two extensions,
// edits one mid-session, reloads just that one, and asserts its tools are
// refreshed while the other extension's tools are untouched.
func TestManager_ReloadOne_RefreshesOnlyTargetTools(t *testing.T) {
	reloadGrace = 0
	dir := t.TempDir()
	pathA := writeExtScriptTool(t, dir, "ext-a", "a_tool")
	pathB := writeExtScriptTool(t, dir, "ext-b", "b_tool")

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	trustExt(t, ts, dir, "ext-a", pathA)
	trustExt(t, ts, dir, "ext-b", pathB)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)
	// Auto-trust on reload (the edited script changes its hash).
	mgr.ConfirmFn = func(_, _ string) bool { return true }

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	if n := pollToolCount(api, builtinToolCount+2, 5*time.Second); n != builtinToolCount+2 {
		t.Fatalf("expected %d tools, got %d", builtinToolCount+2, n)
	}
	names := api.toolNameSet()
	if !names["a_tool"] || !names["b_tool"] {
		t.Fatalf("expected a_tool and b_tool registered, got %v", names)
	}

	// Edit ext-a to register a different tool, then reload only ext-a via
	// the manager-wired callback (exercises the Start wiring closure too).
	writeExtScriptTool(t, dir, "ext-a", "a_tool_v2")
	api.mu.Lock()
	reloadFn := api.reloadFn
	api.mu.Unlock()
	if reloadFn == nil {
		t.Fatal("expected manager to wire SetReloadFn on the api")
	}
	if err := reloadFn("ext-a"); err != nil {
		t.Fatalf("ReloadOne(ext-a) failed: %v", err)
	}

	names = api.toolNameSet()
	if names["a_tool"] {
		t.Fatalf("expected old a_tool removed after reload, got %v", names)
	}
	if !names["a_tool_v2"] {
		t.Fatalf("expected refreshed a_tool_v2 registered, got %v", names)
	}
	if !names["b_tool"] {
		t.Fatalf("expected ext-b's b_tool untouched, got %v", names)
	}
}

// TestManager_ReloadOne_BuiltinRefused asserts a builtin extension is never
// reloadable.
func TestManager_ReloadOne_BuiltinRefused(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(slog.Default())
	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	err := mgr.ReloadOne(ctx, "aside")
	if err == nil {
		t.Fatal("expected error reloading builtin extension")
	}
	if !strings.Contains(err.Error(), "builtin") {
		t.Fatalf("expected 'builtin' in error, got %q", err.Error())
	}
}

// TestManager_ReloadOne_DeletedFileUnloads asserts that reloading an
// extension whose file was deleted cleanly unloads it (stop-only).
func TestManager_ReloadOne_DeletedFileUnloads(t *testing.T) {
	reloadGrace = 5 * time.Millisecond
	dir := t.TempDir()
	pathC := writeExtScriptTool(t, dir, "ext-c", "c_tool")

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	trustExt(t, ts, dir, "ext-c", pathC)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	if n := pollToolCount(api, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools, got %d", builtinToolCount+1, n)
	}
	if !api.toolNameSet()["c_tool"] {
		t.Fatal("expected c_tool registered")
	}

	// Delete the script and reload — should unload cleanly.
	if err := os.Remove(pathC); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ReloadOne(ctx, "ext-c"); err != nil {
		t.Fatalf("ReloadOne(ext-c) after delete failed: %v", err)
	}

	if api.toolNameSet()["c_tool"] {
		t.Fatal("expected c_tool removed after unload")
	}
	// The bridge must be gone from the manager.
	if got := mgr.ExtensionToolNames()["ext-c"]; got != nil {
		t.Fatalf("expected ext-c dropped from manager, still has tools %v", got)
	}
}

// TestManager_ReloadOneBeforeStart asserts ReloadOne errors before Start.
func TestManager_ReloadOneBeforeStart(t *testing.T) {
	mgr := NewManager(slog.Default())
	if err := mgr.ReloadOne(context.Background(), "anything"); err == nil {
		t.Fatal("expected error from ReloadOne before Start")
	}
}

// TestManager_StartOne_RefusesAppendAfterStop asserts that a respawn racing
// with Stop does not orphan the process: startOne rolls back when the
// manager is already stopped, leaving no bridge registered.
func TestManager_StartOne_RefusesAppendAfterStop(t *testing.T) {
	dir := t.TempDir()
	pathA := writeExtScriptTool(t, dir, "ext-stop", "stop_tool")

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	trustExt(t, ts, dir, "ext-stop", pathA)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)
	mgr.ConfirmFn = func(_, _ string) bool { return true }

	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	if n := pollToolCount(api, builtinToolCount+1, 5*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools, got %d", builtinToolCount+1, n)
	}

	// Stop the manager, then attempt a direct startOne respawn (simulating
	// a reload that lost the race with shutdown). It must not register.
	if err := mgr.Stop(); err != nil {
		t.Fatal(err)
	}
	cfgs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	var cfg *ExtProcConfig
	for i := range cfgs {
		if cfgs[i].Name == "ext-stop" {
			cfg = &cfgs[i]
			break
		}
	}
	if cfg == nil {
		t.Fatal("ext-stop not discovered")
	}
	if err := mgr.startOne(ctx, *cfg, dir, mgr.sdkEnv, api, dir); err != nil {
		t.Fatalf("startOne after stop should roll back cleanly, got %v", err)
	}
	mgr.mu.Lock()
	n := len(mgr.bridges)
	mgr.mu.Unlock()
	if n != 0 {
		t.Fatalf("expected no bridges registered after stop, got %d", n)
	}
}

// TestSessionBridge_RemoveExtensionTools verifies removeExtensionTools drops
// only the named tools and leaves the rest in the session's tool set.
func TestSessionBridge_RemoveExtensionTools(t *testing.T) {
	sess := newSetupTestSession(t, t.TempDir())
	sb := NewSessionBridge(sess)

	reg := func(name string) {
		sb.RegisterTool(ToolDefinition{
			Name:        name,
			Description: "x",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(ToolContext) (ToolResult, error) {
				return ToolResult{}, nil
			},
		})
	}
	reg("tool_a")
	reg("tool_b")
	reg("tool_c")

	has := func(name string) bool {
		var found bool
		sess.Agent.UpdateTools(func(ts *agent.ToolSet) { found = ts.Has(name) })
		return found
	}

	// Remove only tool_a and tool_c.
	sb.removeExtensionTools([]string{"tool_a", "tool_c"})
	if has("tool_a") || has("tool_c") {
		t.Fatal("expected tool_a and tool_c removed")
	}
	if !has("tool_b") {
		t.Fatal("expected tool_b retained")
	}
	// extTools tracking should now contain only tool_b.
	sb.mu.Lock()
	rem := append([]string(nil), sb.extTools...)
	sb.mu.Unlock()
	if len(rem) != 1 || rem[0] != "tool_b" {
		t.Fatalf("expected extTools == [tool_b], got %v", rem)
	}

	// Empty slice is a no-op.
	sb.removeExtensionTools(nil)
	if !has("tool_b") {
		t.Fatal("no-op removal should not drop tool_b")
	}
}

// TestSessionBridge_ReloadExtension_NoCallback asserts an error when no
// manager has wired a reload handler.
func TestSessionBridge_ReloadExtension_NoCallback(t *testing.T) {
	sb := &SessionBridge{}
	if err := sb.ReloadExtension("x"); err == nil {
		t.Fatal("expected error when no ReloadFn is registered")
	}
}

// TestSessionBridge_ReloadExtension_InvokesCallback asserts the wired handler
// receives the name.
func TestSessionBridge_ReloadExtension_InvokesCallback(t *testing.T) {
	sb := &SessionBridge{}
	got := make(chan string, 1)
	sb.SetReloadFn(func(name string) error {
		got <- name
		return nil
	})
	if err := sb.ReloadExtension("my-ext"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case n := <-got:
		if n != "my-ext" {
			t.Fatalf("got %q, want my-ext", n)
		}
	case <-time.After(time.Second):
		t.Fatal("reload callback was not invoked")
	}
}

// ---------------------------------------------------------------------------
// Bridge dispatch arm: reload_extension
// ---------------------------------------------------------------------------

func TestBridge_ReloadExtension_RPC(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "caller-ext"})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"name":"other-ext"}`)
	_ = extCodec.WriteRequest(11, "reload_extension", &params)

	resp := mustResponse(t, extCodec)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	waitFor(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.reloadNames) == 1 && api.reloadNames[0] == "other-ext"
	}, "reload_extension not delivered to BridgeAPI")
}

func TestBridge_ReloadExtension_SelfReloadRefused(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "caller-ext"})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"name":"caller-ext"}`)
	_ = extCodec.WriteRequest(12, "reload_extension", &params)

	resp := mustResponse(t, extCodec)
	if resp.Error == nil {
		t.Fatal("expected error response for self-reload")
	}
	if !strings.Contains(resp.Error.Message, "itself") {
		t.Fatalf("expected 'itself' in error, got %q", resp.Error.Message)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.reloadNames) != 0 {
		t.Fatalf("self-reload must not call api.ReloadExtension, got %v", api.reloadNames)
	}
}

func TestBridge_ReloadExtension_MissingName(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "caller-ext"})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"name":""}`)
	_ = extCodec.WriteRequest(13, "reload_extension", &params)

	resp := mustResponse(t, extCodec)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "name is required") {
		t.Fatalf("expected 'name is required' error, got %v", resp.Error)
	}
}

func TestBridge_ReloadExtension_InvalidParams(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "caller-ext"})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"name":12345}`)
	_ = extCodec.WriteRequest(14, "reload_extension", &params)

	resp := mustResponse(t, extCodec)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "invalid params") {
		t.Fatalf("expected invalid params error, got %v", resp.Error)
	}
}

func TestBridge_ReloadExtension_PropagatesError(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "caller-ext"})
	api := newMockAPI()
	api.mu.Lock()
	api.reloadErr = errTestReload
	api.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"name":"other-ext"}`)
	_ = extCodec.WriteRequest(15, "reload_extension", &params)

	resp := mustResponse(t, extCodec)
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "boom") {
		t.Fatalf("expected propagated error, got %v", resp.Error)
	}
}

func mustResponse(t *testing.T, codec *Codec) *Response {
	t.Helper()
	msg, err := codec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected Response, got %T", msg)
	}
	return resp
}

var errTestReload = errTest("boom")

type errTest string

func (e errTest) Error() string { return string(e) }
