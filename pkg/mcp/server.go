package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/agent"
)

// NewToolServer creates an MCP server that exposes the provided fir tools as
// MCP tools. The server can be connected to any transport (stdio, streamable
// HTTP, etc.) and called by external MCP clients.
//
// Example (stdio mode, for use as a subprocess):
//
//	srv := mcp.NewToolServer(tools)
//	t := sdk.NewStdioTransport()
//	srv.Run(ctx, t)
func NewToolServer(tools []agent.AgentTool) *sdk.Server {
	impl := &sdk.Implementation{Name: "fir", Version: "dev"}
	server := sdk.NewServer(impl, nil)
	for i := range tools {
		registerTool(server, &tools[i])
	}
	return server
}

// registerTool adds a single AgentTool to the MCP server.
func registerTool(server *sdk.Server, tool *agent.AgentTool) {
	schema, err := toolInputSchema(tool)
	if err != nil {
		// Fall back to an empty object schema so the tool is still exposed.
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	mcpTool := &sdk.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: schema,
	}
	server.AddTool(mcpTool, makeToolHandler(tool))
}

// toolInputSchema converts an ai.Tool's Parameters field to a json.RawMessage
// suitable for sdk.Tool.InputSchema.
func toolInputSchema(tool *agent.AgentTool) (json.RawMessage, error) {
	if tool.Parameters == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	data, err := json.Marshal(tool.Parameters)
	if err != nil {
		return nil, fmt.Errorf("marshal tool %q schema: %w", tool.Name, err)
	}
	return data, nil
}

// makeToolHandler returns an sdk.ToolHandler that delegates to tool.Execute.
func makeToolHandler(tool *agent.AgentTool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var params map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &params); err != nil {
				return errorToolResult(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
		}

		result, err := tool.Execute(ctx, "", params, nil)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}

		return convertAgentToolResult(result), nil
	}
}

// convertAgentToolResult converts an agent.AgentToolResult to an sdk.CallToolResult.
func convertAgentToolResult(r agent.AgentToolResult) *sdk.CallToolResult {
	content := make([]sdk.Content, 0, len(r.Content))
	for _, c := range r.Content {
		switch c.Type {
		case "image":
			content = append(content, &sdk.ImageContent{
				Data:     decodeBase64OrRaw(c.Data),
				MIMEType: c.MimeType,
			})
		default: // text
			content = append(content, &sdk.TextContent{Text: c.Text})
		}
	}
	if len(content) == 0 {
		content = []sdk.Content{&sdk.TextContent{Text: ""}}
	}
	return &sdk.CallToolResult{
		Content: content,
		IsError: r.IsError,
	}
}

// errorToolResult creates an MCP error tool result with the given message.
func errorToolResult(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: msg}},
	}
}

// decodeBase64OrRaw attempts to base64-decode data; if that fails, returns the
// raw bytes of the string (treats it as already binary).
func decodeBase64OrRaw(s string) []byte {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b
	}
	return []byte(s)
}
