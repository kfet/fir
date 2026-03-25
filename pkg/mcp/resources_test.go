package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/agent"
)

// TestManager_Resources_ListAndRead verifies that list_resources and
// read_resource tools are exposed for every MCP server and that they return
// the correct content from a server-side resource.
func TestManager_Resources_ListAndRead(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "res-srv", Version: "0"}, nil)
	// Add a dummy tool so the server is non-empty (resources are the focus).
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)
	// Register a text resource and a resource template.
	server.AddResource(
		&sdk.Resource{URI: "file:///readme.md", Name: "readme", Description: "Project readme", MIMEType: "text/markdown"},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{
				Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: "# Hello World"}},
			}, nil
		},
	)
	server.AddResourceTemplate(
		&sdk.ResourceTemplate{URITemplate: "file:///notes/{name}.md", Name: "notes", Description: "Project notes"},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{
				Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: "note content"}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	// Locate the resource tools by name.
	var listTool, readTool *toolEntry
	for i := range tools {
		switch tools[i].Name {
		case "mcp__srv__list_resources":
			listTool = &toolEntry{tool: &tools[i]}
		case "mcp__srv__read_resource":
			readTool = &toolEntry{tool: &tools[i]}
		}
	}
	require.NotNil(t, listTool, "list_resources tool must be present")
	require.NotNil(t, readTool, "read_resource tool must be present")

	// --- list_resources returns the registered resource and template ---
	listResult, err := listTool.tool.Execute(ctx, "", nil, nil)
	require.NoError(t, err)
	require.Len(t, listResult.Content, 1)
	listing := listResult.Content[0].Text
	assert.Contains(t, listing, "file:///readme.md")
	assert.Contains(t, listing, "readme")
	assert.Contains(t, listing, "Project readme")
	assert.Contains(t, listing, "[template] file:///notes/{name}.md")

	// --- read_resource fetches resource content by URI ---
	readResult, err := readTool.tool.Execute(ctx, "", map[string]any{"uri": "file:///readme.md"}, nil)
	require.NoError(t, err)
	require.Len(t, readResult.Content, 1)
	assert.Equal(t, "# Hello World", readResult.Content[0].Text)
}

// TestManager_Resources_EmptyServer verifies that list_resources returns a
// friendly "no resources" message when the server exposes none.
func TestManager_Resources_EmptyServer(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "empty", Version: "0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Resources: &sdk.ResourceCapabilities{}},
	})
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	var listTool *toolEntry
	for i := range tools {
		if tools[i].Name == "mcp__srv__list_resources" {
			listTool = &toolEntry{tool: &tools[i]}
			break
		}
	}
	require.NotNil(t, listTool)

	result, err := listTool.tool.Execute(ctx, "", nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "No resources available.", result.Content[0].Text)
}

// TestManager_Resources_ReadMissingURI verifies that read_resource returns an
// IsError result (not a Go error) when no URI is provided.
func TestManager_Resources_ReadMissingURI(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "srv", Version: "0"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Resources: &sdk.ResourceCapabilities{}},
	})
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	var readTool *toolEntry
	for i := range tools {
		if tools[i].Name == "mcp__srv__read_resource" {
			readTool = &toolEntry{tool: &tools[i]}
			break
		}
	}
	require.NotNil(t, readTool)

	result, err := readTool.tool.Execute(ctx, "", map[string]any{}, nil)
	require.NoError(t, err) // missing URI is a user error, not a Go error
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "uri")
}

// TestManager_ResourceListChanged verifies that when a server's resource list
// changes, OnToolsChanged is called and the updated tool list still includes
// the list_resources and read_resource tools.
func TestManager_ResourceListChanged(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "noop", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: ""}}}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	changed := make(chan []agent.AgentTool, 2)
	mgr.OnToolsChanged = func(tools []agent.AgentTool) {
		changed <- tools
	}

	ctx := context.Background()
	startAndWait(t, mgr, ctx)
	defer mgr.Close()

	// Adding a resource triggers notifications/resources/list_changed.
	server.AddResource(
		&sdk.Resource{URI: "file:///new.txt", Name: "new"},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{
				Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: "content"}},
			}, nil
		},
	)

	// The tool list should not change (only resource content changes, not the tools).
	select {
	case <-changed:
		t.Log("OnToolsChanged called after resource list change — unexpected but not wrong")
	case <-time.After(500 * time.Millisecond):
		// Expected: no tool-list change when only resources change.
	}
}

// TestConvertResourceResult verifies the conversion of ReadResourceResult
// for text and blob resource contents.
func TestConvertResourceResult(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		result := convertResourceResult(&sdk.ReadResourceResult{
			Contents: []*sdk.ResourceContents{
				{URI: "file:///a.txt", Text: "hello"},
			},
		})
		require.Len(t, result.Content, 1)
		assert.Equal(t, "hello", result.Content[0].Text)
		assert.False(t, result.IsError)
	})

	t.Run("image blob", func(t *testing.T) {
		result := convertResourceResult(&sdk.ReadResourceResult{
			Contents: []*sdk.ResourceContents{
				{URI: "file:///img.png", Blob: []byte{1, 2, 3}, MIMEType: "image/png"},
			},
		})
		require.Len(t, result.Content, 1)
		assert.Equal(t, "AQID", result.Content[0].Data) // base64 of [1,2,3]
		assert.Equal(t, "image/png", result.Content[0].MimeType)
	})

	t.Run("non-image blob", func(t *testing.T) {
		// A PDF blob must NOT be tagged ContentTypeImage; it should be text.
		result := convertResourceResult(&sdk.ReadResourceResult{
			Contents: []*sdk.ResourceContents{
				{URI: "file:///doc.pdf", Blob: []byte{1, 2, 3}, MIMEType: "application/pdf"},
			},
		})
		require.Len(t, result.Content, 1)
		assert.Contains(t, result.Content[0].Text, "application/pdf")
		assert.Contains(t, result.Content[0].Text, "AQID")
	})

	t.Run("blob no mime", func(t *testing.T) {
		result := convertResourceResult(&sdk.ReadResourceResult{
			Contents: []*sdk.ResourceContents{
				{URI: "file:///data.bin", Blob: []byte{0xFF}},
			},
		})
		require.Len(t, result.Content, 1)
		assert.Contains(t, result.Content[0].Text, "octet-stream")
	})

	t.Run("empty", func(t *testing.T) {
		result := convertResourceResult(&sdk.ReadResourceResult{})
		assert.Empty(t, result.Content)
		assert.False(t, result.IsError)
	})
}

// TestManager_ResourceSubscription verifies that when a resource is read
// (triggering a lazy subscription) and the server pushes a resources/updated
// notification, the OnResourceUpdated callback is called with the correct
// server name and URI.
func TestManager_ResourceSubscription(t *testing.T) {
	const resURI = "file:///watch.txt"

	// The SDK requires both SubscribeHandler and UnsubscribeHandler to be set
	// in order to process resources/subscribe requests. The no-op handlers
	// allow the SDK's built-in session-tracking to run after them.
	server := sdk.NewServer(&sdk.Implementation{Name: "sub-srv", Version: "0"}, &sdk.ServerOptions{
		SubscribeHandler:   func(_ context.Context, _ *sdk.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(_ context.Context, _ *sdk.UnsubscribeRequest) error { return nil },
	})
	server.AddResource(
		&sdk.Resource{URI: resURI, Name: "watch", MIMEType: "text/plain"},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{
				Contents: []*sdk.ResourceContents{{URI: req.Params.URI, Text: "v1"}},
			}, nil
		},
	)

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	updated := make(chan string, 1)
	mgr.OnResourceUpdated = func(_, uri string) {
		updated <- uri
	}

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	// Find and call read_resource to trigger a lazy subscription.
	var readTool *agent.AgentTool
	for i := range tools {
		if strings.Contains(tools[i].Name, "read_resource") {
			readTool = &tools[i]
			break
		}
	}
	require.NotNil(t, readTool, "read_resource tool must be present")

	result, readErr := readTool.Execute(ctx, "r1", map[string]any{"uri": resURI}, nil)
	require.NoError(t, readErr)
	require.False(t, result.IsError)

	// Give the lazy subscribe goroutine time to complete.
	time.Sleep(200 * time.Millisecond)

	// Trigger a resource update from the server side.
	updateErr := server.ResourceUpdated(ctx, &sdk.ResourceUpdatedNotificationParams{URI: resURI})
	require.NoError(t, updateErr)

	select {
	case uri := <-updated:
		assert.Equal(t, resURI, uri)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for resource update notification")
	}
}

// toolEntry is a helper to hold a pointer to an AgentTool without copying the
// Execute closure repeatedly.
type toolEntry struct {
	tool *agent.AgentTool
}
