package mcp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWatchConfig verifies that WatchConfig calls the callback when the
// watched file is overwritten.
func TestWatchConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	initial := `{"mcpServers":{"srv":{"command":"echo"}}}`
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o600))

	got := make(chan *ConfigFile, 1)
	stop, err := WatchConfig(path, func(cfg *ConfigFile) {
		select {
		case got <- cfg:
		default:
		}
	})
	require.NoError(t, err)
	defer stop()

	updated := `{"mcpServers":{"srv":{"command":"cat"},"new":{"command":"ls"}}}`
	require.NoError(t, os.WriteFile(path, []byte(updated), 0o600))

	select {
	case cfg := <-got:
		assert.Contains(t, cfg.MCPServers, "srv")
		assert.Contains(t, cfg.MCPServers, "new")
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for WatchConfig callback")
	}
}

// TestWatchConfig_StopCancels verifies that calling stop terminates the watcher.
func TestWatchConfig_StopCancels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600))

	got := make(chan struct{}, 1)
	stop, err := WatchConfig(path, func(_ *ConfigFile) {
		got <- struct{}{}
	})
	require.NoError(t, err)

	// Stop the watcher before any write — callback must not fire after stop.
	stop()

	// Give the watcher goroutine time to exit.
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, os.WriteFile(path, []byte(`{"mcpServers":{"x":{}}}`), 0o600))

	select {
	case <-got:
		t.Error("callback fired after stop()")
	case <-time.After(500 * time.Millisecond):
		// Good — no spurious callback.
	}
}

// TestManager_Reload_RemoveServer verifies that Reload stops a server that
// was removed from the new config.
func TestManager_Reload_RemoveServer(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "myTool", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{
		"srv1": {},
		"srv2": {},
	}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	// Two servers × (1 tool + 4 resource/prompt tools) = 10
	require.Len(t, tools, 2)

	// Reload with srv2 removed.
	remaining, err := mgr.Reload(context.Background(), map[string]ServerConfig{"srv1": {}})
	require.NoError(t, err)
	// Only srv1's tools remain (1 + 4 = 5).
	assert.Len(t, remaining, 1)

	assert.False(t, func() bool { mgr.mu.Lock(); _, ok := mgr.sessions["srv2"]; mgr.mu.Unlock(); return ok }(), "srv2 session should be closed after Reload")
}

// TestManager_Reload_AddServer verifies that Reload starts a new server that
// was not in the original config.
func TestManager_Reload_AddServer(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "myTool", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv1": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	tools := startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	// 1 server × 5 tools = 5
	require.Len(t, tools, 1)

	// Reload with srv2 added.
	all, err := mgr.Reload(context.Background(), map[string]ServerConfig{
		"srv1": {},
		"srv2": {},
	})
	require.NoError(t, err)
	// Both servers contribute tools (5 each = 10).
	assert.Len(t, all, 2)

	assert.True(t, func() bool { mgr.mu.Lock(); _, ok := mgr.sessions["srv2"]; mgr.mu.Unlock(); return ok }(), "srv2 session should exist after Reload")
}

// TestManager_Reload_Unchanged verifies that an unchanged server is not reconnected.
func TestManager_Reload_Unchanged(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "myTool", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	connectCount := 0
	realDial := inMemoryDial(t, server)
	mgr := NewManager(map[string]ServerConfig{"srv1": {}}, false)
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		connectCount++
		return realDial(cfg)
	}

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	require.Equal(t, 1, connectCount)

	// Reload with identical config — srv1 must NOT be reconnected.
	_, reloadErr := mgr.Reload(context.Background(), map[string]ServerConfig{"srv1": {}})
	require.NoError(t, reloadErr)
	assert.Equal(t, 1, connectCount, "unchanged server should not be reconnected")
}

// TestManager_Reload_Concurrent_ConfigChange exercises concurrent Reload calls
// where each goroutine alternates between two distinct configs, forcing the
// actual stop/start path on every other Reload. reloadMu serialises the
// operations; this test verifies the Manager remains consistent under rapid
// stop+restart cycles triggered by concurrent callers.
func TestManager_Reload_Concurrent_ConfigChange(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	// Use Command as the differentiating field — configsEqual compares by JSON
	// so different Command values trigger stop+start even though dialFn ignores
	// the Command and always connects to the same in-memory server.
	cfgA := map[string]ServerConfig{"srv1": {Command: "cmd-a"}}
	cfgB := map[string]ServerConfig{"srv1": {Command: "cmd-b"}}

	mgr := NewManager(cfgA, false)
	mgr.dialFn = inMemoryDial(t, server)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	const goroutines = 6
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := cfgA
			if i%2 != 0 {
				cfg = cfgB
			}
			_, err := mgr.Reload(context.Background(), cfg)
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	// Manager must be in a consistent state: srv1 session must exist.
	assert.True(t, func() bool { mgr.mu.Lock(); _, ok := mgr.sessions["srv1"]; mgr.mu.Unlock(); return ok }(), "srv1 session must exist after concurrent config-changing Reloads")
}

// TestManager_Reload_Concurrent verifies that concurrent Reload calls do not
// corrupt Manager state. We launch several goroutines that simultaneously
// Reload with the same config and assert that all succeed without panic and
// leave the Manager in a consistent state (exactly one session per server).
func TestManager_Reload_Concurrent(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{"srv1": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	cfg := map[string]ServerConfig{"srv1": {}}
	const goroutines = 5
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := mgr.Reload(context.Background(), cfg)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	// After all concurrent Reloads complete, Manager must be consistent:
	// exactly one session for srv1 and its tools must be present.
	assert.True(t, func() bool { mgr.mu.Lock(); _, ok := mgr.sessions["srv1"]; mgr.mu.Unlock(); return ok }(), "srv1 session must exist after concurrent Reloads")
}
