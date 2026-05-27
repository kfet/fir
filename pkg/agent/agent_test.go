package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAgent_Defaults(t *testing.T) {
	a := NewAgent(AgentOptions{})
	require.NotNil(t, a)

	state := a.State()
	assert.Equal(t, ThinkingOff, state.ThinkingLevel)
	assert.False(t, state.IsStreaming)
	assert.Nil(t, state.Model)
	assert.Nil(t, state.Messages)
}

func TestNewAgent_WithOptions(t *testing.T) {
	model := testModel()
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{
			SystemPrompt:  "Be helpful",
			Model:         model,
			ThinkingLevel: ThinkingHigh,
		},
		SteeringMode: "all",
		FollowUpMode: "all",
		SessionID:    "session-123",
	})

	state := a.State()
	assert.Equal(t, "Be helpful", state.SystemPrompt)
	assert.Equal(t, model, state.Model)
	assert.Equal(t, ThinkingHigh, state.ThinkingLevel)
	assert.Equal(t, "session-123", a.GetSessionID())
	assert.Equal(t, "all", a.GetSteeringMode())
	assert.Equal(t, "all", a.GetFollowUpMode())
}

func TestAgent_SettersAndGetters(t *testing.T) {
	a := NewAgent(AgentOptions{})
	model := testModel()

	a.SetSystemPrompt("You are a test agent")
	a.SetModel(model)
	a.SetThinkingLevel(ThinkingMedium)
	a.SetSessionID("sid-456")
	a.SetSteeringMode("all")
	a.SetFollowUpMode("all")

	state := a.State()
	assert.Equal(t, "You are a test agent", state.SystemPrompt)
	assert.Equal(t, model, state.Model)
	assert.Equal(t, ThinkingMedium, state.ThinkingLevel)
	assert.Equal(t, "sid-456", a.GetSessionID())
	assert.Equal(t, "all", a.GetSteeringMode())
	assert.Equal(t, "all", a.GetFollowUpMode())
}

func TestAgent_MessageOperations(t *testing.T) {
	a := NewAgent(AgentOptions{})

	msg1 := NewAgentMessage(core.NewUserMsg("hello", 0))
	msg2 := NewAgentMessage(core.NewUserMsg("world", 0))

	a.AppendMessage(msg1)
	a.AppendMessage(msg2)

	state := a.State()
	assert.Len(t, state.Messages, 2)

	a.ReplaceMessages([]AgentMessage{msg1})
	state = a.State()
	assert.Len(t, state.Messages, 1)

	a.ClearMessages()
	state = a.State()
	assert.Nil(t, state.Messages)
}

func TestAgent_QueueOperations(t *testing.T) {
	a := NewAgent(AgentOptions{})

	msg := NewAgentMessage(core.NewUserMsg("steering", 0))
	a.Steer(msg)
	assert.True(t, a.HasQueuedMessages())

	followUp := NewAgentMessage(core.NewUserMsg("follow up", 0))
	a.FollowUp(followUp)
	assert.True(t, a.HasQueuedMessages())

	a.ClearSteeringQueue()
	assert.True(t, a.HasQueuedMessages()) // still has follow-up

	a.ClearAllQueues()
	assert.False(t, a.HasQueuedMessages())
}

func TestAgent_PeekFollowUpQueue(t *testing.T) {
	a := NewAgent(AgentOptions{})

	// Empty queue returns empty slice.
	assert.Empty(t, a.PeekFollowUpQueue())

	m1 := NewAgentMessage(core.NewUserMsg("first", 0))
	m2 := NewAgentMessage(core.NewUserMsg("second", 0))
	a.FollowUp(m1)
	a.FollowUp(m2)

	snap := a.PeekFollowUpQueue()
	assert.Len(t, snap, 2)

	// Peek must not modify the queue.
	assert.Equal(t, 2, a.FollowUpQueueLen())

	// Mutating the returned slice must not affect the internal queue.
	snap[0] = NewAgentMessage(core.NewUserMsg("mutated", 0))
	assert.Equal(t, 2, a.FollowUpQueueLen())
	fresh := a.PeekFollowUpQueue()
	assert.Equal(t, "first", fresh[0].Message.AsUser().Content)
}

func TestAgent_RemoveFollowUp(t *testing.T) {
	a := NewAgent(AgentOptions{})

	m1 := NewAgentMessage(core.NewUserMsg("first", 0))
	m2 := NewAgentMessage(core.NewUserMsg("second", 0))
	m3 := NewAgentMessage(core.NewUserMsg("third", 0))
	a.FollowUp(m1)
	a.FollowUp(m2)
	a.FollowUp(m3)

	// Out-of-range indices return false.
	_, ok := a.RemoveFollowUp(-1)
	assert.False(t, ok)
	_, ok = a.RemoveFollowUp(3)
	assert.False(t, ok)

	// Remove the middle item (0-based index 1).
	removed, ok := a.RemoveFollowUp(1)
	require.True(t, ok)
	assert.Equal(t, "second", removed.Message.AsUser().Content)
	assert.Equal(t, 2, a.FollowUpQueueLen())

	// Remaining items preserve order.
	remaining := a.PeekFollowUpQueue()
	assert.Equal(t, "first", remaining[0].Message.AsUser().Content)
	assert.Equal(t, "third", remaining[1].Message.AsUser().Content)

	// Remove first item.
	removed, ok = a.RemoveFollowUp(0)
	require.True(t, ok)
	assert.Equal(t, "first", removed.Message.AsUser().Content)
	assert.Equal(t, 1, a.FollowUpQueueLen())
}

func TestAgent_Subscribe(t *testing.T) {
	a := NewAgent(AgentOptions{})
	var received []AgentEvent
	var mu sync.Mutex

	unsub := a.Subscribe(func(e AgentEvent) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	a.emit(AgentEvent{Type: EventAgentStart})

	mu.Lock()
	assert.Len(t, received, 1)
	assert.Equal(t, EventAgentStart, received[0].Type)
	mu.Unlock()

	unsub()
	a.emit(AgentEvent{Type: EventAgentEnd})

	mu.Lock()
	assert.Len(t, received, 1)
	mu.Unlock()
}

func TestAgent_Reset(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.AppendMessage(NewAgentMessage(core.NewUserMsg("hello", 0)))
	a.Steer(NewAgentMessage(core.NewUserMsg("steer", 0)))
	a.FollowUp(NewAgentMessage(core.NewUserMsg("follow", 0)))

	a.Reset()

	state := a.State()
	assert.Nil(t, state.Messages)
	assert.False(t, state.IsStreaming)
	assert.Empty(t, state.Error)
	assert.False(t, a.HasQueuedMessages())
}

func TestAgent_PromptNoModel(t *testing.T) {
	a := NewAgent(AgentOptions{})
	err := a.Prompt("hello")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no model configured")
}

func TestAgent_PromptWhileStreaming(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetModel(testModel())

	a.mu.Lock()
	a.state.IsStreaming = true
	a.mu.Unlock()

	err := a.Prompt("hello")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already processing")
}

func TestAgent_ContinueNoMessages(t *testing.T) {
	a := NewAgent(AgentOptions{})
	err := a.Continue()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no messages")
}

func TestAgent_ContinueFromAssistant_NoQueued(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{
			Model: testModel(),
		},
		StreamFn: mockStreamFn(simpleResponse("continued")),
	})

	assistMsg := core.AssistantMessage{
		Role:       core.RoleAssistant,
		Content:    []core.AssistantContent{core.NewTextContent("partial response")},
		StopReason: core.StopReasonStop,
	}
	a.AppendMessage(NewAgentMessage(core.NewAssistantMsg(assistMsg)))

	err := a.Continue()
	assert.NoError(t, err)

	// Wait for the async loop to finish.
	a.WaitForIdle()

	// Should have the original assistant, the steering "continue" message, and the new assistant response.
	state := a.State()
	assert.GreaterOrEqual(t, len(state.Messages), 3)
	// The "continue" message is injected as steering (invisible), but still present in messages.
	assert.Equal(t, "assistant", state.Messages[2].Role())
}

func TestAgent_Prompt_SimpleResponse(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{
			Model: testModel(),
		},
		StreamFn: mockStreamFn(simpleResponse("Hello back!")),
	})

	var events []AgentEvent
	var mu sync.Mutex

	a.Subscribe(func(e AgentEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	err := a.Prompt("Hello")
	require.NoError(t, err)

	a.WaitForIdle()

	state := a.State()
	assert.False(t, state.IsStreaming)
	assert.GreaterOrEqual(t, len(state.Messages), 2)

	mu.Lock()
	hasAgentStart := false
	hasAgentEnd := false
	for _, e := range events {
		if e.Type == EventAgentStart {
			hasAgentStart = true
		}
		if e.Type == EventAgentEnd {
			hasAgentEnd = true
		}
	}
	mu.Unlock()

	assert.True(t, hasAgentStart)
	assert.True(t, hasAgentEnd)
}

func TestAgent_ThinkingBudgets(t *testing.T) {
	a := NewAgent(AgentOptions{})
	assert.Nil(t, a.GetThinkingBudgets())

	val := 1000
	tb := &core.ThinkingBudgets{High: &val}
	a.SetThinkingBudgets(tb)
	assert.Equal(t, tb, a.GetThinkingBudgets())
}

func TestAgent_MaxRetryDelayMs(t *testing.T) {
	a := NewAgent(AgentOptions{})
	assert.Nil(t, a.GetMaxRetryDelayMs())

	val := 30000
	a.SetMaxRetryDelayMs(&val)
	assert.Equal(t, &val, a.GetMaxRetryDelayMs())
}

func TestAgent_DequeueSteeringOneAtATime(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetSteeringMode("one-at-a-time")

	msg1 := NewAgentMessage(core.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(core.NewUserMsg("second", 0))
	a.Steer(msg1)
	a.Steer(msg2)

	got := a.dequeueSteeringMessages()
	assert.Len(t, got, 1)
	assert.True(t, a.HasQueuedMessages())

	got = a.dequeueSteeringMessages()
	assert.Len(t, got, 1)
	assert.False(t, a.HasQueuedMessages())
}

func TestAgent_DequeueSteeringAll(t *testing.T) {
	a := NewAgent(AgentOptions{SteeringMode: "all"})

	msg1 := NewAgentMessage(core.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(core.NewUserMsg("second", 0))
	a.Steer(msg1)
	a.Steer(msg2)

	got := a.dequeueSteeringMessages()
	assert.Len(t, got, 2)
	assert.False(t, a.HasQueuedMessages())
}

func TestAgent_DequeueFollowUpOneAtATime(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetFollowUpMode("one-at-a-time")

	msg1 := NewAgentMessage(core.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(core.NewUserMsg("second", 0))
	a.FollowUp(msg1)
	a.FollowUp(msg2)

	got := a.dequeueFollowUpMessages()
	assert.Len(t, got, 1)

	got = a.dequeueFollowUpMessages()
	assert.Len(t, got, 1)

	got = a.dequeueFollowUpMessages()
	assert.Nil(t, got)
}

func TestAgent_DequeueFollowUpAll(t *testing.T) {
	a := NewAgent(AgentOptions{FollowUpMode: "all"})

	msg1 := NewAgentMessage(core.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(core.NewUserMsg("second", 0))
	a.FollowUp(msg1)
	a.FollowUp(msg2)

	got := a.dequeueFollowUpMessages()
	assert.Len(t, got, 2)
}

func TestDefaultConvertToLLM_Agent(t *testing.T) {
	messages := []AgentMessage{
		NewAgentMessage(core.NewUserMsg("hello", 0)),
		NewAgentMessage(core.NewAssistantMsg(core.AssistantMessage{
			Role:       core.RoleAssistant,
			Content:    []core.AssistantContent{core.NewTextContent("hi")},
			StopReason: core.StopReasonStop,
		})),
	}

	result, err := DefaultConvertToLLM(messages)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestAgent_WaitForIdle_WhenNotRunning(t *testing.T) {
	a := NewAgent(AgentOptions{})
	done := make(chan struct{})
	go func() {
		a.WaitForIdle()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(100 * time.Millisecond):
		t.Fatal("WaitForIdle should return immediately when not running")
	}
}

func TestAgent_Abort(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.Abort()
}

// ============================================================================
// SimplePrompt
// ============================================================================

func TestSimplePrompt_ReturnsText(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{
			SystemPrompt: "you are a test assistant",
		},
		StreamFn:     mockStreamFn(simpleResponse("hello from simple prompt")),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	msgs := []AgentMessage{NewAgentMessage(core.NewUserMsg("test question", 0))}
	got, err := a.SimplePrompt(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello from simple prompt" {
		t.Errorf("expected %q, got %q", "hello from simple prompt", got)
	}
}

func TestSimplePrompt_ErrorOnNoModel(t *testing.T) {
	a := NewAgent(AgentOptions{
		ConvertToLLM: DefaultConvertToLLM,
	})
	_, err := a.SimplePrompt(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error when no model set")
	}
	if !strings.Contains(err.Error(), "no model") {
		t.Errorf("expected 'no model' error, got: %v", err)
	}
}

func TestSimplePrompt_ErrorMessageFromModel(t *testing.T) {
	errMsg := &core.AssistantMessage{
		Role:         "assistant",
		ErrorMessage: "rate limit exceeded",
		StopReason:   core.StopReasonStop,
	}
	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(errMsg),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	_, err := a.SimplePrompt(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error for error message from model")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("expected 'rate limit exceeded' error, got: %v", err)
	}
}

func TestSimplePrompt_DoesNotMutateAgentState(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{
			SystemPrompt: "test",
		},
		StreamFn:     mockStreamFn(simpleResponse("response")),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())
	a.AppendMessage(NewAgentMessage(core.NewUserMsg("existing", 0)))

	before := len(a.State().Messages)
	msgs := []AgentMessage{
		NewAgentMessage(core.NewUserMsg("existing", 0)),
		NewAgentMessage(core.NewUserMsg("side question", 0)),
	}
	_, err := a.SimplePrompt(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := len(a.State().Messages)
	if before != after {
		t.Errorf("agent messages changed: before=%d after=%d", before, after)
	}
}

// captureStreamFn returns a StreamFn that records the model and reasoning
// it was invoked with. Used to assert SimplePromptOptions overrides take
// effect without leaking through to the agent's persistent state.
func captureStreamFn(seenModel **core.Model, seenReasoning *core.ThinkingLevel, response *core.AssistantMessage) StreamFn {
	return func(model *core.Model, c core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		*seenModel = model
		if options != nil {
			*seenReasoning = options.Reasoning
		}
		s := core.NewAssistantMessageEventStream()
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: response})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: response.StopReason, Message: response})
			s.End(nil)
		}()
		return s
	}
}

func TestSimplePrompt_OverrideModel(t *testing.T) {
	defaultModel := testModel()
	overrideModel := &core.Model{
		ID: "advisor-model", Name: "Advisor", Api: core.ApiAnthropicMessages,
		Provider: core.ProviderAnthropic, ContextWindow: 200000, MaxTokens: 4096,
	}

	var seenModel *core.Model
	var seenReasoning core.ThinkingLevel
	a := NewAgent(AgentOptions{
		StreamFn:     captureStreamFn(&seenModel, &seenReasoning, simpleResponse("ok")),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(defaultModel)

	// No override → uses agent's model.
	_, err := a.SimplePrompt(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenModel == nil || seenModel.ID != defaultModel.ID {
		t.Errorf("expected default model, got %+v", seenModel)
	}

	// With override → uses overrideModel.
	_, err = a.SimplePrompt(context.Background(), nil, &SimplePromptOptions{Model: overrideModel})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenModel == nil || seenModel.ID != overrideModel.ID {
		t.Errorf("expected override model, got %+v", seenModel)
	}

	// Agent state must not have been mutated by the override.
	if a.State().Model.ID != defaultModel.ID {
		t.Errorf("agent model mutated: got %s, want %s", a.State().Model.ID, defaultModel.ID)
	}
}

func TestSimplePrompt_OverrideReasoning(t *testing.T) {
	var seenModel *core.Model
	var seenReasoning core.ThinkingLevel
	a := NewAgent(AgentOptions{
		StreamFn:     captureStreamFn(&seenModel, &seenReasoning, simpleResponse("ok")),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())
	// Agent's default reasoning is ThinkingOff.

	// With reasoning override → uses overridden level.
	_, err := a.SimplePrompt(context.Background(), nil, &SimplePromptOptions{Reasoning: core.ThinkingHigh})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenReasoning != core.ThinkingHigh {
		t.Errorf("expected reasoning %q, got %q", core.ThinkingHigh, seenReasoning)
	}

	// Agent's persistent thinking level must not have been touched.
	if a.State().ThinkingLevel != ThinkingOff {
		t.Errorf("agent thinking level mutated: got %s, want %s", a.State().ThinkingLevel, ThinkingOff)
	}
}

// TestSimplePrompt_ThinkingOnlyReturnsMarker verifies that a response which
// contains only thinking blocks (no text, no tool_use) still produces useful
// output: the thinking content is surfaced with a [think] marker rather than
// silently returned as an empty string.
func TestSimplePrompt_ThinkingOnlyReturnsMarker(t *testing.T) {
	resp := &core.AssistantMessage{
		Role: "assistant",
		Content: []core.AssistantContent{
			core.NewThinkingContent("weighed options A and B"),
		},
		Api: core.ApiAnthropicMessages, Provider: core.ProviderAnthropic,
		Model: "test-model", StopReason: core.StopReasonStop,
	}
	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(resp),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	got, err := a.SimplePrompt(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "[think]") || !strings.Contains(got, "weighed options A and B") {
		t.Errorf("expected [think] marker with content, got: %q", got)
	}
}

// TestSimplePrompt_ToolCallOnlyReturnsMarker verifies that a response which
// only requested tool calls (no text, no thinking) surfaces a [tool ...]
// marker for each call. Useful signal: the advisor wanted to run a tool that
// SimplePrompt callers can't execute.
func TestSimplePrompt_ToolCallOnlyReturnsMarker(t *testing.T) {
	resp := &core.AssistantMessage{
		Role: "assistant",
		Content: []core.AssistantContent{
			core.NewToolCallContent("toolu_x", "Bash", map[string]any{"command": "ls"}),
		},
		Api: core.ApiAnthropicMessages, Provider: core.ProviderAnthropic,
		Model: "test-model", StopReason: core.StopReasonToolUse,
	}
	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(resp),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	got, err := a.SimplePrompt(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "[tool Bash") || !strings.Contains(got, `"command":"ls"`) {
		t.Errorf("expected [tool Bash ...] marker with args, got: %q", got)
	}
}

// TestSimplePrompt_MixedContent verifies text + thinking + tool_use are all
// surfaced together, in order, with the appropriate markers.
func TestSimplePrompt_MixedContent(t *testing.T) {
	resp := &core.AssistantMessage{
		Role: "assistant",
		Content: []core.AssistantContent{
			core.NewTextContent("here's my take."),
			core.NewThinkingContent("comparing options"),
			core.NewToolCallContent("toolu_y", "Read", map[string]any{"path": "/x"}),
		},
		Api: core.ApiAnthropicMessages, Provider: core.ProviderAnthropic,
		Model: "test-model", StopReason: core.StopReasonToolUse,
	}
	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(resp),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	got, err := a.SimplePrompt(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"here's my take.", "[think] comparing options", "[tool Read"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected output to contain %q; got: %q", want, got)
		}
	}
	// Order: text first, then thinking, then tool.
	iText := strings.Index(got, "here's my take.")
	iThink := strings.Index(got, "[think]")
	iTool := strings.Index(got, "[tool ")
	if !(iText < iThink && iThink < iTool) {
		t.Errorf("expected text < thinking < tool order; positions: text=%d think=%d tool=%d (got %q)", iText, iThink, iTool, got)
	}
}

// TestSimplePrompt_EmptyContentReturnsError verifies that a response with
// zero content blocks returns a clear error (rather than an empty string).
func TestSimplePrompt_EmptyContentReturnsError(t *testing.T) {
	resp := &core.AssistantMessage{
		Role:       "assistant",
		Content:    []core.AssistantContent{},
		Api:        core.ApiAnthropicMessages,
		Provider:   core.ProviderAnthropic,
		Model:      "test-model",
		StopReason: core.StopReasonStop,
	}
	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(resp),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	_, err := a.SimplePrompt(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error on empty response")
	}
	if !strings.Contains(err.Error(), "no content") && !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "no usable content") {
		t.Errorf("expected 'no content' / 'empty' error, got: %v", err)
	}
}

// TestSimplePrompt_EmptyContentErrorIncludesBlockSummary verifies that the
// "no usable content" error from a side query with only a redacted thinking
// block carries the per-block summary inline, so callers (e.g. the aside
// extension) can classify the failure without parsing the raw message.
func TestSimplePrompt_EmptyContentErrorIncludesBlockSummary(t *testing.T) {
	resp := &core.AssistantMessage{
		Role: "assistant",
		Content: []core.AssistantContent{
			{Thinking: &core.ThinkingContent{Thinking: "", ThinkingSignature: "REDACTED_PAYLOAD"}},
		},
		StopReason: core.StopReasonStop,
	}
	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(resp),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	_, err := a.SimplePrompt(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error on empty response")
	}
	msg := err.Error()
	if !strings.Contains(msg, "blocks:") {
		t.Errorf("error missing block summary: %v", err)
	}
	if !strings.Contains(msg, "thinking(th=0,sig=") {
		t.Errorf("error missing thinking(th=, sig=) marker: %v", err)
	}
}

// TestSimplePromptStream_ForwardsEventsAndMatchesSimplePrompt verifies the
// streaming variant fires onEvent for each agent event and that the final
// text matches what SimplePrompt would have produced with no callback.
func TestSimplePromptStream_ForwardsEventsAndMatchesSimplePrompt(t *testing.T) {
	respText := "streamed response"
	resp := simpleResponse(respText)

	a := NewAgent(AgentOptions{
		StreamFn:     mockStreamFn(resp),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())

	var events []AgentEvent
	text, msg, err := a.SimplePromptStream(context.Background(), nil, nil, func(ev AgentEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != respText {
		t.Errorf("text = %q, want %q", text, respText)
	}
	if msg == nil {
		t.Fatal("expected non-nil final message")
	}
	if len(events) == 0 {
		t.Fatal("expected callback to fire at least once")
	}
	// Must include at least one message_start and one message_end.
	sawStart, sawEnd := false, false
	for _, ev := range events {
		switch ev.Type {
		case EventMessageStart:
			sawStart = true
		case EventMessageEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Errorf("missing lifecycle events: start=%v end=%v", sawStart, sawEnd)
	}

	// And the no-callback flavor must agree on the rendered text.
	plain, err := a.SimplePrompt(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("SimplePrompt: %v", err)
	}
	if plain != respText {
		t.Errorf("SimplePrompt text = %q, want %q", plain, respText)
	}
}

// --- SimplePrompt / SideQuery transient-result retry --------------------

// thinkingOnlyResponse simulates the degenerate generation that recurs with
// thinking models on side queries: a thinking block carrying a signature but
// no thinking text, and no text block — a clean stop with no usable content.
func thinkingOnlyResponse() *core.AssistantMessage {
	tc := core.NewThinkingContent("")
	tc.Thinking.ThinkingSignature = "sig-1300-chars-abcdefghij"
	return &core.AssistantMessage{
		Role:       "assistant",
		Content:    []core.AssistantContent{tc},
		Api:        core.ApiAnthropicMessages,
		Provider:   core.ProviderAnthropic,
		Model:      "test-model",
		StopReason: core.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

// TestSimplePrompt_RetriesDegenerateThinkingOnly verifies that a thinking-only/
// empty response is re-rolled rather than dead-ending the call — the recurring
// "side-query: response had no usable content" advisor failure.
func TestSimplePrompt_RetriesDegenerateThinkingOnly(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel()},
		StreamFn:     mockStreamFn(thinkingOnlyResponse(), simpleResponse("the real answer")),
	})

	text, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(core.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "the real answer", text)
}

// TestSimplePrompt_RetriesTransportError verifies a transport/stream error is
// re-rolled on the single-shot path (which has no agent-loop auto-resume).
func TestSimplePrompt_RetriesTransportError(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel()},
		StreamFn:     mockStreamFn(transportError("", connResetErr), simpleResponse("recovered")),
	})

	text, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(core.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "recovered", text)
}

// TestSimplePrompt_DoesNotRetryGenuineError verifies a non-transient API
// rejection (e.g. 400) is surfaced immediately, without burning retries.
func TestSimplePrompt_DoesNotRetryGenuineError(t *testing.T) {
	calls := 0
	streamFn := func(model *core.Model, ctx core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		calls++
		s := core.NewAssistantMessageEventStream()
		msg := transportError("", "400 invalid request: messages.0: too long")
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: msg})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel()},
		StreamFn:     streamFn,
	})

	_, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(core.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.Error(t, err)
	assert.Equal(t, 1, calls, "genuine 400 error must not be retried")
}

// TestSimplePrompt_DisablesThinkingOnNoContentRetry verifies the refinement:
// after a no-usable-content (thinking-only) result, the retry forces thinking
// OFF so the model is structurally required to emit text. The first attempt
// uses the configured reasoning level; the retry uses ThinkingOff.
func TestSimplePrompt_DisablesThinkingOnNoContentRetry(t *testing.T) {
	var reasonings []core.ThinkingLevel
	responses := []*core.AssistantMessage{thinkingOnlyResponse(), simpleResponse("answer")}
	idx := 0
	streamFn := func(model *core.Model, ctx core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		reasonings = append(reasonings, options.Reasoning)
		s := core.NewAssistantMessageEventStream()
		msg := responses[idx]
		if idx < len(responses)-1 {
			idx++
		}
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: msg})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel(), ThinkingLevel: ThinkingMedium},
		StreamFn:     streamFn,
	})

	text, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(core.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "answer", text)
	require.Len(t, reasonings, 2)
	assert.Equal(t, core.ThinkingMedium, reasonings[0], "first attempt keeps configured reasoning")
	assert.Equal(t, core.ThinkingOff, reasonings[1], "no-content retry forces thinking off")
}

// TestSimplePrompt_KeepsReasoningOnTransportRetry verifies that a transport
// error retry does NOT disable thinking (only no-content results do).
func TestSimplePrompt_KeepsReasoningOnTransportRetry(t *testing.T) {
	var reasonings []core.ThinkingLevel
	responses := []*core.AssistantMessage{transportError("", connResetErr), simpleResponse("ok")}
	idx := 0
	streamFn := func(model *core.Model, ctx core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		reasonings = append(reasonings, options.Reasoning)
		s := core.NewAssistantMessageEventStream()
		msg := responses[idx]
		if idx < len(responses)-1 {
			idx++
		}
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: msg})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel(), ThinkingLevel: ThinkingMedium},
		StreamFn:     streamFn,
	})

	_, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(core.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.NoError(t, err)
	require.Len(t, reasonings, 2)
	assert.Equal(t, core.ThinkingMedium, reasonings[0])
	assert.Equal(t, core.ThinkingMedium, reasonings[1], "transport retry keeps configured reasoning")
}

// TestSimplePrompt_ExhaustsRetriesThenErrors verifies the retry cap: a
// persistently degenerate response eventually surfaces the error rather than
// looping forever.
func TestSimplePrompt_ExhaustsRetriesThenErrors(t *testing.T) {
	calls := 0
	streamFn := func(model *core.Model, ctx core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		calls++
		s := core.NewAssistantMessageEventStream()
		msg := thinkingOnlyResponse()
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: msg})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel()},
		StreamFn:     streamFn,
	})

	_, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(core.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable content")
	assert.Contains(t, err.Error(), "stop_reason=", "error must surface the stop reason for diagnosis")
	assert.Equal(t, len(simplePromptRetryBackoffs)+1, calls, "should attempt exactly maxAttempts times")
}
