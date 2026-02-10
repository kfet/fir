// Ported from: packages/ai/src/stream.ts
// Upstream hash: 1caadb2e
package ai

import (
	"context"
	"testing"
)

func TestStream_NoProvider(t *testing.T) {
	r := NewRegistry()
	model := &Model{Api: "nonexistent-api", ID: "m1", Provider: "p1"}
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
	model := &Model{Api: "nonexistent-api", ID: "m1", Provider: "p1"}
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

	model := &Model{Api: "test-api"}
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

	model := &Model{Api: "test-api"}
	result := Complete(context.Background(), r, model, Context{}, nil)
	if result == nil {
		t.Fatal("expected result")
	}
	if result.Content[0].Text.Text != "hello" {
		t.Error("expected hello text")
	}
}
