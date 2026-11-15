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
)

// TestManager_LoadSessionExtension loads a script that lives outside every
// discovery dir into the session, then checks it shadows a discovered
// extension of the same name and survives a targeted reload.
func TestManager_LoadSessionExtension(t *testing.T) {
	withShutdownAckTimeout(t, 0)
	dir := t.TempDir()
	projPath := writeExtScriptTool(t, dir, "sx", "project_tool")
	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	trustExt(t, ts, dir, "sx", projPath)

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)
	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck
	if n := pollToolCount(api, builtinToolCount+1, 15*time.Second); n != builtinToolCount+1 {
		t.Fatalf("expected %d tools, got %d", builtinToolCount+1, n)
	}

	// The session script lives in its own dir, not .fir/extensions.
	other := t.TempDir()
	sessPath := writeExtScriptTool(t, other, "sx", "session_tool")

	api.mu.Lock()
	load := api.loadSessionFn
	api.mu.Unlock()
	if load == nil {
		t.Fatal("expected manager to wire SetLoadSessionFn")
	}
	if err := load("sx", sessPath); err != nil {
		t.Fatalf("LoadSessionExtension: %v", err)
	}
	names := api.toolNameSet()
	if names["project_tool"] || !names["session_tool"] {
		t.Fatalf("session extension must shadow the project one, got %v", names)
	}

	// A plain reload keeps the session-scoped file, not the project one.
	if err := mgr.ReloadOne(ctx, "sx"); err != nil {
		t.Fatal(err)
	}
	names = api.toolNameSet()
	if names["project_tool"] || !names["session_tool"] {
		t.Fatalf("reload must keep the session extension, got %v", names)
	}
	var found bool
	for _, e := range mgr.RunningExtensions() {
		if e.Name == "sx" {
			found = true
		}
	}
	if !found {
		t.Fatal("session extension not listed as loaded")
	}
	// Nothing was written into any discovery dir.
	if _, err := os.Stat(filepath.Join(dir, ".fir", "extensions", "sx")); err != nil {
		t.Fatalf("project file unexpectedly touched: %v", err)
	}
}

func TestManager_LoadSessionExtension_Rejects(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(slog.Default())
	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	p := writeExtScriptTool(t, t.TempDir(), "real", "t")
	if err := mgr.LoadSessionExtension(ctx, "other", p); err == nil || !strings.Contains(err.Error(), "not a loadable extension") {
		t.Fatalf("name mismatch must be rejected, got %v", err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.LoadSessionExtension(ctx, "real", p); err == nil {
		t.Fatal("non-executable file must be rejected")
	}
	if err := mgr.LoadSessionExtension(ctx, "", p); err == nil {
		t.Fatal("empty name must be rejected")
	}
}

func TestBridge_ReloadExtension_WithPathLoadsSessionOnly(t *testing.T) {
	b, extCodec := pipePair(&InitResult{Name: "caller-ext"})
	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"name":"other-ext","path":"/tmp/x/other-ext.py"}`)
	_ = extCodec.WriteRequest(21, "reload_extension", &params)
	resp := mustResponse(t, extCodec)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.loadSessionArgs) != 1 || api.loadSessionArgs[0] != [2]string{"other-ext", "/tmp/x/other-ext.py"} {
		t.Fatalf("path must route to LoadSessionExtension, got %v", api.loadSessionArgs)
	}
	if len(api.reloadNames) != 0 {
		t.Fatalf("path load must not call ReloadExtension, got %v", api.reloadNames)
	}
}

func TestSessionBridge_LoadSessionExtension(t *testing.T) {
	sb := &SessionBridge{}
	if err := sb.LoadSessionExtension("a", "/p"); err == nil {
		t.Fatal("expected error with no loader wired")
	}
	var got [2]string
	sb.SetLoadSessionFn(func(n, p string) error { got = [2]string{n, p}; return nil })
	if err := sb.LoadSessionExtension("a", "/p"); err != nil || got != [2]string{"a", "/p"} {
		t.Fatalf("got %v err %v", got, err)
	}
}

// TestManager_ReloadOne_FindsPackageExtension: a package-contributed
// extension (SetExtraExtensionFiles) must be reloadable by name.
func TestManager_ReloadOne_FindsPackageExtension(t *testing.T) {
	withShutdownAckTimeout(t, 0)
	dir := t.TempDir()
	pkgFile := writeExtScriptTool(t, t.TempDir(), "pkgext", "pkg_tool")
	mgr := NewManager(slog.Default())
	mgr.SetExtraExtensionFiles([]string{pkgFile})
	api := newMockAPI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck
	if err := mgr.ReloadOne(ctx, "pkgext"); err != nil {
		t.Fatalf("ReloadOne(pkgext): %v", err)
	}
	if !api.toolNameSet()["pkg_tool"] {
		t.Fatal("package extension tool missing after reload")
	}
}
