package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// mockBatchProvider implements BatchToolProvider for testing.
type mockBatchProvider struct {
	tools        *agent.ToolSet
	promptCalls  int
	lastMessages []agent.AgentMessage
	promptResult string
	promptErr    error
}

func (m *mockBatchProvider) GetTools() *agent.ToolSet {
	return m.tools
}

func (m *mockBatchProvider) SimplePrompt(_ context.Context, messages []agent.AgentMessage) (string, error) {
	m.promptCalls++
	m.lastMessages = messages
	return m.promptResult, m.promptErr
}

// newMockTool creates a simple tool that returns its name and params as text.
func newMockTool(name string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        name,
			Description: "mock " + name,
		},
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			text := fmt.Sprintf("output from %s", name)
			if p, ok := params["path"]; ok {
				text += fmt.Sprintf(" path=%v", p)
			}
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: text}},
			}, nil
		},
	}
}

// newErrorTool creates a tool that returns an error result.
func newErrorTool(name string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        name,
			Description: "error " + name,
		},
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: "something went wrong"}},
				IsError: true,
			}, nil
		},
	}
}

func TestBatchTool_Basic(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))
	ts.Add(newMockTool("Bash"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "Here is a summary of the files.",
	}
	tool := NewBatchTool(mock)

	if tool.Name != "batch" {
		t.Fatalf("name = %q, want batch", tool.Name)
	}

	result, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"tools": []any{
			map[string]any{"name": "Read", "params": map[string]any{"path": "foo.go"}},
			map[string]any{"name": "Bash", "params": map[string]any{"command": "ls"}},
		},
		"instructions": "Summarise the outputs.",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content[0].Text)
	}
	if mock.promptCalls != 1 {
		t.Fatalf("promptCalls = %d, want 1", mock.promptCalls)
	}
	if result.Content[0].Text != "Here is a summary of the files." {
		t.Errorf("text = %q", result.Content[0].Text)
	}

	// Verify the synthesis prompt includes both tool outputs.
	if len(mock.lastMessages) != 1 {
		t.Fatalf("messages = %d, want 1", len(mock.lastMessages))
	}
	userMsg := mock.lastMessages[0].AsUser()
	if userMsg == nil {
		t.Fatal("expected user message")
	}
	content, _ := userMsg.Content.(string)
	if !strings.Contains(content, "output from Read path=foo.go") {
		t.Errorf("prompt missing Read output: %s", content)
	}
	if !strings.Contains(content, "output from Bash") {
		t.Errorf("prompt missing Bash output: %s", content)
	}
	if !strings.Contains(content, "Summarise the outputs.") {
		t.Errorf("prompt missing instructions: %s", content)
	}
}

func TestBatchTool_EmptyTools(t *testing.T) {
	mock := &mockBatchProvider{
		tools:        agent.NewToolSet(),
		promptResult: "ok",
	}
	tool := NewBatchTool(mock)

	result, err := tool.Execute(context.Background(), "tc2", map[string]any{
		"tools":        []any{},
		"instructions": "do something",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for empty tools")
	}
	if mock.promptCalls != 0 {
		t.Error("should not call SimplePrompt with empty tools")
	}
}

func TestBatchTool_MissingInstructions(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "ok",
	}
	tool := NewBatchTool(mock)

	result, err := tool.Execute(context.Background(), "tc3", map[string]any{
		"tools": []any{
			map[string]any{"name": "Read"},
		},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for missing instructions")
	}
	if mock.promptCalls != 0 {
		t.Error("should not call SimplePrompt without instructions")
	}
}

func TestBatchTool_ToolNotFound(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "handled missing tool gracefully",
	}
	tool := NewBatchTool(mock)

	result, err := tool.Execute(context.Background(), "tc4", map[string]any{
		"tools": []any{
			map[string]any{"name": "Read"},
			map[string]any{"name": "NonExistent"},
		},
		"instructions": "Summarise.",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	// Should still succeed — the error is included in the synthesis.
	if result.IsError {
		t.Error("unexpected error — missing tool should be reported in synthesis")
	}
	if mock.promptCalls != 1 {
		t.Fatalf("promptCalls = %d, want 1", mock.promptCalls)
	}

	// The synthesis prompt should mention the error.
	content, _ := mock.lastMessages[0].AsUser().Content.(string)
	if !strings.Contains(content, "not found") {
		t.Errorf("prompt should mention tool not found: %s", content)
	}
	if !strings.Contains(content, "[ERROR]") {
		t.Errorf("prompt should mark error: %s", content)
	}
}

func TestBatchTool_ErrorTool(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))
	ts.Add(newErrorTool("FailTool"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "one succeeded, one failed",
	}
	tool := NewBatchTool(mock)

	result, err := tool.Execute(context.Background(), "tc5", map[string]any{
		"tools": []any{
			map[string]any{"name": "Read"},
			map[string]any{"name": "FailTool"},
		},
		"instructions": "Report status.",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Error("batch should succeed even if sub-tools error")
	}
	content, _ := mock.lastMessages[0].AsUser().Content.(string)
	if !strings.Contains(content, "[ERROR]") {
		t.Errorf("prompt should mark FailTool as error: %s", content)
	}
}

func TestBatchTool_SelfCallPrevented(t *testing.T) {
	ts := agent.NewToolSet()
	// Add a mock "batch" tool to the tool set so it's findable.
	ts.Add(newMockTool("batch"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "should include error for self-call",
	}
	tool := NewBatchTool(mock)

	result, err := tool.Execute(context.Background(), "tc6", map[string]any{
		"tools": []any{
			map[string]any{"name": "batch"},
		},
		"instructions": "Summarise.",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	// Should still synthesise — the recursion prevention is reported as an error in the output.
	if result.IsError {
		t.Error("batch should succeed, reporting self-call as error in synthesis")
	}
	content, _ := mock.lastMessages[0].AsUser().Content.(string)
	if !strings.Contains(content, "cannot call itself") {
		t.Errorf("prompt should mention self-call prevention: %s", content)
	}
}

func TestBatchTool_PromptError(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))

	mock := &mockBatchProvider{
		tools:     ts,
		promptErr: fmt.Errorf("model overloaded"),
	}
	tool := NewBatchTool(mock)

	result, err := tool.Execute(context.Background(), "tc7", map[string]any{
		"tools": []any{
			map[string]any{"name": "Read"},
		},
		"instructions": "Summarise.",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error when SimplePrompt fails")
	}
	if !strings.Contains(result.Content[0].Text, "synthesis failed") {
		t.Errorf("text = %q", result.Content[0].Text)
	}
}

func TestBatchTool_CancelledContext(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "should not reach here",
	}
	tool := NewBatchTool(mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	result, err := tool.Execute(ctx, "tc8", map[string]any{
		"tools": []any{
			map[string]any{"name": "Read"},
		},
		"instructions": "Summarise.",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for cancelled context")
	}
	if !strings.Contains(result.Content[0].Text, "cancelled") {
		t.Errorf("text = %q", result.Content[0].Text)
	}
	if mock.promptCalls != 0 {
		t.Error("should not call SimplePrompt when cancelled")
	}
}

func TestBatchTool_ProgressUpdates(t *testing.T) {
	ts := agent.NewToolSet()
	ts.Add(newMockTool("Read"))
	ts.Add(newMockTool("Bash"))

	mock := &mockBatchProvider{
		tools:        ts,
		promptResult: "done",
	}
	tool := NewBatchTool(mock)

	var updates []string
	onUpdate := func(partial agent.AgentToolResult) {
		if len(partial.Content) > 0 {
			updates = append(updates, partial.Content[0].Text)
		}
	}

	_, err := tool.Execute(context.Background(), "tc9", map[string]any{
		"description": "reading files",
		"tools": []any{
			map[string]any{"name": "Read"},
			map[string]any{"name": "Bash"},
		},
		"instructions": "Summarise.",
	}, onUpdate)

	if err != nil {
		t.Fatal(err)
	}

	// Expect: 2 tool execution updates + 1 synthesis update = 3
	if len(updates) != 3 {
		t.Fatalf("updates = %d, want 3; got: %v", len(updates), updates)
	}
	if !strings.Contains(updates[0], "1/2") || !strings.Contains(updates[0], "Read") {
		t.Errorf("update[0] = %q", updates[0])
	}
	if !strings.Contains(updates[1], "2/2") || !strings.Contains(updates[1], "Bash") {
		t.Errorf("update[1] = %q", updates[1])
	}
	if !strings.Contains(updates[2], "synthesising") {
		t.Errorf("update[2] = %q", updates[2])
	}
	// All should include the description.
	for i, u := range updates {
		if !strings.Contains(u, "reading files") {
			t.Errorf("update[%d] missing description: %q", i, u)
		}
	}
}

func TestBatchTool_InvalidParams(t *testing.T) {
	mock := &mockBatchProvider{
		tools:        agent.NewToolSet(),
		promptResult: "ok",
	}
	tool := NewBatchTool(mock)

	tests := []struct {
		name   string
		params map[string]any
		errStr string
	}{
		{
			name:   "tools not array",
			params: map[string]any{"tools": "not-array", "instructions": "do it"},
			errStr: "must be an array",
		},
		{
			name:   "tools missing",
			params: map[string]any{"instructions": "do it"},
			errStr: "tools is required",
		},
		{
			name: "tool entry not object",
			params: map[string]any{
				"tools":        []any{"not-an-object"},
				"instructions": "do it",
			},
			errStr: "must be an object",
		},
		{
			name: "tool entry missing name",
			params: map[string]any{
				"tools":        []any{map[string]any{"params": map[string]any{}}},
				"instructions": "do it",
			},
			errStr: "name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), "tc", tt.params, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError {
				t.Error("expected error result")
			}
			if !strings.Contains(result.Content[0].Text, tt.errStr) {
				t.Errorf("text = %q, want containing %q", result.Content[0].Text, tt.errStr)
			}
		})
	}
}

func TestParseBatchToolCalls(t *testing.T) {
	calls, err := parseBatchToolCalls(map[string]any{
		"tools": []any{
			map[string]any{"name": "Read", "params": map[string]any{"path": "a.go"}},
			map[string]any{"name": "Bash"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("len = %d", len(calls))
	}
	if calls[0].Name != "Read" || calls[0].Params["path"] != "a.go" {
		t.Errorf("call[0] = %+v", calls[0])
	}
	if calls[1].Name != "Bash" || calls[1].Params != nil {
		t.Errorf("call[1] = %+v", calls[1])
	}
}

func TestBuildSynthesisPrompt(t *testing.T) {
	results := []batchToolResult{
		{Name: "Read", Index: 0, Output: "file content here"},
		{Name: "Bash", Index: 1, Output: "command failed", IsError: true},
	}

	prompt := buildSynthesisPrompt(results, "Tell me what happened.")

	if !strings.Contains(prompt, "Tool 1: Read") {
		t.Error("missing Read header")
	}
	if !strings.Contains(prompt, "Tool 2: Bash [ERROR]") {
		t.Error("missing Bash error header")
	}
	if !strings.Contains(prompt, "file content here") {
		t.Error("missing Read output")
	}
	if !strings.Contains(prompt, "command failed") {
		t.Error("missing Bash output")
	}
	if !strings.Contains(prompt, "Tell me what happened.") {
		t.Error("missing instructions")
	}
}
