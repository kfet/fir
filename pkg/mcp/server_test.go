package mcp

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// makeAgentTool builds a simple agent.AgentTool for tests.
func makeAgentTool(name, desc string, fn func(params map[string]any) string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        name,
			Description: desc,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string"},
				},
			},
		},
		Execute: func(_ context.Context, _ string, params map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			text := fn(params)
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

func TestNewToolServer_ListTools(t *testing.T) {
	tools := []agent.AgentTool{
		makeAgentTool("echo", "Echo text", func(p map[string]any) string {
			if s, ok := p["input"].(string); ok {
				return s
			}
			return ""
		}),
		makeAgentTool("shout", "Uppercase text", func(p map[string]any) string {
			return "SHOUT"
		}),
	}

	server := NewToolServer(tools)
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 2)

	names := []string{result.Tools[0].Name, result.Tools[1].Name}
	assert.Contains(t, names, "echo")
	assert.Contains(t, names, "shout")
}

func TestNewToolServer_CallTool(t *testing.T) {
	tools := []agent.AgentTool{
		makeAgentTool("echo", "Echo text", func(p map[string]any) string {
			if s, ok := p["input"].(string); ok {
				return s
			}
			return "(empty)"
		}),
	}

	server := NewToolServer(tools)
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "echo",
		Arguments: map[string]any{"input": "hello MCP"},
	})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	assert.Equal(t, "hello MCP", tc.Text)
}

func TestNewToolServer_ErrorResult(t *testing.T) {
	tools := []agent.AgentTool{
		{
			Tool: ai.Tool{Name: "fail", Description: "always fails"},
			Execute: func(_ context.Context, _ string, _ map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{
					IsError: true,
					Content: []ai.ToolResultContent{{Type: "text", Text: "something went wrong"}},
				}, nil
			},
		},
	}

	server := NewToolServer(tools)
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "fail"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	assert.Equal(t, "something went wrong", tc.Text)
}

func TestNewToolServer_Empty(t *testing.T) {
	server := NewToolServer(nil)
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Tools)
}

func TestConvertAgentToolResult_Image(t *testing.T) {
	r := agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{
				Type:     "image",
				Data:     "AQID", // base64 of [0x01, 0x02, 0x03]
				MimeType: "image/png",
			},
		},
	}
	out := convertAgentToolResult(r)
	require.Len(t, out.Content, 1)
	img, ok := out.Content[0].(*sdk.ImageContent)
	require.True(t, ok)
	assert.Equal(t, "image/png", img.MIMEType)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, img.Data)
}
