// Ported from: packages/ai/src/stream.ts
// Upstream hash: 1caadb2e
package ai

import (
	"context"
	"testing"
)

func TestStream_NoProvider(t *testing.T) {
	r := NewRegistry()
	model := &Model{API: "nonexistent-api", ID: "m1", Provider: "p1"}
	prompt := Context{Messages: []Message{NewUserMsg("hi", 1000)}}

	stream := Stream(context.Background(), r, model, prompt, nil)
	var events []AssistantMessageEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(events))
	}
	if events[0].Type != EventError {
		t.Errorf("expected error event, got %s", events[0].Type)
	}
	if events[0].Error == nil {
		t.Fatal("expected error message")
	}
	if events[0].Error.ErrorMessage == "" {
		t.Error("expected non-empty error message")
	}
}

func TestStreamSimple_NoProvider(t *testing.T) {
	r := NewRegistry()
	model := &Model{API: "nonexistent-api", ID: "m1", Provider: "p1"}
	prompt := Context{Messages: []Message{NewUserMsg("hi", 1000)}}

	result := CompleteSimple(context.Background(), r, model, prompt, nil)
	if result == nil {
		t.Fatal("expected result")
	}
	if result.StopReason != StopReasonError {
		t.Errorf("expected error stop reason, got %s", result.StopReason)
	}
}

func TestStream_WithProvider(t *testing.T) {
	r := NewRegistry()

	finalMsg := &AssistantMessage{
		Content:    []AssistantContent{NewTextContent("hello")},
		StopReason: StopReasonStop,
	}
	r.RegisterApiProvider(&ApiProvider{
		Api: "test-api",
		Stream: func(ctx context.Context, model *Model, prompt Context, options *StreamOptions) *AssistantMessageEventStream {
			s := NewAssistantMessageEventStream()
			go func() {
				s.Push(AssistantMessageEvent{Type: EventStart, Partial: finalMsg})
				s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: finalMsg})
				s.End(nil)
			}()
			return s
		},
		StreamSimple: func(ctx context.Context, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
			return NewAssistantMessageEventStream()
		},
	}, "")

	model := &Model{API: "test-api"}
	prompt := Context{Messages: []Message{NewUserMsg("hi", 1000)}}

	stream := Stream(context.Background(), r, model, prompt, nil)
	var events []AssistantMessageEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Type != EventDone {
		t.Errorf("expected done, got %s", events[1].Type)
	}
}

func TestComplete_WithProvider(t *testing.T) {
	r := NewRegistry()

	finalMsg := &AssistantMessage{
		Content:    []AssistantContent{NewTextContent("hello")},
		StopReason: StopReasonStop,
	}
	r.RegisterApiProvider(&ApiProvider{
		Api: "test-api",
		Stream: func(ctx context.Context, model *Model, prompt Context, options *StreamOptions) *AssistantMessageEventStream {
			s := NewAssistantMessageEventStream()
			go func() {
				s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: finalMsg})
				s.End(nil)
			}()
			return s
		},
		StreamSimple: func(ctx context.Context, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
			return NewAssistantMessageEventStream()
		},
	}, "")

	model := &Model{API: "test-api"}
	result := Complete(context.Background(), r, model, Context{}, nil)
	if result == nil {
		t.Fatal("expected result")
	}
	if result.Content[0].Text.Text != "hello" {
		t.Error("expected hello text")
	}
}

// --- Reasoning policy / thinking-off fallback (generic, provider-agnostic) ---

func TestResolveReasoning_AdaptiveOffDowngradesToMinimal(t *testing.T) {
	adaptive := &Model{Reasoning: true, AdaptiveThinking: true}
	opts := &SimpleStreamOptions{Reasoning: ThinkingOff}
	got := resolveReasoning(adaptive, opts)
	if got.Reasoning != ThinkingMinimal {
		t.Errorf("adaptive off => want minimal, got %q", got.Reasoning)
	}
	if opts.Reasoning != ThinkingOff {
		t.Error("caller options must not be mutated")
	}

	// Non-adaptive reasoning model: off passes through unchanged.
	plain := &Model{Reasoning: true}
	got2 := resolveReasoning(plain, &SimpleStreamOptions{Reasoning: ThinkingOff})
	if got2.Reasoning != ThinkingOff {
		t.Errorf("plain off => want off, got %q", got2.Reasoning)
	}

	// Non-off requests untouched.
	got3 := resolveReasoning(adaptive, &SimpleStreamOptions{Reasoning: ThinkingHigh})
	if got3.Reasoning != ThinkingHigh {
		t.Errorf("high => want high, got %q", got3.Reasoning)
	}
}

func TestIsThinkingOffUnsupportedError(t *testing.T) {
	yes := []string{
		`"thinking.type.disabled" is not supported for this model.`,
		"thinking is not supported for this model",
		"reasoning is not supported",
		"Unsupported parameter: thinking",
	}
	no := []string{"", "overloaded", "rate limit exceeded", "model not supported"}
	for _, m := range yes {
		if !isThinkingOffUnsupportedError(m) {
			t.Errorf("expected true for %q", m)
		}
	}
	for _, m := range no {
		if isThinkingOffUnsupportedError(m) {
			t.Errorf("expected false for %q", m)
		}
	}
}

func TestStreamSimple_ThinkingOffFallback(t *testing.T) {
	r := NewRegistry()
	var seen []ThinkingLevel
	r.RegisterApiProvider(&ApiProvider{
		Api: "fb-api",
		StreamSimple: func(ctx context.Context, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
			lvl := ThinkingLevel("")
			if options != nil {
				lvl = options.Reasoning
			}
			seen = append(seen, lvl)
			s := NewAssistantMessageEventStream()
			go func() {
				if lvl == ThinkingOff {
					// Reject thinking-off (model can't actually disable it).
					s.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &AssistantMessage{
						StopReason:   StopReasonError,
						ErrorMessage: `"thinking.type.disabled" is not supported for this model.`,
					}})
					s.End(nil)
					return
				}
				final := &AssistantMessage{Content: []AssistantContent{NewTextContent("ok")}, StopReason: StopReasonStop}
				s.Push(AssistantMessageEvent{Type: EventStart, Partial: final})
				s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: final})
				s.End(nil)
			}()
			return s
		},
	}, "")

	// Model believed to support disabling thinking (Reasoning, not adaptive) so
	// thinking-off passes through; the provider rejects it and the generic layer
	// retries with minimal.
	model := &Model{API: "fb-api", ID: "m", Reasoning: true}
	prompt := Context{Messages: []Message{NewUserMsg("hi", 1000)}}
	opts := &SimpleStreamOptions{Reasoning: ThinkingOff}

	stream := StreamSimple(context.Background(), r, model, prompt, opts)
	var events []AssistantMessageEvent
	for ev := range stream.Events {
		events = append(events, ev)
	}
	result := stream.Result()
	if result == nil || result.StopReason == StopReasonError {
		t.Fatalf("expected transparent recovery, got %+v", result)
	}
	for _, e := range events {
		if e.Type == EventError {
			t.Fatal("unexpected EventError — fallback should be transparent")
		}
	}
	if len(seen) != 2 || seen[0] != ThinkingOff || seen[1] != ThinkingMinimal {
		t.Fatalf("expected calls [off, minimal], got %v", seen)
	}
}

func TestStreamSimple_AdaptiveOffNeverSendsOff(t *testing.T) {
	r := NewRegistry()
	var seen []ThinkingLevel
	r.RegisterApiProvider(&ApiProvider{
		Api: "ad-api",
		StreamSimple: func(ctx context.Context, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
			if options != nil {
				seen = append(seen, options.Reasoning)
			}
			s := NewAssistantMessageEventStream()
			final := &AssistantMessage{Content: []AssistantContent{NewTextContent("ok")}, StopReason: StopReasonStop}
			go func() {
				s.Push(AssistantMessageEvent{Type: EventStart, Partial: final})
				s.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: final})
				s.End(nil)
			}()
			return s
		},
	}, "")

	model := &Model{API: "ad-api", ID: "m", Reasoning: true, AdaptiveThinking: true}
	prompt := Context{Messages: []Message{NewUserMsg("hi", 1000)}}
	CompleteSimple(context.Background(), r, model, prompt, &SimpleStreamOptions{Reasoning: ThinkingOff})
	if len(seen) != 1 || seen[0] != ThinkingMinimal {
		t.Fatalf("adaptive off must be resolved to minimal up front, got %v", seen)
	}
}
