package acp

// MCP data passthrough (fir ACP extension "dev.fir.mcp/v1").
//
// fir forwards raw MCP data to ACP clients without interpreting it, so a
// client can implement MCP Apps UI on its own:
//
//   - tool_call / tool_call_update for MCP tools carry
//     _meta["dev.fir.mcp/v1"] = {server, tool, toolMeta, resultMeta,
//     structuredContent} (fields present only when known);
//   - MCP "resource" / "resource_link" result blocks are forwarded as ACP
//     resource content blocks instead of being flattened to text;
//   - two extension methods reuse the session's live MCP connections:
//     _fir/mcp/resources/read and _fir/mcp/tools/call.
//
// See docs/acp-mcp-passthrough.md.

import (
	"context"
	"encoding/json"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/mcp"
)

const (
	mcpExtKey             = "dev.fir.mcp/v1"
	mcpMethodReadResource = "_fir/mcp/resources/read"
	mcpMethodCallTool     = "_fir/mcp/tools/call"
)

// mcpCapability is advertised under agentCapabilities._meta[mcpExtKey].
func mcpCapability() map[string]any {
	return map[string]any{
		"toolCallMeta":   true,
		"resourceBlocks": true,
		"methods":        []string{mcpMethodReadResource, mcpMethodCallTool},
	}
}

// mcpToolStartMeta returns the tool_call _meta for an MCP tool, or nil.
func mcpToolStartMeta(entry *firSession, toolName string) map[string]any {
	if entry == nil {
		return nil
	}
	o, ok := entry.mcpManager.ToolOrigin(toolName)
	if !ok {
		return nil
	}
	v := map[string]any{"server": o.Server, "tool": o.Name}
	if len(o.Meta) > 0 {
		v["toolMeta"] = o.Meta
	}
	return map[string]any{mcpExtKey: v}
}

// mcpDetails returns the passthrough map an MCP tool stored in its result
// details (see mcp.DetailsKey), or nil.
func mcpDetails(result any) map[string]any {
	m, _ := result.(map[string]any)
	details, _ := m["details"].(map[string]any)
	pt, _ := details[mcp.DetailsKey].(map[string]any)
	return pt
}

// mcpToolEndMeta returns the tool_call_update _meta for an MCP tool result
// (normalised result map), or nil for non-MCP tools.
func mcpToolEndMeta(entry *firSession, toolName string, result any) map[string]any {
	meta := mcpToolStartMeta(entry, toolName)
	pt := mcpDetails(result)
	if pt == nil {
		return meta
	}
	var v map[string]any
	if meta != nil {
		v = meta[mcpExtKey].(map[string]any)
	} else {
		v = map[string]any{}
		meta = map[string]any{mcpExtKey: v}
	}
	for _, k := range []string{"server", "tool"} {
		if s, ok := pt[k].(string); ok && s != "" {
			v[k] = s
		}
	}
	if rm, ok := pt["_meta"]; ok {
		v["resultMeta"] = rm
	}
	if sc, ok := pt["structuredContent"]; ok {
		v["structuredContent"] = sc
	}
	return meta
}

// mcpResourceContent converts the raw MCP resource blocks of an MCP tool
// result into ACP resource content blocks. MCP and ACP share the wire shape
// of "resource" and "resource_link", so blocks are decoded as-is.
func mcpResourceContent(result any) []acpsdk.ToolCallContent {
	raw, _ := mcpDetails(result)["resources"].([]any)
	var out []acpsdk.ToolCallContent
	for _, r := range raw {
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		var cb acpsdk.ContentBlock
		if json.Unmarshal(b, &cb) != nil || (cb.Resource == nil && cb.ResourceLink == nil) {
			continue
		}
		out = append(out, acpsdk.ToolCallContent{
			Content: &acpsdk.ToolCallContentContent{Type: "content", Content: cb},
		})
	}
	return out
}

// mcpDisplayResult returns result without the text renderings of the MCP
// resource blocks that mcpResourceContent forwards, so the client does not
// get the same resource twice. rawOutput keeps the full result.
func mcpDisplayResult(result any) any {
	pt := mcpDetails(result)
	idx := map[int]bool{}
	switch v := pt["flattened"].(type) {
	case []int:
		for _, i := range v {
			idx[i] = true
		}
	case []any: // after a JSON round trip (session/load)
		for _, i := range v {
			if f, ok := i.(float64); ok {
				idx[int(f)] = true
			}
		}
	}
	m, _ := result.(map[string]any)
	content, _ := m["content"].([]any)
	if len(idx) == 0 || len(content) == 0 {
		return result
	}
	kept := make([]any, 0, len(content))
	for i, c := range content {
		if !idx[i] {
			kept = append(kept, c)
		}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	out["content"] = kept
	return out
}

// normalizeToolResult converts a live agent.AgentToolResult into the map
// shape replayed results use ({content, isError, details}), so display and
// passthrough code sees one form. Other values are returned unchanged.
func normalizeToolResult(result any) any {
	var r agent.AgentToolResult
	switch v := result.(type) {
	case agent.AgentToolResult:
		r = v
	case *agent.AgentToolResult:
		if v == nil {
			return result
		}
		r = *v
	default:
		return result
	}
	m := map[string]any{
		"content": toolResultToContent(r.Content),
		"isError": r.IsError,
	}
	if r.Details != nil {
		m["details"] = r.Details
	}
	return m
}

func withStartMeta(meta map[string]any) acpsdk.ToolCallStartOpt {
	return func(tc *acpsdk.SessionUpdateToolCall) { tc.Meta = meta }
}

func withUpdateMeta(meta map[string]any) acpsdk.ToolCallUpdateOpt {
	return func(tu *acpsdk.SessionToolCallUpdate) { tu.Meta = meta }
}

type mcpReadResourceRequest struct {
	SessionId string `json:"sessionId"`
	Server    string `json:"server"`
	Uri       string `json:"uri"`
}

type mcpCallToolRequest struct {
	SessionId string         `json:"sessionId"`
	Server    string         `json:"server"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// mcpSessionTool resolves the session and checks that the agent itself may
// call toolName: it must be in the session's active tool set. That is the
// permission the client inherits — nothing the model cannot reach.
func (pa *firAgent) mcpSessionTool(sessionID, toolName string) (*firSession, *acpsdk.RequestError) {
	pa.mu.Lock()
	entry, ok := pa.sessions[sessionID]
	pa.mu.Unlock()
	if !ok {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": "unknown sessionId"})
	}
	if entry.mcpManager == nil || entry.session == nil || entry.session.Agent == nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": "no MCP servers in this session"})
	}
	ts := entry.session.Agent.State().Tools
	if ts == nil {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": "tool not available: " + toolName})
	}
	if _, ok := ts.Get(toolName); !ok {
		return nil, acpsdk.NewInvalidParams(map[string]any{"error": "tool not available: " + toolName})
	}
	return entry, nil
}

// handleMCPMethod serves the _fir/mcp/* extension methods. It returns the
// raw MCP result object unchanged.
func (pa *firAgent) handleMCPMethod(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	var (
		res any
		err error
	)
	switch method {
	case mcpMethodReadResource:
		var p mcpReadResourceRequest
		if e := json.Unmarshal(params, &p); e != nil || p.Server == "" || p.Uri == "" {
			return nil, acpsdk.NewInvalidParams(map[string]any{"error": "sessionId, server and uri are required"})
		}
		// Reading resources is gated on the server's read_resource tool.
		entry, rerr := pa.mcpSessionTool(p.SessionId, mcp.ToolName(p.Server, "read_resource"))
		if rerr != nil {
			return nil, rerr
		}
		res, err = entry.mcpManager.ReadResource(ctx, p.Server, p.Uri)
	case mcpMethodCallTool:
		var p mcpCallToolRequest
		if e := json.Unmarshal(params, &p); e != nil || p.Server == "" || p.Name == "" {
			return nil, acpsdk.NewInvalidParams(map[string]any{"error": "sessionId, server and name are required"})
		}
		entry, rerr := pa.mcpSessionTool(p.SessionId, mcp.ToolName(p.Server, p.Name))
		if rerr != nil {
			return nil, rerr
		}
		res, err = entry.mcpManager.CallTool(ctx, p.Server, p.Name, p.Arguments)
	default:
		return nil, acpsdk.NewMethodNotFound(method)
	}
	if err != nil {
		return nil, toReqErr(err)
	}
	return res, nil
}
