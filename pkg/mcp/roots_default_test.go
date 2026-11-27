package mcp

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A legacy server with no roots configured still gets the cwd root.
func TestManager_LegacyRootsDefaultToCWD(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "0"},
		&sdk.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	got := make(chan []string, 1)
	sdk.AddTool(server, &sdk.Tool{Name: "roots"}, func(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
		res, err := req.Session.ListRoots(ctx, nil)
		require.NoError(t, err)
		var uris []string
		for _, r := range res.Roots {
			uris = append(uris, r.URI)
		}
		got <- uris
		return &sdk.CallToolResult{}, nil, nil
	})
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)
	tools := startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	require.Len(t, tools, 1)
	_, err := tools[0].Execute(context.Background(), "id", map[string]any{}, nil)
	require.NoError(t, err)
	roots := <-got
	require.Len(t, roots, 1)
	assert.True(t, strings.HasPrefix(roots[0], "file:///"))
}

// A stateless server with no roots configured gets no roots/list_changed.
func TestManager_StatelessNoRootsListChanged(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "t", Version: "0"}, &sdk.ServerOptions{
		RootsListChangedHandler: func(context.Context, *sdk.RootsListChangedRequest) {
			t.Error("stateless server got roots/list_changed")
		},
	})
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{}, nil, nil
	})
	mgr := NewManager(map[string]ServerConfig{"s": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)
	tools := startAndWait(t, mgr, context.Background())
	defer mgr.Close()
	require.Len(t, tools, 1)
	_, err := tools[0].Execute(context.Background(), "id", map[string]any{}, nil)
	require.NoError(t, err)
}
