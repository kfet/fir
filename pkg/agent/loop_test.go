package agent

import (
	"context"
	"fmt"
	"strings"
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
		Tools:        ToolSetFrom([]AgentTool{readTool}),
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

func TestAgentLoop_SteeringAfterAllToolCalls(t *testing.T) {
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
			// Steering is checked after the full turn (all tool calls),
			// not after each individual tool call.
			if steeringCalled == 2 {
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
		Tools:    ToolSetFrom([]AgentTool{slowTool}),
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

	// Both tools should have executed (no skipping)
	if len(toolExecutions) != 2 {
		t.Errorf("expected 2 tool executions, got %d: %v", len(toolExecutions), toolExecutions)
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
		Tools:    ToolSetFrom([]AgentTool{blockingTool}),
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

func TestAgentLoop_FollowUpAfterError(t *testing.T) {
	// Regression test: when the LLM returns an error (e.g. 429), the agent
	// loop must still drain follow-up messages that arrived during the failed
	// turn. Previously, the loop did an early return on StopReasonError
	// without checking GetFollowUpMessages, silently dropping channel
	// messages that were queued via FollowUp().
	events := make(chan AgentEvent, 100)

	errorMsg := &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "429 rate_limit_error",
		Timestamp:    time.Now().UnixMilli(),
	}

	followUpDelivered := false
	followUpMsg := NewAgentMessage(ai.NewUserMsg("follow-up after error", time.Now().UnixMilli()))

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			if !followUpDelivered {
				followUpDelivered = true
				return []AgentMessage{followUpMsg}, nil
			}
			return nil, nil
		},
	}

	// First call returns error, second call (after follow-up) returns success.
	streamFn := mockStreamFn(errorMsg, simpleResponse("recovered after error"))

	prompt := NewAgentMessage(ai.NewUserMsg("Hello!", time.Now().UnixMilli()))
	agentCtx := &AgentContext{
		Messages: []AgentMessage{},
	}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
	}()

	allEvents := collectEvents(events)

	// The follow-up message must appear in the event stream.
	var sawFollowUp bool
	var sawRecovery bool
	for _, e := range allEvents {
		if e.Type == EventMessageEnd && e.Message != nil {
			if u := e.Message.Message.AsUser(); u != nil {
				if text, ok := u.Content.(string); ok && text == "follow-up after error" {
					sawFollowUp = true
				}
			}
			if a := e.Message.Message.AsAssistant(); a != nil {
				for _, c := range a.Content {
					if c.IsText() && c.Text.Text == "recovered after error" {
						sawRecovery = true
					}
				}
			}
		}
	}

	if !sawFollowUp {
		t.Error("follow-up message was not processed after error — it was silently dropped")
	}
	if !sawRecovery {
		t.Error("agent did not continue with a new turn after processing the follow-up")
	}

	// Last event must be agent_end
	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func TestAgentLoop_NoFollowUpAfterError(t *testing.T) {
	// When there are no follow-up messages after an error, the loop should
	// still exit cleanly (no hang, no panic).
	events := make(chan AgentEvent, 100)

	errorMsg := &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "429 rate_limit_error",
		Timestamp:    time.Now().UnixMilli(),
	}

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			return nil, nil
		},
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

	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

func init() {
	// Speed up mid-tool-call retries for tests.
	midToolCallRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	// Speed up transport auto-resume backoffs for tests (length is the cap).
	autoResumeBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	// Speed up single-shot SimplePrompt/SideQuery retries for tests.
	simplePromptRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond}
}

// TestAgentLoop_FollowUpHookError verifies that when GetFollowUpMessages
// returns an error, the loop logs it and continues (exits) without panicking.
func TestAgentLoop_FollowUpHookError(t *testing.T) {
	events := make(chan AgentEvent, 100)

	errorMsg := &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "429 rate_limit_error",
		Timestamp:    time.Now().UnixMilli(),
	}

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			return nil, fmt.Errorf("simulated hook failure")
		},
	}

	prompt := NewAgentMessage(ai.NewUserMsg("Hello!", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, mockStreamFn(errorMsg), events)
		close(events)
	}()

	allEvents := collectEvents(events)

	last := allEvents[len(allEvents)-1]
	if last.Type != EventAgentEnd {
		t.Errorf("last event = %s, want agent_end", last.Type)
	}
}

// partialToolCallError builds an assistant message representing a stream that
// dropped mid-tool-call: stop_reason=error, content has a tool_use block with
// empty/nil arguments because input_json_delta never completed.
func partialToolCallError(toolName string, partialText string) *ai.AssistantMessage {
	content := []ai.AssistantContent{}
	if partialText != "" {
		content = append(content, ai.NewTextContent(partialText))
	}
	content = append(content, ai.NewToolCallContent("toolu_partial", toolName, nil))
	return &ai.AssistantMessage{
		Role:         "assistant",
		Content:      content,
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "read tcp 1.2.3.4:443: i/o timeout (Anthropic stream ended before message_stop)",
		Timestamp:    time.Now().UnixMilli(),
	}
}

// TestAgentLoop_DropsPartialToolCallAndRetries verifies that when the Anthropic
// streaming connection dies mid-tool-call (stop_reason=error + tool_use block
// with empty Arguments), the agent loop drops the broken partial message from
// history and transparently retries. A subsequent successful response must be
// the only assistant turn that ends up in history.
func TestAgentLoop_DropsPartialToolCallAndRetries(t *testing.T) {
	events := make(chan AgentEvent, 200)

	broken := partialToolCallError("Bash", "I'll check the logs")
	recovered := simpleResponse("Logs look fine")

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		// (retry backoff overridden via midToolCallRetryBackoffs in init below)
	}

	streamFn := mockStreamFn(broken, recovered)

	prompt := NewAgentMessage(ai.NewUserMsg("Show me the logs", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	var returned []AgentMessage
	done := make(chan struct{})
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
		close(done)
	}()

	allEvents := collectEvents(events)
	<-done

	// The partial mid-tool-call assistant message must NOT survive in the
	// returned newMessages set.
	for i, m := range returned {
		if m.Role() != "assistant" {
			continue
		}
		a := m.Message.AsAssistant()
		if a == nil {
			continue
		}
		if a.StopReason == ai.StopReasonError {
			t.Errorf("returned[%d]: partial mid-tool-call error message survived: %+v", i, a)
		}
	}

	sawRetry := false
	sawRecovered := false
	for _, e := range allEvents {
		if e.Type == EventStreamRetry {
			sawRetry = true
		}
		if e.Type == EventMessageEnd && e.Message != nil {
			if a := e.Message.Message.AsAssistant(); a != nil {
				for _, c := range a.Content {
					if c.IsText() && c.Text.Text == "Logs look fine" {
						sawRecovered = true
					}
				}
			}
		}
	}
	if !sawRetry {
		t.Error("expected EventStreamRetry to be emitted after mid-tool-call stream error")
	}
	if !sawRecovered {
		t.Error("expected recovered response to appear in event stream")
	}
}

// TestAgentLoop_MidToolCallRetryExhaustedInjectsUserNote verifies that when
// retries are exhausted (3 attempts all return mid-tool-call errors), the loop
// drops every partial and injects a regular user-role note into history so the
// next turn has accurate context.
func TestAgentLoop_MidToolCallRetryExhaustedInjectsUserNote(t *testing.T) {
	events := make(chan AgentEvent, 200)

	b1 := partialToolCallError("Bash", "checking")
	b2 := partialToolCallError("Bash", "checking")
	b3 := partialToolCallError("Bash", "checking")
	b4 := partialToolCallError("Bash", "checking")

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
	}

	streamFn := mockStreamFn(b1, b2, b3, b4)

	prompt := NewAgentMessage(ai.NewUserMsg("Check logs", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	var returned []AgentMessage
	done := make(chan struct{})
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
		close(done)
	}()

	_ = collectEvents(events)
	<-done

	// No partial assistant error turn must survive in the returned messages.
	for i, m := range returned {
		if m.Role() != "assistant" {
			continue
		}
		a := m.Message.AsAssistant()
		if a != nil && a.StopReason == ai.StopReasonError {
			t.Errorf("returned[%d]: partial error message survived after exhausted retries: %+v", i, a)
		}
	}

	// A synthetic user-role note must have been injected, mentioning that
	// the previous turn was cut off mid-tool-call.
	sawNote := false
	for _, m := range returned {
		if m.Role() != "user" {
			continue
		}
		u := m.Message.AsUser()
		if u == nil {
			continue
		}
		text, _ := u.Content.(string)
		if text != "" && strings.Contains(text, "cut off") && strings.Contains(text, "tool") {
			sawNote = true
		}
	}
	if !sawNote {
		t.Errorf("expected a synthetic user-role note about the mid-tool-call cutoff in returned messages; got=%+v", returned)
	}
}

// TestAgentLoop_MidToolCallExhaustedWithFollowUpsFoldsNote verifies that
// when retries are exhausted AND follow-up messages exist, the cutoff note
// is folded into the first follow-up rather than being appended as its own
// user turn. Anthropic's API tolerates consecutive user-role messages (it
// effectively concatenates them) but folding still keeps the note attached
// to the follow-up that motivated the next turn and avoids gratuitous
// fragmentation of history.
func TestAgentLoop_MidToolCallExhaustedWithFollowUpsFoldsNote(t *testing.T) {
	events := make(chan AgentEvent, 200)

	b1 := partialToolCallError("Bash", "")
	b2 := partialToolCallError("Bash", "")
	b3 := partialToolCallError("Bash", "")
	b4 := partialToolCallError("Bash", "")
	recovered := simpleResponse("ok")

	followUpDelivered := false
	followUpMsg := NewAgentMessage(ai.NewUserMsg("channel follow-up", time.Now().UnixMilli()))

	config := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			if !followUpDelivered {
				followUpDelivered = true
				return []AgentMessage{followUpMsg}, nil
			}
			return nil, nil
		},
	}

	streamFn := mockStreamFn(b1, b2, b3, b4, recovered)

	prompt := NewAgentMessage(ai.NewUserMsg("Show me logs", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	var returned []AgentMessage
	done := make(chan struct{})
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
		close(done)
	}()
	_ = collectEvents(events)
	<-done

	// The cutoff context must be folded INTO the follow-up user message
	// (single combined user message), not appear as a separate user turn.
	var foldedCount int
	for _, m := range returned {
		if m.Role() != "user" {
			continue
		}
		u := m.Message.AsUser()
		if u == nil {
			continue
		}
		text, _ := u.Content.(string)
		if strings.Contains(text, "cut off") && strings.Contains(text, "channel follow-up") {
			foldedCount++
		}
	}
	if foldedCount != 1 {
		t.Errorf("expected exactly one user message carrying both the cutoff note and the follow-up text (folded); got %d. returned=%+v", foldedCount, returned)
	}

	// No standalone synthetic note should remain as a separate user message
	// alongside the folded follow-up.
	standaloneNotes := 0
	for _, m := range returned {
		if m.Role() != "user" {
			continue
		}
		u := m.Message.AsUser()
		if u == nil {
			continue
		}
		text, _ := u.Content.(string)
		if strings.Contains(text, "cut off") && !strings.Contains(text, "channel follow-up") {
			standaloneNotes++
		}
	}
	if standaloneNotes != 0 {
		t.Errorf("expected the cutoff note to be folded into the follow-up, not appear as a standalone user message; standalone count=%d", standaloneNotes)
	}
}

// --- Transport/stream-error auto-resume ---------------------------------

const connResetErr = "read tcp 192.168.50.98:56658->160.79.104.10:443: read: connection reset by peer"

// TestAgentLoop_AutoResumeWithPartialContent verifies the primary bug
// scenario: the assistant emits a short text prefix, the stream is killed by a
// transport reset (stop_reason=error), and the loop auto-resumes by injecting
// the single-symbol AutoResumeMarker user message and continuing — instead of
// pausing for a human. The emitted prefix is preserved as a clean (non-error)
// assistant turn and a subsequent successful response completes the turn.
func TestAgentLoop_AutoResumeWithPartialContent(t *testing.T) {
	events := make(chan AgentEvent, 200)

	broken := transportError("Let me check the logs.", connResetErr)
	recovered := simpleResponse("All good.")

	config := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	streamFn := mockStreamFn(broken, recovered)

	prompt := NewAgentMessage(ai.NewUserMsg("Check the logs", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	var returned []AgentMessage
	done := make(chan struct{})
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
		close(done)
	}()
	allEvents := collectEvents(events)
	<-done

	// EventAutoResume must have been emitted exactly once.
	resumeEvents := 0
	for _, e := range allEvents {
		if e.Type == EventAutoResume {
			resumeEvents++
			if e.RetryAttempt != 1 {
				t.Errorf("first auto-resume RetryAttempt=%d, want 1", e.RetryAttempt)
			}
			if e.ErrorMessage != connResetErr {
				t.Errorf("auto-resume ErrorMessage=%q, want connection reset", e.ErrorMessage)
			}
		}
	}
	if resumeEvents != 1 {
		t.Fatalf("EventAutoResume count=%d, want 1", resumeEvents)
	}

	// The AutoResumeMarker must appear as a user message in history.
	sawMarker := false
	for _, m := range returned {
		if m.Role() != "user" {
			continue
		}
		if text, _ := m.Message.AsUser().Content.(string); text == AutoResumeMarker {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Errorf("expected AutoResumeMarker %q user message in returned history; got %+v", AutoResumeMarker, returned)
	}

	// No assistant error turn must survive — the partial prefix was sanitized.
	for i, m := range returned {
		if a := m.Message.AsAssistant(); a != nil && a.StopReason == ai.StopReasonError {
			t.Errorf("returned[%d]: error assistant turn survived auto-resume: %+v", i, a)
		}
	}

	// The recovered response must be the final assistant turn.
	last := returned[len(returned)-1]
	la := last.Message.AsAssistant()
	if la == nil || la.StopReason != ai.StopReasonStop {
		t.Fatalf("last message is not a clean assistant turn: %+v", last)
	}
	if len(la.Content) == 0 || !la.Content[0].IsText() || la.Content[0].Text.Text != "All good." {
		t.Errorf("final assistant content = %+v, want recovered text", la.Content)
	}

	// The partial prefix must still be present (model continues, not duplicates).
	sawPrefix := false
	for _, m := range returned {
		if a := m.Message.AsAssistant(); a != nil {
			for _, c := range a.Content {
				if c.IsText() && c.Text.Text == "Let me check the logs." {
					sawPrefix = true
				}
			}
		}
	}
	if !sawPrefix {
		t.Errorf("expected the emitted partial prefix to be preserved in history")
	}
}

// TestAgentLoop_AutoResumeEmptyPartialSilentRetry verifies that when the
// transport reset happens before ANY content was emitted, the loop drops the
// empty errored turn and retries transparently (no marker is injected, since
// there is nothing to continue from and a trailing user marker would break
// role alternation).
func TestAgentLoop_AutoResumeEmptyPartialSilentRetry(t *testing.T) {
	events := make(chan AgentEvent, 200)

	broken := transportError("", "write tcp 10.0.0.1:443: write: broken pipe")
	recovered := simpleResponse("Recovered.")

	config := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	streamFn := mockStreamFn(broken, recovered)

	prompt := NewAgentMessage(ai.NewUserMsg("Do the thing", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	var returned []AgentMessage
	done := make(chan struct{})
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
		close(done)
	}()
	allEvents := collectEvents(events)
	<-done

	resumeEvents := 0
	for _, e := range allEvents {
		if e.Type == EventAutoResume {
			resumeEvents++
		}
	}
	if resumeEvents != 1 {
		t.Fatalf("EventAutoResume count=%d, want 1", resumeEvents)
	}

	// No marker should be injected on the empty path.
	for _, m := range returned {
		if m.Role() == "user" {
			if text, _ := m.Message.AsUser().Content.(string); text == AutoResumeMarker {
				t.Errorf("AutoResumeMarker should NOT be injected on the empty-partial path")
			}
		}
	}
	// No empty error turn should survive.
	for i, m := range returned {
		if a := m.Message.AsAssistant(); a != nil && a.StopReason == ai.StopReasonError {
			t.Errorf("returned[%d]: empty error turn survived: %+v", i, a)
		}
	}
	// Recovered response present.
	last := returned[len(returned)-1].Message.AsAssistant()
	if last == nil || last.StopReason != ai.StopReasonStop {
		t.Fatalf("expected clean recovered assistant turn, got %+v", returned[len(returned)-1])
	}
}

// TestAgentLoop_AutoResumeCapExhausted verifies that consecutive transport
// errors are capped: after len(autoResumeBackoffs) auto-resumes the loop falls
// back to the normal behaviour (emit agent_end and stop) rather than looping
// forever on a persistently dead network.
func TestAgentLoop_AutoResumeCapExhausted(t *testing.T) {
	events := make(chan AgentEvent, 400)

	// More broken responses than the cap; every call fails.
	b1 := transportError("partial", connResetErr)
	b2 := transportError("partial", connResetErr)
	b3 := transportError("partial", connResetErr)
	b4 := transportError("partial", connResetErr)
	b5 := transportError("partial", connResetErr)

	config := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	streamFn := mockStreamFn(b1, b2, b3, b4, b5)

	prompt := NewAgentMessage(ai.NewUserMsg("Check", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	var returned []AgentMessage
	done := make(chan struct{})
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, streamFn, events)
		close(events)
		close(done)
	}()
	allEvents := collectEvents(events)
	<-done

	resumeEvents := 0
	for _, e := range allEvents {
		if e.Type == EventAutoResume {
			resumeEvents++
		}
	}
	if resumeEvents != len(autoResumeBackoffs) {
		t.Errorf("EventAutoResume count=%d, want cap %d", resumeEvents, len(autoResumeBackoffs))
	}

	// Must terminate with agent_end, not loop forever.
	if last := allEvents[len(allEvents)-1]; last.Type != EventAgentEnd {
		t.Errorf("last event=%s, want agent_end after cap exhausted", last.Type)
	}
	_ = returned
}

// TestAgentLoop_NonRetryableErrorNotResumed verifies that a genuine model/API
// rejection (e.g. 400 invalid request) is NOT auto-resumed.
func TestAgentLoop_NonRetryableErrorNotResumed(t *testing.T) {
	events := make(chan AgentEvent, 100)

	broken := transportError("", "400 invalid request: messages.0 too long")
	config := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}

	prompt := NewAgentMessage(ai.NewUserMsg("Hi", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, mockStreamFn(broken), events)
		close(events)
	}()
	allEvents := collectEvents(events)

	for _, e := range allEvents {
		if e.Type == EventAutoResume {
			t.Errorf("400 error must not be auto-resumed")
		}
	}
	if last := allEvents[len(allEvents)-1]; last.Type != EventAgentEnd {
		t.Errorf("last event=%s, want agent_end", last.Type)
	}
}

// TestAgentLoop_AbortedNotResumed verifies that a user-aborted turn is never
// auto-resumed, even if its error text resembles a transport failure.
func TestAgentLoop_AbortedNotResumed(t *testing.T) {
	events := make(chan AgentEvent, 100)

	aborted := &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{ai.NewTextContent("stopping")},
		Api:          ai.ApiAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonAborted,
		ErrorMessage: connResetErr,
		Timestamp:    time.Now().UnixMilli(),
	}
	config := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}

	prompt := NewAgentMessage(ai.NewUserMsg("Hi", time.Now().UnixMilli()))
	agentCtx := &AgentContext{Messages: []AgentMessage{}}

	go func() {
		AgentLoop(context.Background(), []AgentMessage{prompt}, agentCtx, config, mockStreamFn(aborted), events)
		close(events)
	}()
	allEvents := collectEvents(events)

	for _, e := range allEvents {
		if e.Type == EventAutoResume {
			t.Errorf("aborted turn must not be auto-resumed")
		}
	}
}

func TestIsResumableStreamError(t *testing.T) {
	resumable := []string{
		connResetErr,
		"write tcp 10.0.0.1:443: write: broken pipe",
		"unexpected EOF",
		"Anthropic stream ended before message_stop",
		"stream ended without result",
		"http2: server sent GOAWAY and closed the connection",
	}
	for _, s := range resumable {
		if !isResumableStreamError(s) {
			t.Errorf("isResumableStreamError(%q) = false, want true", s)
		}
	}
	notResumable := []string{
		"",
		"   ",
		"400 invalid request",
		"401 authentication_error: invalid x-api-key",
		"prompt is too long: 250000 tokens > 200000 maximum",
	}
	for _, s := range notResumable {
		if isResumableStreamError(s) {
			t.Errorf("isResumableStreamError(%q) = true, want false", s)
		}
	}
}

func TestHasReplayableContent(t *testing.T) {
	if hasReplayableContent(transportError("", connResetErr)) {
		t.Error("empty content must not be replayable")
	}
	if !hasReplayableContent(transportError("hello", connResetErr)) {
		t.Error("non-empty text must be replayable")
	}
	if hasReplayableContent(nil) {
		t.Error("nil must not be replayable")
	}
}
