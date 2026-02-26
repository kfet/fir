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
func AdaptTool(session *sdk.ClientSession, serverName string, tool *sdk.Tool) agent.AgentTool {
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
			result, err := session.CallTool(ctx, &sdk.CallToolParams{
				Name:      toolName,
				Arguments: params,
			})
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
