package providers

// Defense-in-depth coverage for the empty-text invariant on OpenAI
// completions. Today the OpenAI completions streamer creates a text
// content block lazily — only when `delta.Content` is non-empty —
// so a stream of pure whitespace deltas should produce a single text
// block whose accumulated text is whitespace-only. The end-of-stream
// pruner removes that block, leaving only the tool_use. This pins
// both halves of the invariant: (a) whitespace-only deltas don't
// leak through to the stored Content, (b) tool_use blocks are not
// dropped by the pruner.

import (
	"context"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestStreamOpenAICompletions_WhitespaceOnlyTextPruned(t *testing.T) {
	srv := mockSSEServer(t, "openai_whitespace_only.sse")
	defer srv.Close()

	model := openaiTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("q", 0)}}

	stream := StreamOpenAICompletions(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	for range stream.Events {
	}
	got := stream.Result()
	if got == nil {
		t.Fatal("nil result")
	}

	var sawToolCall bool
	for i, c := range got.Content {
		if c.IsText() && strings.TrimSpace(c.Text.Text) == "" {
			t.Errorf("whitespace-only text leaked through pruner at index %d: %q", i, c.Text.Text)
		}
		if c.IsToolCall() && c.ToolCall.Name == "bash" {
			sawToolCall = true
		}
	}
	if !sawToolCall {
		t.Errorf("tool_use block dropped by pruner: %+v", got.Content)
	}
}
