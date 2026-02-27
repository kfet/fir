// mcp_e2e_test.go — end-to-end tests for the MCP client manager.
//
// These tests spawn the current test binary as a real stdio subprocess MCP
// server (see testserver_test.go / TestMain) and exercise the full
// client-manager-adapter stack over real OS pipes.
//
// No FIR_TEST_BINARY or network access is required: the test binary itself is
// reused via the MCP_TEST_SERVER=1 self-invocation pattern used by the Go
// standard library (e.g. os/exec tests).
package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpServerConfig returns a ServerConfig that re-invokes the current test
// binary as an MCP server subprocess via the MCP_TEST_SERVER=1 guard in
// TestMain. extraEnv is a flat list of "KEY", "VALUE" pairs merged on top of
// the inherited environment.
func mcpServerConfig(extraEnv ...string) ServerConfig {
	env := map[string]string{"MCP_TEST_SERVER": "1"}
	for i := 0; i+1 < len(extraEnv); i += 2 {
		env[extraEnv[i]] = extraEnv[i+1]
	}
	return ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^$"}, // suppress all test execution in subprocess
		Env:     env,
	}
}

// TestMCP_E2E_ListTools verifies that Manager connects to a real subprocess
// MCP server and lists the correct tools.
func TestMCP_E2E_ListTools(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"myserver": mcpServerConfig(),
	}, false)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tools, err := mgr.Start(ctx)
	require.NoError(t, err)

	// The test server registers echo, add, and slow.
	require.Len(t, tools, 3, "expected 3 tools from test server")

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	assert.True(t, names["mcp__myserver__echo"], "echo tool missing")
	assert.True(t, names["mcp__myserver__add"], "add tool missing")
	assert.True(t, names["mcp__myserver__slow"], "slow tool missing")
}

// TestMCP_E2E_CallTool verifies that tools registered on a subprocess MCP
// server are callable and return the correct results.
func TestMCP_E2E_CallTool(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"srv": mcpServerConfig(),
	}, false)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tools, err := mgr.Start(ctx)
	require.NoError(t, err)

	// Index by name for easy lookup.
	byName := make(map[string]int, len(tools))
	for i, tool := range tools {
		byName[tool.Name] = i
	}

	// -- echo tool --
	echoIdx, ok := byName["mcp__srv__echo"]
	require.True(t, ok, "echo tool not found")
	echoResult, err := tools[echoIdx].Execute(ctx, "call-1", map[string]any{"message": "hello"}, nil)
	require.NoError(t, err)
	require.Len(t, echoResult.Content, 1)
	assert.Equal(t, "echo: hello", echoResult.Content[0].Text)
	assert.False(t, echoResult.IsError)

	// -- add tool --
	addIdx, ok := byName["mcp__srv__add"]
	require.True(t, ok, "add tool not found")
	addResult, err := tools[addIdx].Execute(ctx, "call-2", map[string]any{"a": 3.0, "b": 4.0}, nil)
	require.NoError(t, err)
	require.Len(t, addResult.Content, 1)
	assert.Equal(t, "7", addResult.Content[0].Text)
}

// TestMCP_E2E_ToolNamePrefixing verifies the mcp__<server>__<tool> naming
// convention across all tools returned by the subprocess server.
func TestMCP_E2E_ToolNamePrefixing(t *testing.T) {
	const serverName = "specialserver"

	mgr := NewManager(map[string]ServerConfig{
		serverName: mcpServerConfig(),
	}, false)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tools, err := mgr.Start(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tools)

	wantPrefix := "mcp__" + serverName + "__"
	for _, tool := range tools {
		assert.True(t,
			strings.HasPrefix(tool.Name, wantPrefix),
			"tool name %q must start with %q", tool.Name, wantPrefix,
		)
		// The suffix after the prefix must be a non-empty bare tool name.
		suffix := strings.TrimPrefix(tool.Name, wantPrefix)
		assert.NotEmpty(t, suffix, "tool name %q has empty suffix", tool.Name)
		// No further double-underscore sequences in the suffix (no nesting).
		assert.False(t,
			strings.Contains(suffix, "__"),
			"tool suffix %q must not contain __ (found in %q)", suffix, tool.Name,
		)
	}
}

// TestMCP_E2E_MultipleServers verifies that Manager connects to two distinct
// subprocess MCP servers and merges their tool lists with per-server prefixes.
func TestMCP_E2E_MultipleServers(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"alpha": mcpServerConfig(),
		"beta":  mcpServerConfig(),
	}, false)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tools, err := mgr.Start(ctx)
	require.NoError(t, err)

	// Each server has 3 tools → 6 total.
	require.Len(t, tools, 6, "expected 3 tools × 2 servers = 6")

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}
	// Spot-check one tool from each server.
	assert.True(t, names["mcp__alpha__echo"], "alpha echo missing")
	assert.True(t, names["mcp__alpha__add"], "alpha add missing")
	assert.True(t, names["mcp__beta__echo"], "beta echo missing")
	assert.True(t, names["mcp__beta__add"], "beta add missing")

	// Call the echo tool on each server independently.
	for _, tool := range tools {
		if tool.Name != "mcp__alpha__echo" && tool.Name != "mcp__beta__echo" {
			continue
		}
		result, err := tool.Execute(ctx, "id", map[string]any{"message": "world"}, nil)
		require.NoError(t, err, "calling %s", tool.Name)
		require.Len(t, result.Content, 1)
		assert.Equal(t, "echo: world", result.Content[0].Text)
	}
}

// TestMCP_E2E_ServerExit verifies that when the server subprocess exits
// before completing the MCP handshake, Manager.Start returns an error and
// does not panic or hang.
func TestMCP_E2E_ServerExit(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"gone": mcpServerConfig("MCP_TEST_SERVER_MODE", "exit_immediately"),
	}, false)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := mgr.Start(ctx)
	require.Error(t, err, "expected error when server exits immediately")
	// The server name should appear in the error for debuggability.
	assert.Contains(t, err.Error(), "gone")
}

// TestMCP_E2E_ContextCancellation verifies that a context deadline imposed by
// the caller propagates through the JSON-RPC layer to the MCP server, causing
// an in-flight tool call to return an error rather than hanging forever.
func TestMCP_E2E_ContextCancellation(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"srv": mcpServerConfig(),
	}, false)
	defer mgr.Close()

	startCtx, startCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer startCancel()

	tools, err := mgr.Start(startCtx)
	require.NoError(t, err)

	// Find the slow tool.
	var slowIdx = -1
	for i, tool := range tools {
		if tool.Name == "mcp__srv__slow" {
			slowIdx = i
			break
		}
	}
	require.NotEqual(t, -1, slowIdx, "slow tool not found in tool list")

	// Call the slow tool with a short deadline; it blocks on the server side
	// until the context expires. The SDK must return an error rather than hang.
	callCtx, callCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer callCancel()

	_, err = tools[slowIdx].Execute(callCtx, "slow-call", nil, nil)
	assert.Error(t, err, "Execute must return an error when context expires")
}
