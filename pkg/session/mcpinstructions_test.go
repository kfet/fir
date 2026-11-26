package session

import (
	"strings"
	"testing"

	"github.com/kfet/agent"

	"github.com/kfet/fir/pkg/mcp"
)

func TestMCPInstructions_MergeRenderAndPrompt(t *testing.T) {
	s, _ := newTestAgentSession(t)
	if got := s.mcpInstructionsSection(); got != "" {
		t.Fatalf("no servers: section = %q, want empty", got)
	}
	long := strings.Repeat("x", mcpInstructionsMaxChars+10)
	s.mcpSetInstructions(map[string][]agent.AgentTool{"b": {}, "a": {}, "c": {}, "d": {}},
		map[string]string{"a": "same", "b": "same", "c": long, "d": "  "})
	sec := s.mcpInstructionsSection()
	if !strings.Contains(sec, "## a, b\nsame") || strings.Count(sec, "same") != 1 {
		t.Errorf("dedupe failed:\n%s", sec)
	}
	if strings.Contains(sec, "## d") || strings.Contains(sec, long) || !strings.Contains(sec, long[:mcpInstructionsMaxChars]) {
		t.Errorf("empty or truncation wrong")
	}
	if !strings.Contains(s.Agent.State().SystemPrompt, "## a, b\nsame") {
		t.Errorf("system prompt not rebuilt")
	}

	// a offline keeps its saved text; c is no longer configured.
	s.mcpSetInstructions(map[string][]agent.AgentTool{"a": nil, "b": {}}, map[string]string{"b": "new"})
	sec = s.mcpInstructionsSection()
	if !strings.Contains(sec, "## a\nsame") || !strings.Contains(sec, "## b\nnew") || strings.Contains(sec, "xxx") {
		t.Errorf("merge wrong:\n%s", sec)
	}

	s.mcpSetInstructions(map[string][]agent.AgentTool{}, nil)
	if s.mcpInstructionsSection() != "" || strings.Contains(s.Agent.State().SystemPrompt, "MCP server instructions") {
		t.Errorf("section not removed")
	}
}

// MCP servers set instructions from their own goroutines while the session
// reads its prompt; run with -race.
func TestMCPInstructions_ConcurrentUpdates(t *testing.T) {
	s, _ := newTestAgentSession(t)
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				s.mcpSetInstructions(map[string][]agent.AgentTool{"a": {}},
					map[string]string{"a": strings.Repeat("y", i+j+1)})
				_ = s.GetSystemPrompt()
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	want := s.composeSystemPrompt()
	if got := s.Agent.State().SystemPrompt; got != want || s.GetSystemPrompt() == "" {
		t.Errorf("applied prompt differs from the composed one")
	}
}

func TestMCPSkills_MergeAndPrompt(t *testing.T) {
	s, _ := newTestAgentSession(t)
	sk := mcp.MCPSkill{Name: "srv:git", Description: "Git rules", URI: "skill://git/SKILL.md", Tool: "mcp__srv__read_skill"}
	s.mcpSetSkills(map[string][]agent.AgentTool{"srv": {}, "other": {}}, map[string][]mcp.MCPSkill{"srv": {sk}, "other": nil})
	if !strings.Contains(s.Agent.State().SystemPrompt, "srv:git — Git rules") {
		t.Fatalf("skill not in prompt:\n%s", s.mcpSkillsSection())
	}
	// Offline keeps the saved list.
	s.mcpSetSkills(map[string][]agent.AgentTool{"srv": {}}, map[string][]mcp.MCPSkill{})
	if !strings.Contains(s.mcpSkillsSection(), "srv:git") {
		t.Errorf("saved list dropped while offline")
	}
	// Connected with no skills clears it.
	s.mcpSetSkills(map[string][]agent.AgentTool{"srv": {}}, map[string][]mcp.MCPSkill{"srv": nil})
	if s.mcpSkillsSection() != "" || strings.Contains(s.Agent.State().SystemPrompt, "MCP server skills") {
		t.Errorf("section not removed")
	}
}
