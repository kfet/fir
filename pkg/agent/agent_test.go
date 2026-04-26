package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
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

	msg1 := NewAgentMessage(ai.NewUserMsg("hello", 0))
	msg2 := NewAgentMessage(ai.NewUserMsg("world", 0))

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

	msg := NewAgentMessage(ai.NewUserMsg("steering", 0))
	a.Steer(msg)
	assert.True(t, a.HasQueuedMessages())

	followUp := NewAgentMessage(ai.NewUserMsg("follow up", 0))
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

	m1 := NewAgentMessage(ai.NewUserMsg("first", 0))
	m2 := NewAgentMessage(ai.NewUserMsg("second", 0))
	a.FollowUp(m1)
	a.FollowUp(m2)

	snap := a.PeekFollowUpQueue()
	assert.Len(t, snap, 2)

	// Peek must not modify the queue.
	assert.Equal(t, 2, a.FollowUpQueueLen())

	// Mutating the returned slice must not affect the internal queue.
	snap[0] = NewAgentMessage(ai.NewUserMsg("mutated", 0))
	assert.Equal(t, 2, a.FollowUpQueueLen())
	fresh := a.PeekFollowUpQueue()
	assert.Equal(t, "first", fresh[0].Message.AsUser().Content)
}

func TestAgent_RemoveFollowUp(t *testing.T) {
	a := NewAgent(AgentOptions{})

	m1 := NewAgentMessage(ai.NewUserMsg("first", 0))
	m2 := NewAgentMessage(ai.NewUserMsg("second", 0))
	m3 := NewAgentMessage(ai.NewUserMsg("third", 0))
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
	a.AppendMessage(NewAgentMessage(ai.NewUserMsg("hello", 0)))
	a.Steer(NewAgentMessage(ai.NewUserMsg("steer", 0)))
	a.FollowUp(NewAgentMessage(ai.NewUserMsg("follow", 0)))

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

	assistMsg := ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    []ai.AssistantContent{ai.NewTextContent("partial response")},
		StopReason: ai.StopReasonStop,
	}
	a.AppendMessage(NewAgentMessage(ai.NewAssistantMsg(assistMsg)))

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
	tb := &ai.ThinkingBudgets{High: &val}
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

	msg1 := NewAgentMessage(ai.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(ai.NewUserMsg("second", 0))
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

	msg1 := NewAgentMessage(ai.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(ai.NewUserMsg("second", 0))
	a.Steer(msg1)
	a.Steer(msg2)

	got := a.dequeueSteeringMessages()
	assert.Len(t, got, 2)
	assert.False(t, a.HasQueuedMessages())
}

func TestAgent_DequeueFollowUpOneAtATime(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetFollowUpMode("one-at-a-time")

	msg1 := NewAgentMessage(ai.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(ai.NewUserMsg("second", 0))
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

	msg1 := NewAgentMessage(ai.NewUserMsg("first", 0))
	msg2 := NewAgentMessage(ai.NewUserMsg("second", 0))
	a.FollowUp(msg1)
	a.FollowUp(msg2)

	got := a.dequeueFollowUpMessages()
	assert.Len(t, got, 2)
}

func TestDefaultConvertToLLM_Agent(t *testing.T) {
	messages := []AgentMessage{
		NewAgentMessage(ai.NewUserMsg("hello", 0)),
		NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			Role:       ai.RoleAssistant,
			Content:    []ai.AssistantContent{ai.NewTextContent("hi")},
			StopReason: ai.StopReasonStop,
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

	msgs := []AgentMessage{NewAgentMessage(ai.NewUserMsg("test question", 0))}
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
	errMsg := &ai.AssistantMessage{
		Role:         "assistant",
		ErrorMessage: "rate limit exceeded",
		StopReason:   ai.StopReasonStop,
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
	a.AppendMessage(NewAgentMessage(ai.NewUserMsg("existing", 0)))

	before := len(a.State().Messages)
	msgs := []AgentMessage{
		NewAgentMessage(ai.NewUserMsg("existing", 0)),
		NewAgentMessage(ai.NewUserMsg("side question", 0)),
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
func captureStreamFn(seenModel **ai.Model, seenReasoning *ai.ThinkingLevel, response *ai.AssistantMessage) StreamFn {
	return func(model *ai.Model, c ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		*seenModel = model
		if options != nil {
			*seenReasoning = options.Reasoning
		}
		s := ai.NewAssistantMessageEventStream()
		go func() {
			s.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: response})
			s.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: response.StopReason, Message: response})
			s.End(nil)
		}()
		return s
	}
}

func TestSimplePrompt_OverrideModel(t *testing.T) {
	defaultModel := testModel()
	overrideModel := &ai.Model{
		ID: "advisor-model", Name: "Advisor", Api: ai.ApiAnthropicMessages,
		Provider: ai.ProviderAnthropic, ContextWindow: 200000, MaxTokens: 4096,
	}

	var seenModel *ai.Model
	var seenReasoning ai.ThinkingLevel
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
	var seenModel *ai.Model
	var seenReasoning ai.ThinkingLevel
	a := NewAgent(AgentOptions{
		StreamFn:     captureStreamFn(&seenModel, &seenReasoning, simpleResponse("ok")),
		ConvertToLLM: DefaultConvertToLLM,
	})
	a.SetModel(testModel())
	// Agent's default reasoning is ThinkingOff.

	// With reasoning override → uses overridden level.
	_, err := a.SimplePrompt(context.Background(), nil, &SimplePromptOptions{Reasoning: ai.ThinkingHigh})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenReasoning != ai.ThinkingHigh {
		t.Errorf("expected reasoning %q, got %q", ai.ThinkingHigh, seenReasoning)
	}

	// Agent's persistent thinking level must not have been touched.
	if a.State().ThinkingLevel != ThinkingOff {
		t.Errorf("agent thinking level mutated: got %s, want %s", a.State().ThinkingLevel, ThinkingOff)
	}
}
