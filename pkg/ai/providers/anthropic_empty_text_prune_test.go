package providers

// Regression coverage for req_011CaisANCxQkzQfGsdKwEcH (and its dual,
// req_011CaiKVdgvopStQzBuvt3kq).
//
// In interleaved-thinking turns Anthropic's stream sometimes opens a `text`
// content block that never receives a delta — e.g. a turn that goes straight
// from `thinking` to `tool_use`. Two production validators conflict on what
// happens next:
//
//   1. If we replay the assistant turn verbatim with the empty text block,
//      Anthropic returns 400 "messages: text content blocks must be
//      non-empty" (request id req_011CaiKVdgvopStQzBuvt3kq).
//   2. If we strip the empty text block in convertAnthropicMessages instead,
//      Anthropic returns 400 "thinking or redacted_thinking blocks in the
//      latest assistant message cannot be modified" (request id
//      req_011CaisANCxQkzQfGsdKwEcH) — because dropping any sibling of a
//      signed thinking block counts as a mutation.
//
// The only resolution is to never let an empty text block enter the stored
// AssistantMessage in the first place. The streaming aggregator therefore
// prunes empty/whitespace-only text blocks from `output.Content` before
// emitting EventDone. The stored message and every subsequent replay then
// match each other and Anthropic's expectation of the response shape.

import (
	"context"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// TestAnthropicStream_PrunesEmptyTextBlocksBesideThinking is the direct
// regression test for req_011CaisANCxQkzQfGsdKwEcH. The fixture streams a
// turn with [thinking(signed), text(no deltas), tool_use]. After streaming,
// the stored Content must contain no empty text block — otherwise the next
// turn will fail one of the two Anthropic validators above.
func TestAnthropicStream_PrunesEmptyTextBlocksBesideThinking(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_thinking_empty_text.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.ID = "claude-opus-4-7"
	model.Provider = ai.ProviderAnthropic
	model.API = ai.ApiAnthropicMessages

	stream := StreamAnthropic(context.Background(), model,
		ai.Context{Messages: []ai.Message{ai.NewUserMsg("q", 1000)}},
		&ai.StreamOptions{APIKey: "test-key"})
	for range stream.Events {
	}
	got := stream.Result()
	if got == nil {
		t.Fatal("nil result")
	}

	// Walk the stored content: must have signed thinking + tool_use, NO
	// empty text block in between.
	var sawThinking, sawToolUse bool
	for i, c := range got.Content {
		if c.IsText() && strings.TrimSpace(c.Text.Text) == "" {
			t.Errorf("empty text block leaked into stored Content at index %d: %+v", i, c)
		}
		if c.IsThinking() && c.Thinking.ThinkingSignature == "anth-sig-1" {
			sawThinking = true
		}
		if c.IsToolCall() && c.ToolCall.Name == "bash" {
			sawToolUse = true
		}
	}
	if !sawThinking {
		t.Error("expected signed thinking block to survive pruning")
	}
	if !sawToolUse {
		t.Error("expected tool_use block to survive pruning")
	}
	if len(got.Content) != 2 {
		t.Errorf("expected 2 stored blocks (thinking + tool_use), got %d: %+v", len(got.Content), got.Content)
	}

	// Replay through convertAnthropicMessages: the wire blocks must contain
	// the signature verbatim and zero empty text blocks.
	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(*got),
	}
	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) < 2 {
		t.Fatalf("expected ≥2 wire messages, got %d", len(result))
	}
	wire, ok := result[1]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("unexpected wire content type: %T", result[1]["content"])
	}
	for i, b := range wire {
		if b["type"] == "text" {
			if s, _ := b["text"].(string); strings.TrimSpace(s) == "" {
				t.Errorf("wire block %d is empty text after replay: %v", i, b)
			}
		}
	}
	thinkingFound := false
	for _, b := range wire {
		if b["type"] == "thinking" && b["signature"] == "anth-sig-1" {
			thinkingFound = true
		}
	}
	if !thinkingFound {
		t.Errorf("signed thinking block lost in replay: %v", wire)
	}
}

// TestAnthropicStream_DirectPrune unit-tests the pruner in isolation.
func TestAnthropicStream_DirectPrune(t *testing.T) {
	thinking := ai.NewThinkingContent("reasoning")
	thinking.Thinking.ThinkingSignature = "sig"
	in := []ai.AssistantContent{
		thinking,
		ai.NewTextContent(""),
		ai.NewTextContent("   "),
		ai.NewTextContent("real"),
		ai.NewToolCallContent("toolu_1", "bash", map[string]any{"x": 1}),
		ai.NewTextContent("\n\t"),
	}
	out := pruneEmptyAssistantTextBlocks(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 blocks (thinking, real text, tool_use), got %d: %+v", len(out), out)
	}
	if !out[0].IsThinking() || out[0].Thinking.ThinkingSignature != "sig" {
		t.Errorf("thinking block lost: %+v", out[0])
	}
	if !out[1].IsText() || out[1].Text.Text != "real" {
		t.Errorf("non-empty text block lost or mutated: %+v", out[1])
	}
	if !out[2].IsToolCall() {
		t.Errorf("tool_use block lost: %+v", out[2])
	}
}
