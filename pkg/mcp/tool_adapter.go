package mcp

import (
	"context"
	"encoding/base64"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AdaptTool converts an *sdk.Tool + active *sdk.ClientSession into an agent.AgentTool.
//
// The resulting tool name is prefixed with "mcp__<serverName>__" to avoid
// collisions with built-in tools. Double underscores are safe for LLM tool
// calling namespaces.
//
// If registry is non-nil, tool calls with a non-empty toolCallID and a non-nil
// onUpdate callback will set a progress token on the MCP request so the server
// can report progress, and route incoming progress notifications back to onUpdate.
func AdaptTool(session *sdk.ClientSession, serverName string, tool *sdk.Tool, registry *progressRegistry) agent.AgentTool {
	label := tool.Title
	if label == "" {
		label = tool.Name + " (via " + serverName + ")"
	}

	// Capture tool name for closure (avoid loop variable capture issues).
	toolName := tool.Name

	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "mcp__" + serverName + "__" + toolName,
			Description: tool.Description,
			Parameters:  tool.InputSchema,
		},
		Label: label,
		Execute: func(
			ctx context.Context,
			toolCallID string,
			params map[string]any,
			onUpdate agent.AgentToolUpdateCallback,
		) (agent.AgentToolResult, error) {
			callParams := &sdk.CallToolParams{
				Name:      toolName,
				Arguments: params,
			}
			// When we have an update callback and a registry, attach a progress
			// token so the server can send progress notifications, and register
			// the callback so those notifications are forwarded to the caller.
			//
			// The entry is NOT eagerly unregistered after CallTool returns because
			// the SDK's readIncoming goroutine can unblock CallTool (via the
			// response) concurrently with handleAsync still dispatching an
			// earlier progress notification. Removing the callback too early
			// would cause the dispatch to silently miss the callback. Entries
			// accumulate in the registry for the session lifetime, which is
			// acceptable: toolCallIDs are unique, so stale entries never fire
			// spuriously, and the memory is bounded by the number of tool calls
			// in one session.
			if toolCallID != "" && registry != nil && onUpdate != nil {
				callParams.Meta = sdk.Meta{"progressToken": toolCallID}
				registry.register(toolCallID, onUpdate)
			}
			result, err := session.CallTool(ctx, callParams)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return convertResult(result), nil
		},
	}
}

// convertResult maps an *sdk.CallToolResult to an agent.AgentToolResult.
// Text content is mapped directly; image content is base64-encoded. Unknown
// content types are represented as a plain text placeholder.
func convertResult(result *sdk.CallToolResult) agent.AgentToolResult {
	content := make([]ai.ToolResultContent, 0, len(result.Content))
	for _, c := range result.Content {
		switch v := c.(type) {
		case *sdk.TextContent:
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: v.Text,
			})
		case *sdk.ImageContent:
			content = append(content, ai.ToolResultContent{
				Type:     ai.ContentTypeImage,
				Data:     base64.StdEncoding.EncodeToString(v.Data),
				MimeType: v.MIMEType,
			})
		default:
			// Audio, resource links, etc. — render as a text placeholder.
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: "[unsupported MCP content type]",
			})
		}
	}
	return agent.AgentToolResult{
		Content: content,
		IsError: result.IsError,
	}
}
