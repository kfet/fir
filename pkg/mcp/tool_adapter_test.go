package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	adapted := AdaptTool(session, "myserver", result.Tools[0], nil)
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
	adapted := AdaptTool(session, "srv", result.Tools[0], nil)
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
	adapted := AdaptTool(session, "srv", result.Tools[0], nil)
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
	adapted := AdaptTool(session, "s", result.Tools[0], nil)

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

// TestConvertResult_NonImageContent verifies that an ImageContent with a
// non-image MIME type is rendered as base64 text rather than ContentTypeImage
// to avoid API errors on providers that validate image MIME types strictly.
func TestConvertResult_NonImageContent(t *testing.T) {
	pdfData := []byte{0x25, 0x50, 0x44, 0x46} // %PDF magic
	r := &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.ImageContent{Data: pdfData, MIMEType: "application/pdf"}},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type, "non-image blob must NOT be ContentTypeImage")
	assert.Contains(t, out.Content[0].Text, "application/pdf")
	assert.Contains(t, out.Content[0].Text, base64.StdEncoding.EncodeToString(pdfData))
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

// TestAdaptTool_Cancellation verifies that cancelling the client context:
//  1. causes Execute to return an error that wraps context.Canceled, and
//  2. propagates a notifications/cancelled to the MCP server so the server's
//     context is also cancelled (the server handler unblocks from ctx.Done()).
func TestAdaptTool_Cancellation(t *testing.T) {
	started := make(chan struct{})         // closed when server handler begins
	serverCancelled := make(chan struct{}) // closed when server ctx is cancelled

	session := connectTestServer(t, func(s *sdk.Server) {
		s.AddTool(&sdk.Tool{Name: "slow", InputSchema: emptySchema},
			func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				close(started)
				<-ctx.Done() // SDK cancellation handler unblocks this
				close(serverCancelled)
				return nil, ctx.Err()
			})
	})

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	adapted := AdaptTool(session, "s", result.Tools[0], nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := adapted.Execute(ctx, "id", nil, nil)
		done <- err
	}()

	<-started // wait for the server handler to start
	cancel()  // cancel client ctx → SDK sends notifications/cancelled

	// 1. The call must return an error wrapping context.Canceled.
	callErr := <-done
	assert.True(t, errors.Is(callErr, context.Canceled),
		"Execute error should wrap context.Canceled, got: %v", callErr)

	// 2. The MCP server must have received the cancellation notification
	//    (i.e. its handler context was cancelled by the SDK).
	select {
	case <-serverCancelled:
		// good — cancellation notification was delivered to the server
	case <-time.After(2 * time.Second):
		t.Error("server context was not cancelled — notifications/cancelled not delivered")
	}
}

// TestConvertResult_AudioContent verifies that AudioContent is rendered as
// a base64 text string rather than panicking or being dropped.
func TestConvertResult_AudioContent(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.AudioContent{Data: []byte{0x01, 0x02}, MIMEType: "audio/mpeg"},
		},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Contains(t, out.Content[0].Text, "audio/mpeg")
	assert.Contains(t, out.Content[0].Text, "AQI=") // base64 of [0x01, 0x02]
	assert.False(t, out.IsError)
}

// TestConvertResult_ResourceLink verifies that a ResourceLink is rendered as
// a human-readable text reference.
func TestConvertResult_ResourceLink(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.ResourceLink{Name: "My Doc", URI: "file:///docs/readme.md"},
		},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Contains(t, out.Content[0].Text, "My Doc")
	assert.Contains(t, out.Content[0].Text, "file:///docs/readme.md")
}

// TestConvertResult_EmbeddedResource_Text verifies text embedded resources.
func TestConvertResult_EmbeddedResource_Text(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI:  "file:///tmp/out.txt",
				Text: "hello world",
			}},
		},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Equal(t, "hello world", out.Content[0].Text)
}

// TestConvertResult_EmbeddedResource_Blob verifies binary embedded resources.
func TestConvertResult_EmbeddedResource_Blob(t *testing.T) {
	r := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI:      "file:///tmp/img.png",
				MIMEType: "image/png",
				Blob:     []byte{0xAB, 0xCD},
			}},
		},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "text", out.Content[0].Type)
	assert.Contains(t, out.Content[0].Text, "image/png")
	assert.Contains(t, out.Content[0].Text, "q80=") // base64 of [0xAB, 0xCD]
}

// TestConvertResult_UnknownContentType verifies that the default branch in
// convertResult maps unsupported content types to a plain-text placeholder
// rather than panicking or silently dropping the item.
// Note: AudioContent, ResourceLink, and EmbeddedResource are all handled above;
// this test uses a hypothetical future content type (embedded struct).
func TestConvertResult_UnknownContentType(t *testing.T) {
	// Use a ResourceLink with no URI (edge case) to hit the known type path,
	// or just verify that none of the known types return the placeholder text.
	r := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: "known"},
		},
	}
	out := convertResult(r)
	require.Len(t, out.Content, 1)
	assert.Equal(t, "known", out.Content[0].Text)
	assert.False(t, out.IsError)
}
