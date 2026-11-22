package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// mcpAdvertised returns the MCP tools to advertise, given every configured
// server mapped to its live tools (nil when not connected). A connected
// server contributes its live tools, and its schemas are saved when they
// differ from the saved ones. A server that is not connected contributes
// its saved schemas; calling one returns a tool error until it connects.
// Saved entries for servers no longer configured are dropped. Servers are
// visited in name order so the result is deterministic.
func (s *AgentSession) mcpAdvertised(live map[string][]agent.AgentTool) []agent.AgentTool {
	names := make([]string, 0, len(live))
	for name := range live {
		names = append(names, name)
	}
	sort.Strings(names)

	// Read the saved schemas and write the new ones under one lock: a
	// concurrent state update between the read and the write would
	// otherwise be lost.
	var out []agent.AgentTool
	changed := false
	s.updateStateNoSave(func(st *SessionState) {
		saved := st.Runtime.McpTools
		next := make(map[string][]ai.Tool, len(live))
		for _, name := range names {
			tools := live[name]
			if tools == nil {
				if schemas, ok := saved[name]; ok {
					next[name] = schemas
					for _, t := range schemas {
						out = append(out, offlineMCPTool(name, t))
					}
				}
				continue
			}
			schemas := make([]ai.Tool, len(tools))
			for i, t := range tools {
				schemas[i] = t.Tool
			}
			next[name] = schemas
			out = append(out, tools...)
		}
		if sameMCPTools(saved, next) {
			return
		}
		if len(next) == 0 {
			next = nil
		}
		st.Runtime.McpTools = next
		changed = true
	})
	if changed {
		s.SaveState()
	}
	return out
}

// sameMCPTools compares two saved tool maps by their JSON form.
func sameMCPTools(a, b map[string][]ai.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true // nil and empty are the same saved state
	}
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// offlineMCPTool advertises a saved MCP tool schema whose server is not
// connected. Calling it returns a normal tool error.
func offlineMCPTool(server string, t ai.Tool) agent.AgentTool {
	return agent.AgentTool{
		Tool:  t,
		Label: t.Name + " (via " + server + ")",
		Execute: func(context.Context, string, map[string]any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			return agent.AgentToolResult{}, fmt.Errorf("MCP server %q is not connected; try again later", server)
		},
	}
}
