package mcp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	require.Len(t, tools, 2)

	// Reload with srv2 removed.
	remaining, err := mgr.Reload(context.Background(), map[string]ServerConfig{"srv1": {}})
	require.NoError(t, err)
	assert.Len(t, remaining, 1)

	assert.False(t, mgr.hasSession("srv2"), "srv2 session should be closed after Reload")
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
	require.Len(t, tools, 1)

	// Reload with srv2 added.
	all, err := mgr.Reload(context.Background(), map[string]ServerConfig{
		"srv1": {},
		"srv2": {},
	})
	require.NoError(t, err)
	assert.Len(t, all, 2)

	assert.True(t, mgr.hasSession("srv2"), "srv2 session should exist after Reload")
}

// TestManager_Reload_ReconnectsDisconnected verifies that /reload restarts a
// server whose config is unchanged but whose session has dropped or errored.
// Regression test: previously Reload only restarted servers whose config
// differed, so a dead-but-still-configured MCP server (e.g. grafana on a
// transient port that went away) could not be recovered without restarting fir.
func TestManager_Reload_ReconnectsDisconnected(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "myTool", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	var connectCount atomic.Int32
	realDial := inMemoryDial(t, server)
	mgr := NewManager(map[string]ServerConfig{"srv1": {}}, false)
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		connectCount.Add(1)
		return realDial(cfg)
	}

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	require.Equal(t, int32(1), connectCount.Load())
	require.True(t, mgr.hasSession("srv1"))

	// Cancel the auto-reconnect loop so we can deterministically simulate
	// a "stuck disconnected" state without the loop racing to reconnect
	// before /reload runs. This mirrors the regression scenario: a server
	// that genuinely cannot self-heal (e.g. dialFn errors permanently),
	// where /reload is the user's explicit recovery action.
	mgr.withEntry("srv1", func(e *serverEntry) {
		if e.reconnectCancel != nil {
			e.reconnectCancel()
			e.reconnectCancel = nil
		}
		if e.session != nil {
			_ = e.session.Close()
		}
		e.session = nil
		e.tools = nil
		e.err = assert.AnError
	})

	// /reload with identical config should reconnect the dead server.
	_, reloadErr := mgr.Reload(context.Background(), map[string]ServerConfig{"srv1": {}})
	require.NoError(t, reloadErr)
	assert.Equal(t, int32(2), connectCount.Load(), "disconnected server should be reconnected by Reload")
	assert.True(t, mgr.hasSession("srv1"), "srv1 should have a live session after Reload")
	mgr.withEntry("srv1", func(e *serverEntry) {
		assert.NoError(t, e.err, "error should be cleared after successful reconnect")
	})
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
// actual stop/start path on every other Reload.
func TestManager_Reload_Concurrent_ConfigChange(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

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
	assert.True(t, mgr.hasSession("srv1"), "srv1 session must exist after concurrent config-changing Reloads")
}

// TestManager_Reload_Concurrent verifies that concurrent Reload calls do not
// corrupt Manager state.
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
	assert.True(t, mgr.hasSession("srv1"), "srv1 session must exist after concurrent Reloads")
}

// TestConfigsEqual verifies the config equality helper.
func TestConfigsEqual(t *testing.T) {
	a := ServerConfig{Command: "cmd", Args: []string{"--arg"}}
	b := ServerConfig{Command: "cmd", Args: []string{"--arg"}}
	assert.True(t, configsEqual(a, b))

	c := ServerConfig{Command: "other"}
	assert.False(t, configsEqual(a, c))
}
