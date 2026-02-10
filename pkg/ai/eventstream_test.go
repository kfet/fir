// Ported from: packages/ai/src/utils/event-stream.ts
// Upstream hash: 1caadb2e
package ai

import (
	"sync"
	"testing"
	"time"
)

func TestEventStream_SimpleFlow(t *testing.T) {
	s := NewAssistantMessageEventStream()

	partial := &AssistantMessage{Role: RoleAssistant, Model: "test-model"}
	final := &AssistantMessage{
		Role:       RoleAssistant,
		Model:      "test-model",
		StopReason: StopReasonStop,
		Content:    []AssistantContent{NewTextContent("Hello!")},
	}

	go func() {
		s.Push(AssistantMessageEvent{Type: EventStart, Partial: partial})
		s.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: 0, Partial: partial})
		s.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: 0, Delta: "Hello!", Partial: partial})
		s.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: 0, Content: "Hello!", Partial: partial})
		s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: final})
		s.End(nil)
	}()

	var events []AssistantMessageEvent
	for evt := range s.Events {
		events = append(events, evt)
	}

	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	if events[0].Type != EventStart {
		t.Errorf("expected start, got %q", events[0].Type)
	}
	if events[4].Type != EventDone {
		t.Errorf("expected done, got %q", events[4].Type)
	}
}

func TestEventStream_Result(t *testing.T) {
	s := NewAssistantMessageEventStream()
	final := &AssistantMessage{Role: RoleAssistant, Model: "test-model", StopReason: StopReasonStop}

	go func() {
		s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: final})
		s.End(nil)
	}()

	result := s.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Model != "test-model" {
		t.Errorf("expected 'test-model', got %q", result.Model)
	}
}

func TestEventStream_ErrorResult(t *testing.T) {
	s := NewAssistantMessageEventStream()
	errMsg := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonError, ErrorMessage: "API error"}

	go func() {
		s.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: errMsg})
		s.End(nil)
	}()

	result := s.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.ErrorMessage != "API error" {
		t.Errorf("expected 'API error', got %q", result.ErrorMessage)
	}
}

func TestEventStream_PushAfterEnd(t *testing.T) {
	s := NewAssistantMessageEventStream()

	final := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonStop}
	s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: final})
	s.End(nil)

	// Push after End() should not panic (End closes channel, done=true)
	// The push is silently dropped because s.done is true
	s.Push(AssistantMessageEvent{Type: EventTextDelta, Delta: "late"})

	// Drain any events that were in the channel
	count := 0
	for range s.Events {
		count++
	}
	// Should have exactly the done event
	if count != 1 {
		t.Errorf("expected 1 event, got %d", count)
	}
}

func TestEventStream_ConcurrentPush(t *testing.T) {
	s := NewAssistantMessageEventStream()
	partial := &AssistantMessage{Role: RoleAssistant}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Push(AssistantMessageEvent{Type: EventTextDelta, Delta: "x", Partial: partial})
		}()
	}

	go func() {
		wg.Wait()
		final := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonStop}
		s.Push(AssistantMessageEvent{Type: EventDone, Message: final})
		s.End(nil)
	}()

	count := 0
	for range s.Events {
		count++
	}
	// 10 deltas + 1 done = 11
	if count != 11 {
		t.Errorf("expected 11 events, got %d", count)
	}
}

func TestEventStream_ResultBlocksUntilDone(t *testing.T) {
	s := NewAssistantMessageEventStream()

	done := make(chan struct{})
	go func() {
		s.Result()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Result() should block until done")
	case <-time.After(50 * time.Millisecond):
	}

	final := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonStop}
	s.Push(AssistantMessageEvent{Type: EventDone, Message: final})
	s.End(nil)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Result() should have unblocked")
	}
}

func TestEventStream_EndWithResult(t *testing.T) {
	s := NewAssistantMessageEventStream()
	final := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonStop, Model: "direct-end"}

	go func() {
		s.End(final)
	}()

	result := s.Result()
	if result == nil {
		t.Fatal("expected non-nil result from End()")
	}
	if result.Model != "direct-end" {
		t.Errorf("expected 'direct-end', got %q", result.Model)
	}
}

func TestEventStream_DoubleEnd(t *testing.T) {
	s := NewAssistantMessageEventStream()
	final := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonStop, Model: "double-end"}

	s.Push(AssistantMessageEvent{Type: EventDone, Message: final})
	s.End(nil)

	// Drain events
	for range s.Events {
	}

	// Second End() should not panic (guard exists in End())
	s.End(nil)

	result := s.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Model != "double-end" {
		t.Errorf("expected 'double-end', got %q", result.Model)
	}
}

func TestEventStream_Collect(t *testing.T) {
	s := NewAssistantMessageEventStream()
	final := &AssistantMessage{Role: RoleAssistant, StopReason: StopReasonStop, Model: "collect-test"}

	go func() {
		s.Push(AssistantMessageEvent{Type: EventStart, Partial: final})
		s.Push(AssistantMessageEvent{Type: EventDone, Message: final})
		s.End(nil)
	}()

	events, result := s.Collect()
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
	if result == nil {
		t.Fatal("expected non-nil result from Collect()")
	}
}
