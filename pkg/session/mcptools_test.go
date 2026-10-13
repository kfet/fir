package session

import (
	"context"
	"reflect"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

func mcpTool(name, desc string) agent.AgentTool {
	return agent.AgentTool{Tool: ai.Tool{Name: name, Description: desc,
		Parameters: map[string]any{"type": "object"}}}
}

func toolNames(ts []agent.AgentTool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

// A server that is not connected is advertised from its saved schemas, and
// calling one of them is a normal tool error.
func TestMCPAdvertised_OfflineServerUsesSavedTools(t *testing.T) {
	s, _ := newTestAgentSession(t)
	got := s.mcpAdvertised(map[string][]agent.AgentTool{
		"b": {mcpTool("mcp__b__x", "x")},
		"a": {mcpTool("mcp__a__y", "y")},
	})
	if want := []string{"mcp__a__y", "mcp__b__x"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Fatalf("live tools = %v, want %v", toolNames(got), want)
	}

	got = s.mcpAdvertised(map[string][]agent.AgentTool{"a": nil, "b": {mcpTool("mcp__b__x", "x")}})
	if want := []string{"mcp__a__y", "mcp__b__x"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Fatalf("with a offline = %v, want %v", toolNames(got), want)
	}
	if got[0].Description != "y" {
		t.Errorf("saved schema lost: %+v", got[0].Tool)
	}
	if _, err := got[0].Execute(context.Background(), "id", nil, nil); err == nil {
		t.Error("offline tool call succeeded, want error")
	}
}

// A server reconnecting with a changed list replaces its saved schemas, and
// a server no longer configured is forgotten.
func TestMCPAdvertised_UpdatesSavedList(t *testing.T) {
	s, _ := newTestAgentSession(t)
	s.mcpAdvertised(map[string][]agent.AgentTool{"a": {mcpTool("mcp__a__y", "y")}, "b": {mcpTool("mcp__b__x", "x")}})
	s.mcpAdvertised(map[string][]agent.AgentTool{"a": {mcpTool("mcp__a__z", "z")}})

	saved := s.SessionState().Runtime.McpTools
	if len(saved) != 1 || len(saved["a"]) != 1 || saved["a"][0].Name != "mcp__a__z" {
		t.Fatalf("saved = %+v", saved)
	}
	if got := toolNames(s.mcpAdvertised(map[string][]agent.AgentTool{"a": nil})); !reflect.DeepEqual(got, []string{"mcp__a__z"}) {
		t.Errorf("offline a = %v", got)
	}
}
