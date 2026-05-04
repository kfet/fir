package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
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
// invalidToolNameChars matches any character not allowed in LLM tool names.
// Anthropic requires: ^[a-zA-Z0-9_-]{1,128}$
var invalidToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeToolName replaces disallowed characters with underscores and
// truncates to 128 characters to satisfy LLM provider tool name constraints.
func sanitizeToolName(name string) string {
	name = invalidToolNameChars.ReplaceAllString(name, "_")
	if len(name) > 128 {
		name = name[:128]
	}
	return name
}

// SessionGetter returns the currently-active *sdk.ClientSession for a server.
// AdaptTool calls this on every tool invocation so calls survive transparent
// reconnects: the manager's auto-reconnect loop installs a fresh session on
// disconnect, and the next tool call uses it without the agent re-listing.
type SessionGetter func(ctx context.Context) (*sdk.ClientSession, error)

// StaticSession is a SessionGetter that always returns the same session.
// Useful for tests that want a fixed session without a Manager.
func StaticSession(s *sdk.ClientSession) SessionGetter {
	return func(_ context.Context) (*sdk.ClientSession, error) {
		if s == nil {
			return nil, fmt.Errorf("nil session")
		}
		return s, nil
	}
}

func AdaptTool(getSession SessionGetter, serverName string, tool *sdk.Tool, registry *progressRegistry) agent.AgentTool {
	label := tool.Title
	if label == "" {
		label = tool.Name + " (via " + serverName + ")"
	}

	// Capture tool name for closure (avoid loop variable capture issues).
	toolName := tool.Name

	// Sanitize the combined name to match Anthropic's tool name pattern:
	// ^[a-zA-Z0-9_-]{1,128}$
	sanitizedName := sanitizeToolName("mcp__" + serverName + "__" + toolName)

	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        sanitizedName,
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
			session, err := getSession(ctx)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("mcp session for %q: %w", serverName, err)
			}
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
			firlog.Debug("mcp tool call", "server", serverName, "tool", toolName)
			result, err := session.CallTool(ctx, callParams)
			if err != nil {
				firlog.Warn("mcp tool call failed", "server", serverName, "tool", toolName, "err", err)
				return agent.AgentToolResult{}, err
			}
			return convertResult(result), nil
		},
	}
}

// convertResult maps an *sdk.CallToolResult to an agent.AgentToolResult.
// Text content is mapped directly; image content is base64-encoded; audio
// content, resource links, and embedded resources are converted to text.
// Unknown content types are represented as a plain text placeholder.
func convertResult(result *sdk.CallToolResult) agent.AgentToolResult {
	content := make([]ai.ToolResultContent, 0, len(result.Content))
	var (
		fullOutputPath    string
		spillFile         *os.File
		spillBytesWritten int
	)
	defer func() {
		if spillFile != nil {
			spillFile.Close()
		}
	}()
	// capText applies tail truncation to text returned by an MCP server. When
	// truncation occurs, the full original text of every truncated block is
	// appended to a single per-result temp file (separated by a marker), and
	// a footer pointing the agent at the file is appended to the truncated
	// text so it can Read it explicitly.
	capText := func(text string) string {
		tr := tools.TruncateTail(text, tools.TruncationOptions{})
		if !tr.Truncated {
			return text
		}
		if spillFile == nil {
			if f, err := os.CreateTemp("", "fir-mcp-*.txt"); err == nil {
				spillFile = f
				fullOutputPath = f.Name()
			} else {
				firlog.Warn("mcp truncate temp file failed", "err", err)
			}
		}
		if spillFile != nil {
			// Separate multi-block spills with a blank line so the file stays
			// readable when more than one content item is truncated.
			if fullOutputPath != "" && spillBytesWritten > 0 {
				if _, werr := spillFile.WriteString("\n"); werr != nil {
					firlog.Warn("mcp truncate spill failed", "err", werr)
				}
			}
			n, werr := spillFile.WriteString(text)
			if werr != nil {
				firlog.Warn("mcp truncate spill failed", "err", werr)
			}
			spillBytesWritten += n
		}
		var footer string
		if fullOutputPath != "" {
			footer = fmt.Sprintf("\n\n[Output truncated (%d lines, %s). Full output written to %s — use Read to view.]",
				tr.TotalLines, tools.FormatSize(tr.TotalBytes), fullOutputPath)
		} else {
			footer = fmt.Sprintf("\n\n[Output truncated (%d lines, %s).]",
				tr.TotalLines, tools.FormatSize(tr.TotalBytes))
		}
		return tr.Content + footer
	}
	for _, c := range result.Content {
		switch v := c.(type) {
		case *sdk.TextContent:
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: capText(v.Text),
			})
		case *sdk.ImageContent:
			// MCP's ImageContent is spec'd to carry image data, but guard on
			// the MIME type so unexpected values don't cause API errors on
			// providers that validate image types strictly.
			if strings.HasPrefix(v.MIMEType, "image/") {
				content = append(content, ai.ToolResultContent{
					Type:     ai.ContentTypeImage,
					Data:     base64.StdEncoding.EncodeToString(v.Data),
					MimeType: v.MIMEType,
				})
			} else {
				mime := v.MIMEType
				if mime == "" {
					mime = "application/octet-stream"
				}
				content = append(content, ai.ToolResultContent{
					Type: ai.ContentTypeText,
					Text: fmt.Sprintf("[base64 %s] %s", mime, base64.StdEncoding.EncodeToString(v.Data)),
				})
			}
		case *sdk.AudioContent:
			// Render audio as base64 text since most LLM providers don't support
			// audio tool-result content directly.
			mime := v.MIMEType
			if mime == "" {
				mime = "audio/mpeg"
			}
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: fmt.Sprintf("[audio/%s] %s", mime, base64.StdEncoding.EncodeToString(v.Data)),
			})
		case *sdk.ResourceLink:
			// Render a resource link as a plain-text URI reference.
			label := v.Name
			if label == "" {
				label = v.URI
			}
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: fmt.Sprintf("[resource] %s <%s>", label, v.URI),
			})
		case *sdk.EmbeddedResource:
			// Prefer text; fall back to base64 for binary blobs.
			if v.Resource == nil {
				content = append(content, ai.ToolResultContent{
					Type: ai.ContentTypeText,
					Text: "[embedded resource: no content]",
				})
			} else if v.Resource.Text != "" {
				content = append(content, ai.ToolResultContent{
					Type: ai.ContentTypeText,
					Text: capText(v.Resource.Text),
				})
			} else {
				mime := v.Resource.MIMEType
				if mime == "" {
					mime = "application/octet-stream"
				}
				content = append(content, ai.ToolResultContent{
					Type: ai.ContentTypeText,
					Text: fmt.Sprintf("[base64 %s] %s", mime, base64.StdEncoding.EncodeToString(v.Resource.Blob)),
				})
			}
		default:
			// Audio, resource links, etc. — render as a text placeholder.
			content = append(content, ai.ToolResultContent{
				Type: ai.ContentTypeText,
				Text: "[unsupported MCP content type]",
			})
		}
	}
	res := agent.AgentToolResult{
		Content: content,
		IsError: result.IsError,
	}
	if fullOutputPath != "" {
		res.Details = map[string]any{"fullOutputPath": fullOutputPath}
	}
	return res
}
