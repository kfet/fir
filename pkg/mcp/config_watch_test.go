package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/agent"
)

// writeConfigFile marshals cfg as JSON and writes it to path.
func writeConfigFile(t *testing.T, path string, cfg ConfigFile) {
	t.Helper()
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// TestWatchConfig_Debounce verifies that rapid successive writes to a config
// file result in a single onChange call after the 200ms debounce window.
func TestWatchConfig_Debounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeConfigFile(t, path, ConfigFile{})

	calls := make(chan *ConfigFile, 10)
	stop, err := WatchConfig(path, func(cfg *ConfigFile) { calls <- cfg })
	require.NoError(t, err)
	t.Cleanup(stop)

	// Burst-write five successive configs. Each write resets the debounce timer.
	for i := 0; i < 5; i++ {
		writeConfigFile(t, path, ConfigFile{
			MCPServers: map[string]ServerConfig{
				"srv": {Command: "cmd"},
			},
		})
		time.Sleep(20 * time.Millisecond) // well inside the 200 ms window
	}

	// Exactly one call should fire after the debounce settles.
	select {
	case <-calls:
		// good — one call received
	case <-time.After(2 * time.Second):
		t.Fatal("onChange not called within timeout")
	}

	// No additional calls should arrive.
	select {
	case <-calls:
		t.Error("debounce failed: onChange called more than once for a burst of writes")
	case <-time.After(350 * time.Millisecond):
		// good — silent after debounce window
	}
}

// TestWatchConfig_ChangePropagated verifies that a single file write triggers
// onChange with the updated config contents.
func TestWatchConfig_ChangePropagated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeConfigFile(t, path, ConfigFile{})

	calls := make(chan *ConfigFile, 1)
	stop, err := WatchConfig(path, func(cfg *ConfigFile) { calls <- cfg })
	require.NoError(t, err)
	t.Cleanup(stop)

	writeConfigFile(t, path, ConfigFile{
		MCPServers: map[string]ServerConfig{
			"alpha": {Command: "alpha-cmd"},
		},
	})

	select {
	case cfg := <-calls:
		require.Len(t, cfg.MCPServers, 1)
		assert.Equal(t, "alpha-cmd", cfg.MCPServers["alpha"].Command)
	case <-time.After(2 * time.Second):
		t.Fatal("onChange not called after file write")
	}
}

// TestWatchConfig_StopCancelsWatch verifies that calling stop prevents future
// onChange calls.
func TestWatchConfig_StopCancelsWatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeConfigFile(t, path, ConfigFile{})

	calls := make(chan struct{}, 5)
	stop, err := WatchConfig(path, func(_ *ConfigFile) { calls <- struct{}{} })
	require.NoError(t, err)

	stop() // cancel the watcher
	time.Sleep(50 * time.Millisecond)

	writeConfigFile(t, path, ConfigFile{
		MCPServers: map[string]ServerConfig{"x": {Command: "cmd"}},
	})

	select {
	case <-calls:
		t.Error("onChange called after stop()")
	case <-time.After(500 * time.Millisecond):
		// good — no spurious call
	}
}

// TestWatchAndReload_AddServer verifies that a server added to the config file
// after WatchAndReload is started is connected and its tools appear.
func TestWatchAndReload_AddServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeConfigFile(t, path, ConfigFile{}) // start with no servers

	// Build a named in-memory server that exposes "tool-b".
	// Disable ListChanged so AddTool doesn't fire a ToolListChanged notification
	// that would race with the WatchAndReload callback.
	serverB := sdk.NewServer(&sdk.Implementation{Name: "b", Version: "0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: false}},
	})
	serverB.AddTool(
		&sdk.Tool{Name: "tool-b", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "b"}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{}, false)
	// dialFn routes by Command field so in-process servers are used.
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = serverB.Run(context.Background(), serverTransport) }()
		return clientTransport, nil
	}

	ctx := context.Background()
	_, err := mgr.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	toolsUpdated := make(chan []agent.AgentTool, 1)
	mgr.OnToolsChanged = func(tools []agent.AgentTool) {
		toolsUpdated <- tools
	}

	stop, err := mgr.WatchAndReload(ctx, path)
	require.NoError(t, err)
	t.Cleanup(stop)

	// Add server B to the config.
	writeConfigFile(t, path, ConfigFile{
		MCPServers: map[string]ServerConfig{
			"b": {Command: "b-cmd"},
		},
	})

	select {
	case tools := <-toolsUpdated:
		names := toolNames(tools)
		assert.Contains(t, names, "mcp__b__tool-b", "server B tool must appear after reload")
	case <-time.After(3 * time.Second):
		t.Fatal("OnToolsChanged not called after adding server to config")
	}
}

// TestWatchAndReload_RemoveServer verifies that a server removed from the
// config file is stopped and its tools disappear.
func TestWatchAndReload_RemoveServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")

	// Start with server A.
	writeConfigFile(t, path, ConfigFile{
		MCPServers: map[string]ServerConfig{
			"a": {Command: "a-cmd"},
		},
	})

	serverA := sdk.NewServer(&sdk.Implementation{Name: "a", Version: "0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: false}},
	})
	serverA.AddTool(
		&sdk.Tool{Name: "tool-a", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "a"}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"a": {Command: "a-cmd"}}, false)
	mgr.dialFn = func(_ ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = serverA.Run(context.Background(), serverTransport) }()
		return clientTransport, nil
	}

	ctx := context.Background()
	initialTools, err := mgr.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	// Verify server A started with its tool.
	assert.Contains(t, toolNames(initialTools), "mcp__a__tool-a")

	toolsUpdated := make(chan []agent.AgentTool, 1)
	mgr.OnToolsChanged = func(tools []agent.AgentTool) {
		toolsUpdated <- tools
	}

	stop, err := mgr.WatchAndReload(ctx, path)
	require.NoError(t, err)
	t.Cleanup(stop)

	// Remove server A from the config.
	writeConfigFile(t, path, ConfigFile{MCPServers: map[string]ServerConfig{}})

	select {
	case tools := <-toolsUpdated:
		assert.NotContains(t, toolNames(tools), "mcp__a__tool-a",
			"server A tool must disappear after removal from config")
	case <-time.After(3 * time.Second):
		t.Fatal("OnToolsChanged not called after removing server from config")
	}
}

// TestWatchAndReload_SwapServer verifies that a server replaced by a different
// one is stopped and the new one started.
func TestWatchAndReload_SwapServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeConfigFile(t, path, ConfigFile{
		MCPServers: map[string]ServerConfig{
			"svc": {Command: "cmd-a"},
		},
	})

	serverA := sdk.NewServer(&sdk.Implementation{Name: "a", Version: "0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: false}},
	})
	serverA.AddTool(
		&sdk.Tool{Name: "tool-a", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "a"}}}, nil
		},
	)

	serverB := sdk.NewServer(&sdk.Implementation{Name: "b", Version: "0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: false}},
	})
	serverB.AddTool(
		&sdk.Tool{Name: "tool-b", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "b"}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"svc": {Command: "cmd-a"}}, false)
	// Route by Command field to select which in-memory server to connect to.
	mgr.dialFn = func(cfg ServerConfig) (sdk.Transport, error) {
		var srv *sdk.Server
		switch cfg.Command {
		case "cmd-a":
			srv = serverA
		case "cmd-b":
			srv = serverB
		default:
			return nil, fmt.Errorf("unknown command: %s", cfg.Command)
		}
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = srv.Run(context.Background(), serverTransport) }()
		return clientTransport, nil
	}

	ctx := context.Background()
	initial, err := mgr.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	assert.Contains(t, toolNames(initial), "mcp__svc__tool-a")

	toolsUpdated := make(chan []agent.AgentTool, 1)
	mgr.OnToolsChanged = func(tools []agent.AgentTool) {
		toolsUpdated <- tools
	}

	stop, err := mgr.WatchAndReload(ctx, path)
	require.NoError(t, err)
	t.Cleanup(stop)

	// Swap the command: same server name "svc" but different Command.
	writeConfigFile(t, path, ConfigFile{
		MCPServers: map[string]ServerConfig{
			"svc": {Command: "cmd-b"},
		},
	})

	select {
	case tools := <-toolsUpdated:
		names := toolNames(tools)
		assert.Contains(t, names, "mcp__svc__tool-b", "new tool must appear after swap")
		assert.NotContains(t, names, "mcp__svc__tool-a", "old tool must disappear after swap")
	case <-time.After(3 * time.Second):
		t.Fatal("OnToolsChanged not called after swapping server config")
	}
}

// TestConfigsEqual verifies the config equality helper.
func TestConfigsEqual(t *testing.T) {
	a := ServerConfig{Command: "cmd", Args: []string{"--arg"}}
	b := ServerConfig{Command: "cmd", Args: []string{"--arg"}}
	assert.True(t, configsEqual(a, b))

	c := ServerConfig{Command: "other"}
	assert.False(t, configsEqual(a, c))
}

// toolNames extracts the Name field from a slice of AgentTool.
func toolNames(tools []agent.AgentTool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}
