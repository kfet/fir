package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

func toolCallResponse(toolName, toolID string, args map[string]any) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			ai.NewToolCallContent(toolID, toolName, args),
		},
		Api:        ai.ApiAnthropicMessages,
		Provider:   ai.ProviderAnthropic,
		Model:      "test-model",
		StopReason: ai.StopReasonToolUse,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func testConvertToLLM(messages []AgentMessage) ([]ai.Message, error) {
	var result []ai.Message
	for _, m := range messages {
		result = append(result, m.Message)
	}
	return result, nil
}

func collectEvents(events <-chan AgentEvent) []AgentEvent {
	var result []AgentEvent
	for e := range events {
		result = append(result, e)
	}
	return result
}

func TestAgentLoop_SingleTurn(t *testing.T) {
	events := make(chan AgentEvent, 100)

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Hello!", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		SystemPrompt: "You are helpful.",
		Messages:     []AgentMessage{},
		Tools:        nil,
	}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, mockStreamFn(simpleResponse("Hi there!")), events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Check event sequence
	if len(allEvents) < 5 {
		t.Fatalf("expected at least 5 events, got %d", len(allEvents))
	}
	if allEvents[0].Type != EventAgentStart {
		t.Errorf("event[0] = %s, want agent_start", allEvents[0].Type)
	}
	if allEvents[1].Type != EventTurnStart {
		t.Errorf("event[1] = %s, want turn_start", allEvents[1].Type)
	}

	// Should have agent_end as last event
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoop_ToolCall(t *testing.T) {
	events := make(chan AgentEvent, 100)

	readTool := AgentTool{
		Tool: ai.Tool{
			Name:        "read",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		},
		Label: "Read",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate AgentToolUpdateCallback) (AgentToolResult, error) {
			return AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: "file contents"}},
			}, nil
		},
	}

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Read test.txt", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		SystemPrompt: "You are helpful.",
		Messages:     []AgentMessage{},
		Tools: ToolSetFrom([]AgentTool{readTool}),
	}

	streamFn := mockStreamFn(
		toolCallResponse("read", "call-1", map[string]any{"path": "test.txt"}),
		simpleResponse("The file contains: file contents"),
	)

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Should have tool execution events
	hasToolStart := false
	hasToolEnd := false
	for _, e := range allEvents {
		if e.Type == EventToolExecutionStart {
			hasToolStart = true
			if e.ToolName != "read" {
				t.Errorf("tool name = %s, want read", e.ToolName)
			}
		}
		if e.Type == EventToolExecutionEnd {
			hasToolEnd = true
		}
	}
	if !hasToolStart {
		t.Error("missing tool_execution_start event")
	}
	if !hasToolEnd {
		t.Error("missing tool_execution_end event")
	}
}

func TestAgentLoop_ErrorResponse(t *testing.T) {
	events := make(chan AgentEvent, 100)

	errorMsg := &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "rate limited",
		Timestamp:    time.Now().UnixMilli(),
	}

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Hello!", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
	}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, mockStreamFn(errorMsg), events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Should end with agent_end
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoop_ToolNotFound(t *testing.T) {
	events := make(chan AgentEvent, 100)

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Do something", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
		Tools:    nil, // No tools registered
	}

	streamFn := mockStreamFn(
		toolCallResponse("nonexistent", "call-1", map[string]any{}),
		simpleResponse("Sorry, tool not found"),
	)

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Should have error tool result
	hasToolEnd := false
	for _, e := range allEvents {
		if e.Type == EventToolExecutionEnd && e.IsError {
			hasToolEnd = true
		}
	}
	if !hasToolEnd {
		t.Error("expected error tool_execution_end for missing tool")
	}
}

func TestAgentLoop_SteeringSkipsRemainingTools(t *testing.T) {
	events := make(chan AgentEvent, 200)

	var toolExecutions []string

	slowTool := AgentTool{
		Tool: ai.Tool{
			Name:        "slow",
			Description: "Slow tool",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		Label: "Slow",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate AgentToolUpdateCallback) (AgentToolResult, error) {
			toolExecutions = append(toolExecutions, toolCallID)
			return AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: "done"}},
			}, nil
		},
	}

	// Response with TWO tool calls
	multiToolMsg := &ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("call-1", "slow", map[string]any{}),
			ai.NewToolCallContent("call-2", "slow", map[string]any{}),
		},
		Api:        ai.ApiAnthropicMessages,
		Provider:   ai.ProviderAnthropic,
		Model:      "test-model",
		StopReason: ai.StopReasonToolUse,
		Timestamp:  time.Now().UnixMilli(),
	}

	steeringCalled := 0
	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetSteeringMessages: func() ([]AgentMessage, error) {
			steeringCalled++
			// After the first tool call, inject a steering message
			if steeringCalled == 2 { // first call is at loop start, second is after first tool
				return []AgentMessage{
					NewAgentMessage(ai.NewUserMsg("Stop! New instruction.", time.Now().UnixMilli())),
				}, nil
			}
			return nil, nil
		},
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Run two tools", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
		Tools: ToolSetFrom([]AgentTool{slowTool}),
	}

	streamFn := mockStreamFn(
		multiToolMsg,
		simpleResponse("OK, I'll follow the new instruction"),
	)

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Only the first tool should have actually executed
	if len(toolExecutions) != 1 {
		t.Errorf("expected 1 tool execution, got %d: %v", len(toolExecutions), toolExecutions)
	}

	// The second tool should have been skipped with an error result
	skippedCount := 0
	for _, e := range allEvents {
		if e.Type == EventToolExecutionEnd && e.IsError && e.ToolCallID == "call-2" {
			skippedCount++
		}
	}
	if skippedCount != 1 {
		t.Errorf("expected 1 skipped tool, got %d", skippedCount)
	}

	// Should end with agent_end
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoop_FollowUpMessages(t *testing.T) {
	events := make(chan AgentEvent, 200)

	followUpCalled := 0
	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			followUpCalled++
			if followUpCalled == 1 {
				return []AgentMessage{
					NewAgentMessage(ai.NewUserMsg("Follow up question", time.Now().UnixMilli())),
				}, nil
			}
			return nil, nil
		},
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Hello!", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
	}

	streamFn := mockStreamFn(
		simpleResponse("First reply"),
		simpleResponse("Follow-up reply"),
	)

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Should have TWO turn_start events (one for initial, one for follow-up)
	turnStarts := 0
	for _, e := range allEvents {
		if e.Type == EventTurnStart {
			turnStarts++
		}
	}
	if turnStarts < 2 {
		t.Errorf("expected at least 2 turn_start events, got %d", turnStarts)
	}

	// Should end with agent_end
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoop_SteeringCallbackError(t *testing.T) {
	events := make(chan AgentEvent, 100)

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetSteeringMessages: func() ([]AgentMessage, error) {
			return nil, fmt.Errorf("steering error")
		},
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Hello!", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
	}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, mockStreamFn(simpleResponse("Hi")), events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Should complete normally despite steering error
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoop_ContextCancellationDuringTool(t *testing.T) {
	events := make(chan AgentEvent, 200)

	ctx, cancel := context.WithCancel(context.Background())

	blockingTool := AgentTool{
		Tool: ai.Tool{
			Name:        "blocking",
			Description: "Blocks until cancelled",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		Label: "Blocking",
		Execute: func(toolCtx context.Context, toolCallID string, params map[string]any, onUpdate AgentToolUpdateCallback) (AgentToolResult, error) {
			// Cancel the parent context while tool is running
			cancel()
			// Tool respects context cancellation
			<-toolCtx.Done()
			return AgentToolResult{}, toolCtx.Err()
		},
	}

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Do blocking thing", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
		Tools: ToolSetFrom([]AgentTool{blockingTool}),
	}

	streamFn := mockStreamFn(
		toolCallResponse("blocking", "call-1", map[string]any{}),
		simpleResponse("Done"),
	)

	go func() {
		AgentLoop(ctx, []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// Tool execution should end with an error
	hasToolError := false
	for _, e := range allEvents {
		if e.Type == EventToolExecutionEnd && e.IsError {
			hasToolError = true
		}
	}
	if !hasToolError {
		t.Error("expected tool_execution_end with error after context cancellation")
	}

	// Should end with agent_end
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoopContinue_EmptyMessages(t *testing.T) {
	events := make(chan AgentEvent, 100)
	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	_, err := AgentLoopContinue(context.Background(), agentCtx, config, mockStreamFn(simpleResponse("hi")), events)
	if err == nil {
		t.Error("expected error for empty messages")
	}
}

func TestAgentLoopContinue_AssistantMessage(t *testing.T) {
	events := make(chan AgentEvent, 100)
	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}
	agentCtx := &AgentContext{
		Messages: []AgentMessage{
			NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
				Content:    []ai.AssistantContent{ai.NewTextContent("hello")},
				Api:        ai.ApiAnthropicMessages,
				Provider:   ai.ProviderAnthropic,
				Model:      "test",
				StopReason: ai.StopReasonStop,
			})),
		},
	}

	msgs, err := AgentLoopContinue(context.Background(), agentCtx, config, mockStreamFn(simpleResponse("continued")), events)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(msgs) == 0 {
		t.Error("expected messages from continued loop")
	}
}
