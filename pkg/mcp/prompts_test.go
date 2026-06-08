package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// buildPromptServer returns an SDK server with two prompts: "greet" (with an
// argument) and "farewell" (no arguments).
func buildPromptServer(t *testing.T) *sdk.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "prompt-srv", Version: "0"}, nil)
	// A dummy tool so startServer's tool loop has something to iterate.
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		},
	)
	server.AddPrompt(
		&sdk.Prompt{
			Name:        "greet",
			Description: "Greet someone",
			Arguments: []*sdk.PromptArgument{
				{Name: "name", Description: "Name to greet", Required: true},
			},
		},
		func(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			who := req.Params.Arguments["name"]
			return &sdk.GetPromptResult{
				Description: "A greeting prompt",
				Messages: []*sdk.PromptMessage{
					{Role: "user", Content: &sdk.TextContent{Text: "Hello, " + who + "!"}},
				},
			}, nil
		},
	)
	server.AddPrompt(
		&sdk.Prompt{Name: "farewell", Description: "Say farewell"},
		func(_ context.Context, _ *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{
				Messages: []*sdk.PromptMessage{
					{Role: "user", Content: &sdk.TextContent{Text: "Goodbye!"}},
				},
			}, nil
		},
	)
	return server
}

// findPromptTools returns the list_prompts and get_prompt tools from a tool slice.
func findPromptTools(t *testing.T, tools []agent.AgentTool, serverName string) (list, get *agent.AgentTool) {
	t.Helper()
	for i := range tools {
		switch tools[i].Name {
		case "mcp__" + serverName + "__list_prompts":
			list = &tools[i]
		case "mcp__" + serverName + "__get_prompt":
			get = &tools[i]
		}
	}
	require.NotNil(t, list, "list_prompts tool not found")
	require.NotNil(t, get, "get_prompt tool not found")
	return
}

// TestManager_Prompts_ListAndGet verifies that list_prompts and get_prompt
// tools are created for every server and return correct data.
func TestManager_Prompts_ListAndGet(t *testing.T) {
	server := buildPromptServer(t)
	mgr := NewManager(map[string]ServerConfig{"psrv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	listTool, getTool := findPromptTools(t, tools, "psrv")

	// --- list_prompts ---
	listResult, listErr := listTool.Execute(ctx, "", nil, nil)
	require.NoError(t, listErr)
	require.False(t, listResult.IsError)
	require.Len(t, listResult.Content, 1)
	text := listResult.Content[0].Text

	var prompts []map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &prompts))
	require.Len(t, prompts, 2)
	names := []string{prompts[0]["name"].(string), prompts[1]["name"].(string)}
	assert.ElementsMatch(t, []string{"greet", "farewell"}, names)

	// --- get_prompt with argument ---
	getResult, getErr := getTool.Execute(ctx, "", map[string]any{
		"name":      "greet",
		"arguments": map[string]any{"name": "World"},
	}, nil)
	require.NoError(t, getErr)
	require.False(t, getResult.IsError)
	require.Len(t, getResult.Content, 1)
	assert.Contains(t, getResult.Content[0].Text, "Hello, World!")
	assert.Contains(t, getResult.Content[0].Text, "[user]")
}

// TestManager_Prompts_GetNoArgs verifies that get_prompt works on a
// no-argument prompt.
func TestManager_Prompts_GetNoArgs(t *testing.T) {
	server := buildPromptServer(t)
	mgr := NewManager(map[string]ServerConfig{"psrv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	_, getTool := findPromptTools(t, tools, "psrv")

	result, err := getTool.Execute(ctx, "", map[string]any{"name": "farewell"}, nil)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content[0].Text, "Goodbye!")
}

// TestManager_Prompts_GetMissingName verifies that omitting "name" returns an
// error result (not a Go error).
func TestManager_Prompts_GetMissingName(t *testing.T) {
	server := buildPromptServer(t)
	mgr := NewManager(map[string]ServerConfig{"psrv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	_, getTool := findPromptTools(t, tools, "psrv")

	result, err := getTool.Execute(ctx, "", map[string]any{}, nil)
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

// TestConvertPromptResult covers the convertPromptResult helper for all
// content types.
func TestConvertPromptResult(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		r := &sdk.GetPromptResult{
			Description: "my prompt",
			Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.TextContent{Text: "hi"}},
				{Role: "assistant", Content: &sdk.TextContent{Text: "hello"}},
			},
		}
		res := convertPromptResult(r)
		assert.Equal(t, ai.ContentTypeText, res.Content[0].Type)
		assert.Contains(t, res.Content[0].Text, "Description: my prompt")
		assert.Contains(t, res.Content[0].Text, "[user]")
		assert.Contains(t, res.Content[0].Text, "hi")
		assert.Contains(t, res.Content[0].Text, "[assistant]")
		assert.Contains(t, res.Content[0].Text, "hello")
	})

	t.Run("image", func(t *testing.T) {
		r := &sdk.GetPromptResult{
			Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
			},
		}
		res := convertPromptResult(r)
		assert.Contains(t, res.Content[0].Text, "image/png")
	})

	t.Run("embedded_text_resource", func(t *testing.T) {
		r := &sdk.GetPromptResult{
			Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.EmbeddedResource{
					Resource: &sdk.ResourceContents{URI: "file:///a.txt", Text: "file contents"},
				}},
			},
		}
		res := convertPromptResult(r)
		assert.Contains(t, res.Content[0].Text, "file contents")
	})

	t.Run("embedded_blob_resource", func(t *testing.T) {
		r := &sdk.GetPromptResult{
			Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.EmbeddedResource{
					Resource: &sdk.ResourceContents{
						URI:      "file:///data.bin",
						MIMEType: "application/octet-stream",
						Blob:     []byte{0xDE, 0xAD},
					},
				}},
			},
		}
		res := convertPromptResult(r)
		// Small blob → base64 inline.
		assert.Contains(t, res.Content[0].Text, "3q0=") // base64 of 0xDE, 0xAD
	})

	t.Run("empty", func(t *testing.T) {
		res := convertPromptResult(&sdk.GetPromptResult{})
		assert.Equal(t, "", res.Content[0].Text)
	})

	t.Run("no_description", func(t *testing.T) {
		r := &sdk.GetPromptResult{
			Messages: []*sdk.PromptMessage{
				{Role: "user", Content: &sdk.TextContent{Text: "hi"}},
			},
		}
		res := convertPromptResult(r)
		assert.False(t, strings.Contains(res.Content[0].Text, "Description:"))
	})
}
