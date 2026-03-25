package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/agent"
)

// ── unit tests ────────────────────────────────────────────────────────────────

func TestDefaultElicitFn_Decline(t *testing.T) {
	res, err := DefaultElicitFn(context.Background(), &sdk.ElicitRequest{
		Params: &sdk.ElicitParams{Message: "hello?"},
	})
	require.NoError(t, err)
	assert.Equal(t, "decline", res.Action)
}

func TestElicitFormResult(t *testing.T) {
	res := ElicitFormResult(map[string]any{"name": "Alice", "age": 30})
	assert.Equal(t, "accept", res.Action)
	assert.Equal(t, "Alice", res.Content["name"])
}

func TestElicitMessage(t *testing.T) {
	cases := []struct {
		params *sdk.ElicitParams
		want   string
	}{
		{
			params: &sdk.ElicitParams{Message: "What is your name?"},
			want:   "What is your name?",
		},
		{
			params: &sdk.ElicitParams{Mode: "url", URL: "https://example.com/auth"},
			want:   "Open URL: https://example.com/auth",
		},
		{
			params: &sdk.ElicitParams{},
			want:   "An MCP server is requesting input.",
		},
	}
	for _, tc := range cases {
		got := ElicitMessage(&sdk.ElicitRequest{Params: tc.params})
		assert.Equal(t, tc.want, got)
	}
}

// ── integration test ──────────────────────────────────────────────────────────

// TestManager_ElicitationFn verifies that Manager forwards elicitation/create
// requests from a server tool to the configured ElicitationFn.
func TestManager_ElicitationFn(t *testing.T) {
	// Server exposes a "collect_name" tool that triggers form elicitation.
	server := sdk.NewServer(&sdk.Implementation{Name: "elicit-srv", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "collect_name", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			res, err := req.Session.Elicit(ctx, &sdk.ElicitParams{
				Message: "What is your name?",
				RequestedSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
					},
				},
			})
			if err != nil {
				return &sdk.CallToolResult{
					IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}},
				}, nil
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: res.Action}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"esrv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	// Install an ElicitationFn that accepts with a canned name.
	elicitCalled := make(chan string, 1)
	mgr.ElicitationFn = func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		elicitCalled <- ElicitMessage(req)
		return ElicitFormResult(map[string]any{"name": "Alice"}), nil
	}

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	// Find the collect_name tool.
	var tool *agent.AgentTool
	for i := range tools {
		if tools[i].Name == "mcp__esrv__collect_name" {
			tool = &tools[i]
			break
		}
	}
	require.NotNil(t, tool, "collect_name tool not found")

	// Call the tool — triggers elicitation/create on the server side.
	result, execErr := tool.Execute(ctx, "", nil, nil)
	require.NoError(t, execErr)

	// The elicitation handler should have been called with the right message.
	select {
	case msg := <-elicitCalled:
		assert.Equal(t, "What is your name?", msg)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: ElicitationFn not called")
	}

	// The tool result should contain the "accept" action.
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "accept")
}

// TestManager_ElicitationFn_Default verifies that when no ElicitationFn is set,
// elicitation requests are declined.
func TestManager_ElicitationFn_Default(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "elicit-srv2", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "ask", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			res, err := req.Session.Elicit(ctx, &sdk.ElicitParams{Message: "confirm?"})
			if err != nil {
				return &sdk.CallToolResult{
					IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: err.Error()}},
				}, nil
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: res.Action}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"esrv2": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)
	// Leave ElicitationFn nil — should default to decline.

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	var tool *agent.AgentTool
	for i := range tools {
		if tools[i].Name == "mcp__esrv2__ask" {
			tool = &tools[i]
			break
		}
	}
	require.NotNil(t, tool)

	result, execErr := tool.Execute(ctx, "", nil, nil)
	require.NoError(t, execErr)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "decline")
}
