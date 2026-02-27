package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// listResourcesTool creates an agent tool that lists all resources and
// resource templates currently exposed by a server. The listing is performed
// live on each call so it always reflects the current server state.
func listResourcesTool(session *sdk.ClientSession, serverName string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "mcp__" + serverName + "__list_resources",
			Description: "List all resources and resource templates exposed by MCP server " + serverName + ". Call this before read_resource to discover available URIs.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Label: "List resources (" + serverName + ")",
		Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			var lines []string
			for res, err := range session.Resources(ctx, nil) {
				if err != nil {
					return resourceErrResult(err), nil
				}
				lines = append(lines, formatResource(res))
			}
			for tmpl, err := range session.ResourceTemplates(ctx, nil) {
				if err != nil {
					return resourceErrResult(err), nil
				}
				lines = append(lines, formatResourceTemplate(tmpl))
			}
			text := strings.Join(lines, "\n")
			if text == "" {
				text = "No resources available."
			}
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: text}},
			}, nil
		},
	}
}

// readResourceTool creates an agent tool that reads a resource by URI.
// Use list_resources to discover available URIs.
func readResourceTool(session *sdk.ClientSession, serverName string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "mcp__" + serverName + "__read_resource",
			Description: "Read the content of a resource from MCP server " + serverName + " by URI. Use list_resources first to discover available URIs.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"uri":{"type":"string","description":"URI of the resource to read"}},"required":["uri"]}`),
		},
		Label: "Read resource (" + serverName + ")",
		Execute: func(ctx context.Context, _ string, params map[string]any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			uri, _ := params["uri"].(string)
			if uri == "" {
				return agent.AgentToolResult{
					IsError: true,
					Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "uri parameter is required"}},
				}, nil
			}
			result, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return convertResourceResult(result), nil
		},
	}
}

// convertResourceResult maps a *sdk.ReadResourceResult to an agent.AgentToolResult.
// Text resources are returned as text; binary (blob) resources are base64-encoded
// and returned as image content.
func convertResourceResult(result *sdk.ReadResourceResult) agent.AgentToolResult {
	content := make([]ai.ToolResultContent, 0, len(result.Contents))
	for _, c := range result.Contents {
		switch {
		case c.Text != "":
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: c.Text,
			})
		case len(c.Blob) > 0:
			content = append(content, ai.ToolResultContent{
				Type:     ai.ContentTypeImage,
				Data:     base64.StdEncoding.EncodeToString(c.Blob),
				MimeType: c.MIMEType,
			})
		}
	}
	return agent.AgentToolResult{Content: content}
}

// formatResource formats a resource for display in list output.
func formatResource(r *sdk.Resource) string {
	b := &strings.Builder{}
	b.WriteString(r.URI)
	if r.Name != "" {
		b.WriteString(" (")
		b.WriteString(r.Name)
		b.WriteByte(')')
	}
	if r.Description != "" {
		b.WriteString(" — ")
		b.WriteString(r.Description)
	}
	return b.String()
}

// formatResourceTemplate formats a resource template for display in list output.
func formatResourceTemplate(t *sdk.ResourceTemplate) string {
	b := &strings.Builder{}
	b.WriteString("[template] ")
	b.WriteString(t.URITemplate)
	if t.Name != "" {
		b.WriteString(" (")
		b.WriteString(t.Name)
		b.WriteByte(')')
	}
	if t.Description != "" {
		b.WriteString(" — ")
		b.WriteString(t.Description)
	}
	return b.String()
}

// resourceErrResult wraps a resource-listing error as an IsError tool result
// so the LLM receives a descriptive message rather than a Go error.
func resourceErrResult(err error) agent.AgentToolResult {
	return agent.AgentToolResult{
		IsError: true,
		Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: err.Error()}},
	}
}
