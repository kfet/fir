package providers

// Regression coverage for the OpenAI Responses streaming aggregator's
// empty-message-item leak. The aggregator eagerly creates a
// `NewTextContent("")` on every `output_item.added` for type=message —
// see pkg/ai/providers/openai_responses_shared.go. When the model
// emits a message item that never receives any `output_text.delta`
// (e.g. a reasoning→function_call turn), that empty placeholder is
// left in the stored Content, where it breaks cross-provider replay
// (resume on Anthropic) and wastes bytes on subsequent OpenAI
// Responses calls. The end-of-stream pruner in
// pruneEmptyAssistantTextBlocks must remove it while keeping the
// reasoning block (with encrypted_content) and function_call intact.

import (
	"context"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestStreamOpenAIResponses_EmptyMessageItem(t *testing.T) {
	srv := mockSSEServer(t, "openai_responses_empty_message.sse")
	defer srv.Close()

	model := openaiResponsesTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("q", 0)}}

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	for range stream.Events {
	}
	got := stream.Result()
	if got == nil {
		t.Fatal("nil result")
	}

	// Walk stored content. After the empty-message-prune fix, no empty
	// text blocks should remain in stored Content. Reasoning + tool_use
	// items must survive intact.
	var sawReasoning, sawToolCall bool
	for i, c := range got.Content {
		if c.IsText() && strings.TrimSpace(c.Text.Text) == "" {
			t.Errorf("empty text block leaked into stored Content at index %d: %+v", i, c)
		}
		if c.IsThinking() && c.Thinking.ThinkingSignature != "" {
			sawReasoning = true
		}
		if c.IsToolCall() && c.ToolCall.Name == "read" {
			sawToolCall = true
		}
	}
	if !sawReasoning {
		t.Error("expected reasoning block with encrypted_content to survive")
	}
	if !sawToolCall {
		t.Error("expected function_call to survive")
	}
}
