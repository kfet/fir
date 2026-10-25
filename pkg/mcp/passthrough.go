package mcp

// Generic MCP data passthrough. fir does not interpret any of this data; it
// records the raw MCP fields a client UI might need (tool definition _meta,
// result _meta, structuredContent, resource content blocks) so front ends —
// e.g. the ACP mode — can forward them untouched. This is what lets an ACP
// client implement MCP Apps without fir doing any UI work.

import (
	"context"
	"encoding/json"

	"github.com/kfet/agent"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DetailsKey is the key under AgentToolResult.Details (a map) that holds an
// MCP tool result's passthrough data. Its value is a map with:
//
//	server            MCP server name
//	tool              original (unsanitised) MCP tool name
//	_meta             the CallToolResult _meta, when present
//	structuredContent the CallToolResult structuredContent, when present
//	resources         raw MCP "resource" / "resource_link" content blocks
//	flattened         indexes of their text renderings in the result content
const DetailsKey = "mcp"

// ToolOrigin identifies the MCP tool behind an adapted agent tool.
type ToolOrigin struct {
	Server string         // MCP server name
	Name   string         // original MCP tool name
	Meta   map[string]any // tool definition _meta (e.g. ui.resourceUri); may be nil
}

// adaptTool adapts tool and records its ToolOrigin under the adapted name.
func (m *Manager) adaptTool(serverName string, tool *sdk.Tool) agent.AgentTool {
	at := AdaptTool(m.serverSession(serverName), serverName, tool, &m.progressReg)
	m.toolInfo.Store(at.Name, ToolOrigin{Server: serverName, Name: tool.Name, Meta: map[string]any(tool.Meta)})
	return at
}

// ToolOrigin returns the MCP origin of the adapted tool named name
// ("mcp__<server>__<tool>"). ok is false for non-MCP tools.
func (m *Manager) ToolOrigin(name string) (ToolOrigin, bool) {
	if m == nil {
		return ToolOrigin{}, false
	}
	v, ok := m.toolInfo.Load(name)
	if !ok {
		return ToolOrigin{}, false
	}
	return v.(ToolOrigin), true
}

// ReadResource reads uri from the named server's live session, waiting for
// a reconnect like CallTool does.
func (m *Manager) ReadResource(ctx context.Context, serverName, uri string) (*sdk.ReadResourceResult, error) {
	session, err := m.ensureConnected(ctx, serverName)
	if err != nil {
		return nil, err
	}
	return session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
}

// attachPassthrough stores the passthrough fields of result in res.Details
// under DetailsKey, keeping any details convertResult already set.
func attachPassthrough(res *agent.AgentToolResult, serverName, toolName string, result *sdk.CallToolResult) {
	pt := map[string]any{"server": serverName, "tool": toolName}
	if len(result.Meta) > 0 {
		pt["_meta"] = map[string]any(result.Meta)
	}
	if result.StructuredContent != nil {
		pt["structuredContent"] = result.StructuredContent
	}
	var (
		resources []any
		flattened []int
	)
	for i, c := range result.Content {
		switch c.(type) {
		case *sdk.ResourceLink, *sdk.EmbeddedResource:
			if raw := rawBlock(c); raw != nil {
				resources = append(resources, raw)
				// convertResult emits one content entry per MCP block, so i
				// is also the index of this block's text rendering.
				flattened = append(flattened, i)
			}
		}
	}
	if len(resources) > 0 {
		pt["resources"] = resources
		pt["flattened"] = flattened
	}
	details, _ := res.Details.(map[string]any)
	if details == nil {
		details = map[string]any{}
	}
	details[DetailsKey] = pt
	res.Details = details
}

// rawBlock returns the MCP wire form of a content block as a generic map.
func rawBlock(c sdk.Content) map[string]any {
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// ToolName returns the agent tool name fir registers for MCP tool toolName
// on serverName ("mcp__<server>__<tool>", sanitised).
func ToolName(serverName, toolName string) string {
	return sanitizeToolName("mcp__" + serverName + "__" + toolName)
}
