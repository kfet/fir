// Package mcptest provides tiny fake MCP servers for tests.
package mcptest

import (
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Fixed values served by NewUIServer.
const (
	UIResourceURI = "ui://demo/app.html"
	UIHTML        = "<!doctype html><p>demo app</p>"
	UIMIMEType    = "text/html;profile=mcp-app"
	UIToolName    = "show_app"
	LinkURI       = "https://example.com/doc"
)

// NewUIServer returns an MCP server with one MCP Apps style tool,
// "show_app", whose definition _meta names a ui:// resource, plus that
// resource. The tool result carries _meta, structuredContent, an embedded
// resource and a resource_link, so every passthrough field is exercised.
func NewUIServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "ui-srv", Version: "1"}, nil)
	s.AddTool(&sdk.Tool{
		Name:        UIToolName,
		Description: "Show the demo app",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`),
		Meta:        sdk.Meta{"ui": map[string]any{"resourceUri": UIResourceURI}},
	}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args struct {
			N float64 `json:"n"`
		}
		if req.Params.Arguments != nil {
			_ = json.Unmarshal(req.Params.Arguments, &args)
		}
		return &sdk.CallToolResult{
			Meta:              sdk.Meta{"demo": "result-meta"},
			StructuredContent: map[string]any{"n": args.N},
			Content: []sdk.Content{
				&sdk.TextContent{Text: "shown"},
				&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: UIResourceURI, MIMEType: UIMIMEType, Text: UIHTML}},
				&sdk.ResourceLink{URI: LinkURI, Name: "doc"},
			},
		}, nil
	})
	s.AddResource(&sdk.Resource{URI: UIResourceURI, Name: "app", MIMEType: UIMIMEType},
		func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: req.Params.URI, MIMEType: UIMIMEType, Text: UIHTML}}}, nil
		})
	return s
}
