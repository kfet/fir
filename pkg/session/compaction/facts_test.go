package compaction

import (
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

func TestExtractFacts_CommandsAndErrors(t *testing.T) {
	asst := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "bash", map[string]any{"command": "go test ./..."}),
		},
	}))
	tr := agent.NewAgentMessage(ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: "1",
		ToolName:   "bash",
		Content: []ai.ToolResultContent{
			{Type: "text", Text: "make: *** [test] Error 1\nfoo.go:3:5: undefined: bar\nok"},
		},
	}))

	f := extractFacts([]agent.AgentMessage{asst, tr}, 20)
	if len(f.Commands) != 1 || f.Commands[0] != "go test ./..." {
		t.Errorf("commands = %v", f.Commands)
	}
	if len(f.Errors) < 2 {
		t.Errorf("expected ≥2 error lines, got %v", f.Errors)
	}

	out := FormatFacts(f)
	if !strings.Contains(out, "## Facts (verbatim)") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "go test ./...") {
		t.Errorf("missing command: %q", out)
	}
	if !strings.Contains(out, "Error 1") {
		t.Errorf("missing error line: %q", out)
	}
}

func TestExtractFacts_Empty(t *testing.T) {
	if FormatFacts(Facts{}) != "" {
		t.Error("expected empty output for empty facts")
	}
}

func TestExtractFacts_Dedup(t *testing.T) {
	mk := func(cmd string) agent.AgentMessage {
		return agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("1", "bash", map[string]any{"command": cmd}),
			},
		}))
	}
	f := extractFacts([]agent.AgentMessage{mk("ls"), mk("ls"), mk("pwd")}, 20)
	if len(f.Commands) != 2 {
		t.Errorf("expected dedup to 2 commands, got %v", f.Commands)
	}
}
