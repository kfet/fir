package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// listPromptsTool returns an AgentTool that queries the MCP server for its
// available prompt templates and returns a JSON summary.
func listPromptsTool(session *sdk.ClientSession, serverName string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        sanitizeToolName("mcp__" + serverName + "__list_prompts"),
			Description: "List all prompt templates available on MCP server " + serverName + ". Call this before get_prompt to discover available names and arguments.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Label: "List prompts (" + serverName + ")",
		Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			type argInfo struct {
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				Required    bool   `json:"required,omitempty"`
			}
			type promptInfo struct {
				Name        string    `json:"name"`
				Title       string    `json:"title,omitempty"`
				Description string    `json:"description,omitempty"`
				Arguments   []argInfo `json:"arguments,omitempty"`
			}

			var prompts []promptInfo
			for p, err := range session.Prompts(ctx, nil) {
				if err != nil {
					return promptErrResult(serverName, "list", err), nil
				}
				info := promptInfo{
					Name:        p.Name,
					Title:       p.Title,
					Description: p.Description,
				}
				for _, arg := range p.Arguments {
					info.Arguments = append(info.Arguments, argInfo{
						Name:        arg.Name,
						Description: arg.Description,
						Required:    arg.Required,
					})
				}
				prompts = append(prompts, info)
			}
			if prompts == nil {
				prompts = []promptInfo{} // ensure JSON array, not null
			}

			b, _ := json.MarshalIndent(prompts, "", "  ")
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: ai.ContentTypeText, Text: string(b)},
				},
			}, nil
		},
	}
}

// getPromptTool returns an AgentTool that renders an MCP prompt template
// with optional arguments and returns the resulting messages.
func getPromptTool(session *sdk.ClientSession, serverName string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        sanitizeToolName("mcp__" + serverName + "__get_prompt"),
			Description: "Render a named prompt template from MCP server " + serverName + " with optional arguments.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string","description":"Name of the prompt template to render."},
					"arguments":{"type":"object","description":"Key-value pairs for the prompt template arguments.","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
		},
		Label: "Get prompt (" + serverName + ")",
		Execute: func(ctx context.Context, _ string, args map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return promptErrResult(serverName, "get", fmt.Errorf("required argument 'name' is missing or empty")), nil
			}

			// Collect string arguments from the optional map.
			var promptArgs map[string]string
			if raw, ok := args["arguments"]; ok {
				if m, ok := raw.(map[string]any); ok {
					promptArgs = make(map[string]string, len(m))
					for k, v := range m {
						promptArgs[k] = fmt.Sprintf("%v", v)
					}
				}
			}

			result, err := session.GetPrompt(ctx, &sdk.GetPromptParams{
				Name:      name,
				Arguments: promptArgs,
			})
			if err != nil {
				return promptErrResult(serverName, "get", err), nil
			}

			return convertPromptResult(result), nil
		},
	}
}

// convertPromptResult converts a GetPromptResult into an AgentToolResult.
// Each message is rendered as "<role>:\n<content>" separated by blank lines.
func convertPromptResult(result *sdk.GetPromptResult) agent.AgentToolResult {
	var sb strings.Builder
	if result.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n\n", result.Description)
	}
	for i, msg := range result.Messages {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "[%s]\n", msg.Role)
		switch c := msg.Content.(type) {
		case *sdk.TextContent:
			sb.WriteString(c.Text)
		case *sdk.ImageContent:
			fmt.Fprintf(&sb, "(image: %s, %d bytes)", c.MIMEType, len(c.Data))
		case *sdk.EmbeddedResource:
			sb.WriteString(formatEmbeddedResourceContent(c))
		default:
			sb.WriteString("(unsupported content type)")
		}
		sb.WriteString("\n")
	}
	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: ai.ContentTypeText, Text: sb.String()},
		},
	}
}

// formatEmbeddedResourceContent renders an EmbeddedResource's content inline.
func formatEmbeddedResourceContent(r *sdk.EmbeddedResource) string {
	if r.Resource == nil {
		return "(empty resource)"
	}
	rc := r.Resource
	if rc.Text != "" {
		return rc.Text
	}
	if rc.Blob != nil {
		// For small blobs, include base64; otherwise just a size hint.
		if len(rc.Blob) <= 4096 {
			return fmt.Sprintf("(blob: %s)\n%s", rc.MIMEType, base64.StdEncoding.EncodeToString(rc.Blob))
		}
		return fmt.Sprintf("(blob: %s, %d bytes)", rc.MIMEType, len(rc.Blob))
	}
	return fmt.Sprintf("(resource: %s)", rc.URI)
}

// promptErrResult returns an error tool result for a prompt operation.
func promptErrResult(serverName, op string, err error) agent.AgentToolResult {
	return agent.AgentToolResult{
		IsError: true,
		Content: []ai.ToolResultContent{
			{Type: ai.ContentTypeText, Text: fmt.Sprintf("mcp %s prompts (%s): %v", op, serverName, err)},
		},
	}
}
