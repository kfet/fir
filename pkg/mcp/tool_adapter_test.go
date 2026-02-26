package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// emptySchema is the minimal valid JSON Schema for tools with no parameters.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// connectTestServer creates an in-memory MCP server with the given setup
// function and returns a connected ClientSession.
func connectTestServer(t *testing.T, setup func(*sdk.Server)) *sdk.ClientSession {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	if setup != nil {
		setup(server)
	}
	t1, t2 := sdk.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), t1, nil)
	require.NoError(t, err)
	client := sdk.NewClient(&sdk.Implementation{Name: "fir-test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })
	return session
}

func TestAdaptTool_NamePrefixing(t *testing.T) {
	session := connectTestServer(t, func(s *sdk.Server) {
		s.AddTool(&sdk.Tool{Name: "mytool", Description: "A tool", InputSchema: emptySchema},
			func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
			})
	})

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, result.Tools, 1)

	adapted := AdaptTool(session, "myserver", result.Tools[0])
	assert.Equal(t, "mcp__myserver__mytool", adapted.Name)
	assert.Equal(t, "A tool", adapted.Description)
}

func TestAdaptTool_LabelFallback(t *testing.T) {
	session := connectTestServer(t, func(s *sdk.Server) {
		s.AddTool(&sdk.Tool{Name: "calc", InputSchema: emptySchema},
			func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
			})
	})
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	adapted := AdaptTool(session, "srv", result.Tools[0])
	// No Title set — falls back to "<name> (via <server>)".
	assert.Equal(t, "calc (via srv)", adapted.Label)
}

func TestAdaptTool_LabelFromTitle(t *testing.T) {
	session := connectTestServer(t, func(s *sdk.Server) {
		s.AddTool(&sdk.Tool{Name: "calc", Title: "Calculator", InputSchema: emptySchema},
			func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
			})
	})
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	adapted := AdaptTool(session, "srv", result.Tools[0])
	assert.Equal(t, "Calculator", adapted.Label)
}

func TestAdaptTool_ParameterPassThrough(t *testing.T) {
	var gotArgs map[string]any
	session := connectTestServer(t, func(s *sdk.Server) {
		s.AddTool(
			&sdk.Tool{
				Name:        "echo",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
			},
			func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				if err := json.Unmarshal(req.Params.Arguments, &gotArgs); err != nil {
					return nil, err
				}
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echoed"}}}, nil
			},
		)
	})
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	adapted := AdaptTool(session, "s", result.Tools[0])

	params := map[string]any{"msg": "hello"}
	toolResult, err := adapted.Execute(context.Background(), "id1", params, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"msg": "hello"}, gotArgs)
	require.Len(t, toolResult.Content, 1)
	assert.Equal(t, "echoed", toolResult.Content[0].Text)
}

func TestConvertResult_Text(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: "hello"}},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Equal(t, "hello", out.Content[0].Text)
	assert.False(t, out.IsError)
}

func TestConvertResult_Image(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	r := &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.ImageContent{Data: imgData, MIMEType: "image/png"}},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "image", out.Content[0].Type)
	assert.Equal(t, base64.StdEncoding.EncodeToString(imgData), out.Content[0].Data)
	assert.Equal(t, "image/png", out.Content[0].MimeType)
}

func TestConvertResult_IsError(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: "something went wrong"}},
		IsError: true,
	}
	out := convertResult(r)
	assert.True(t, out.IsError)
	assert.Equal(t, "something went wrong", out.Content[0].Text)
}

func TestAdaptTool_Cancellation(t *testing.T) {
	started := make(chan struct{})
	session := connectTestServer(t, func(s *sdk.Server) {
		s.AddTool(&sdk.Tool{Name: "slow", InputSchema: emptySchema},
			func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			})
	})
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	adapted := AdaptTool(session, "s", result.Tools[0])

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := adapted.Execute(ctx, "id", nil, nil)
		done <- err
	}()
	<-started
	cancel()
	err = <-done
	assert.Error(t, err)
}

// TestConvertResult_UnknownContentType verifies that the default branch in
// convertResult maps unsupported content types (e.g. AudioContent) to a
// plain-text placeholder rather than panicking or silently dropping the item.
func TestConvertResult_UnknownContentType(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.AudioContent{Data: []byte{0x01, 0x02}, MIMEType: "audio/mpeg"},
		},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Equal(t, "[unsupported MCP content type]", out.Content[0].Text)
	assert.False(t, out.IsError)
}
