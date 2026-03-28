package mcp

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_Details_Connected(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "myTool", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	cfg := map[string]ServerConfig{
		"alpha": {Command: "alpha-cmd", Args: []string{"--flag"}},
	}
	mgr := NewManager(cfg, false)
	mgr.dialFn = inMemoryDial(t, server)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	details := mgr.Details()
	require.Len(t, details, 1)

	d := details[0]
	assert.Equal(t, "alpha", d.Name)
	assert.Equal(t, "connected", d.Status)
	assert.Equal(t, "alpha-cmd", d.Config.Command)
	assert.Equal(t, []string{"--flag"}, d.Config.Args)
	assert.Empty(t, d.Error)

	// Should have at least the tool we added.
	var toolNames []string
	for _, ti := range d.Tools {
		toolNames = append(toolNames, ti.Name)
	}
	assert.Contains(t, toolNames, "mcp__alpha__myTool")
}

func TestManager_Details_MultipleServers(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	mgr := NewManager(map[string]ServerConfig{
		"bravo": {Command: "b"},
		"alpha": {Command: "a"},
	}, false)
	mgr.dialFn = inMemoryDial(t, server)

	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	details := mgr.Details()
	require.Len(t, details, 2)

	// Should be sorted by name.
	assert.Equal(t, "alpha", details[0].Name)
	assert.Equal(t, "bravo", details[1].Name)
}

func TestManager_Details_Empty(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{}, false)
	startAndWait(t, mgr, context.Background())
	defer mgr.Close()

	details := mgr.Details()
	assert.Empty(t, details)
}

func TestDetailsFunc_Nil(t *testing.T) {
	assert.Nil(t, DetailsFunc(nil))
}
