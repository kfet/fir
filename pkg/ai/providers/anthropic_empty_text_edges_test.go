package providers

// Additional edge-case coverage discovered while fixing
// req_011CaisANCxQkzQfGsdKwEcH. The streaming pruner must handle:
//
//   - Multiple signed thinking blocks in one turn (interleaved-thinking),
//     each surrounded by empty text siblings. This is the actual production
//     shape that triggered the recurring 400.
//   - redacted_thinking blocks paired with empty text. Same immutability
//     contract; the empty sibling must be pruned without touching the
//     redacted payload.
//   - Leading and trailing empty text blocks (before any thinking, after
//     the last tool_use). They contribute no semantics but break replay.
//   - Idempotency: pruner output must be a fixed point.
//   - Cross-provider replay: a *stored* (non-anthropic) assistant message
//     that already contains an empty text block must still convert cleanly
//     when the current target is Anthropic.
//   - Bedrock parity: Bedrock's streamer creates a text block on the
//     first `text_delta` regardless of its value, so a stream of
//     whitespace-only deltas accumulates into a whitespace-only text
//     block — same shape that breaks Anthropic replay. The end-of-stream
//     prune must remove it.

import (
	"context"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// Helper: collect block-type fingerprint (signature for thinking blocks).
type blockFP struct {
	Type      string
	Signature string
}

func fpOfStored(c []ai.AssistantContent) []blockFP {
	out := make([]blockFP, 0, len(c))
	for _, b := range c {
		switch {
		case b.IsText():
			out = append(out, blockFP{Type: "text"})
		case b.IsThinking():
			t := "thinking"
			if b.Thinking.Redacted {
				t = "redacted_thinking"
			}
			out = append(out, blockFP{Type: t, Signature: b.Thinking.ThinkingSignature})
		case b.IsToolCall():
			out = append(out, blockFP{Type: "tool_use"})
		}
	}
	return out
}

func fpOfWire(blocks []map[string]any) []blockFP {
	out := make([]blockFP, 0, len(blocks))
	for _, b := range blocks {
		t, _ := b["type"].(string)
		var sig string
		if t == "thinking" {
			sig, _ = b["signature"].(string)
		} else if t == "redacted_thinking" {
			sig, _ = b["data"].(string)
		}
		out = append(out, blockFP{Type: t, Signature: sig})
	}
	return out
}

func runStream(t *testing.T, fixture string) *ai.AssistantMessage {
	t.Helper()
	srv := mockSSEServer(t, fixture)
	t.Cleanup(srv.Close)
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
		t.Fatal("nil stream result")
	}
	return got
}

// Multiple interleaved-thinking blocks each surrounded by empty text. The
// stored Content must contain exactly: [thinking sig-1, tool_use,
// thinking sig-2, tool_use] — every empty/whitespace text sibling pruned,
// every signed thinking preserved verbatim.
func TestAnthropicStream_InterleavedThinking_AllEmptyTextsPruned(t *testing.T) {
	got := runStream(t, "anthropic_interleaved_thinking_empty_texts.sse")

	want := []blockFP{
		{Type: "thinking", Signature: "sig-1"},
		{Type: "tool_use"},
		{Type: "thinking", Signature: "sig-2"},
		{Type: "tool_use"},
	}
	gotFP := fpOfStored(got.Content)
	if len(gotFP) != len(want) {
		t.Fatalf("blocks mismatch: got %v, want %v", gotFP, want)
	}
	for i := range want {
		if gotFP[i] != want[i] {
			t.Errorf("block[%d]: got %+v, want %+v", i, gotFP[i], want[i])
		}
	}

	// And on replay through the wire converter the fingerprint must match.
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, MaxTokens: 64000, BaseURL: "https://api.anthropic.com"}
	wire := convertAnthropicMessages([]ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(*got),
	}, model, false, ai.CacheNone)
	if len(wire) < 2 {
		t.Fatalf("expected ≥2 wire messages")
	}
	wireFP := fpOfWire(wire[1]["content"].([]map[string]any))
	if len(wireFP) != len(want) {
		t.Fatalf("wire blocks mismatch: got %v, want %v", wireFP, want)
	}
	for i := range want {
		if wireFP[i] != want[i] {
			t.Errorf("wire block[%d]: got %+v, want %+v", i, wireFP[i], want[i])
		}
	}
}

// redacted_thinking + empty text sibling: the redacted payload survives
// verbatim, the empty text is pruned at source.
func TestAnthropicStream_RedactedThinking_EmptyTextPruned(t *testing.T) {
	got := runStream(t, "anthropic_redacted_thinking_empty_text.sse")

	want := []blockFP{
		{Type: "redacted_thinking", Signature: "REDACTED-PAYLOAD-XYZ"},
		{Type: "tool_use"},
	}
	gotFP := fpOfStored(got.Content)
	if len(gotFP) != len(want) {
		t.Fatalf("stored blocks: got %v, want %v", gotFP, want)
	}
	for i := range want {
		if gotFP[i] != want[i] {
			t.Errorf("stored block[%d]: got %+v, want %+v", i, gotFP[i], want[i])
		}
	}

	// Wire roundtrip: the redacted_thinking block uses "data", not
	// "signature". Ensure pruner did not touch it.
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, MaxTokens: 64000, BaseURL: "https://api.anthropic.com"}
	wire := convertAnthropicMessages([]ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(*got),
	}, model, false, ai.CacheNone)
	wireBlocks := wire[1]["content"].([]map[string]any)
	if wireBlocks[0]["type"] != "redacted_thinking" {
		t.Errorf("wire[0]: expected redacted_thinking, got %v", wireBlocks[0]["type"])
	}
	if wireBlocks[0]["data"] != "REDACTED-PAYLOAD-XYZ" {
		t.Errorf("redacted payload mutated: got %v", wireBlocks[0]["data"])
	}
}

// Pruner must be idempotent: f(f(x)) == f(x). Once empty texts are gone,
// a second pass changes nothing.
func TestPruneEmptyAssistantTextBlocks_Idempotent(t *testing.T) {
	thinking := ai.NewThinkingContent("x")
	thinking.Thinking.ThinkingSignature = "sig"
	cases := [][]ai.AssistantContent{
		nil,
		{},
		{ai.NewTextContent("a"), thinking, ai.NewToolCallContent("id", "n", nil)},
		{ai.NewTextContent(""), thinking, ai.NewTextContent("  "), ai.NewToolCallContent("id", "n", nil), ai.NewTextContent("\t\n")},
		{ai.NewTextContent(""), ai.NewTextContent(""), ai.NewTextContent("")},
	}
	for i, c := range cases {
		first := pruneEmptyAssistantTextBlocks(c)
		second := pruneEmptyAssistantTextBlocks(first)
		if len(first) != len(second) {
			t.Errorf("case %d: not idempotent — len(first)=%d, len(second)=%d", i, len(first), len(second))
			continue
		}
		for j := range first {
			if first[j].IsText() != second[j].IsText() ||
				first[j].IsThinking() != second[j].IsThinking() ||
				first[j].IsToolCall() != second[j].IsToolCall() {
				t.Errorf("case %d block %d: type drift between passes", i, j)
			}
		}
		// And no remaining empty text blocks anywhere.
		for j, b := range first {
			if b.IsText() && strings.TrimSpace(b.Text.Text) == "" {
				t.Errorf("case %d block %d: empty text leaked through pruner", i, j)
			}
		}
	}
}

// Cross-provider resume: a session whose JSONL was written *before* this
// fix can contain stored gemini/openai assistant messages with empty text
// blocks. After switching to Anthropic, convertAnthropicMessages must not
// emit those empty text blocks on the wire — otherwise the same recurring
// 400 will fire on the very first request after resume.
func TestAnthropic_ConvertMessages_DropsStoredEmptyTextFromOtherProvider(t *testing.T) {
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, MaxTokens: 64000, BaseURL: "https://api.anthropic.com"}

	// Stored gemini assistant: thinking-no-signature → text(empty) →
	// tool_use. After TransformMessages this becomes:
	// text(from-thinking) → text(empty) → tool_use; then
	// convertAnthropicMessages must drop the empty text.
	geminiMsg := ai.AssistantMessage{
		Role: ai.RoleAssistant, Provider: "google-gemini-cli", API: "google-gemini-cli", Model: "gemini-2.5-flash",
		StopReason: ai.StopReasonToolUse,
		Content: []ai.AssistantContent{
			ai.NewThinkingContent("ponder"),
			ai.NewTextContent(""),
			ai.NewToolCallContent("call_g", "bash", map[string]any{"x": 1}),
		},
	}
	msgs := []ai.Message{
		ai.NewUserMsg("hi", 1),
		ai.NewAssistantMsg(geminiMsg),
		ai.NewToolResultMsg(ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: "call_g", Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}}}),
	}
	wire := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(wire) < 2 {
		t.Fatalf("expected ≥2 wire messages, got %d", len(wire))
	}
	wireBlocks := wire[1]["content"].([]map[string]any)
	for i, b := range wireBlocks {
		if b["type"] == "text" {
			if s, _ := b["text"].(string); strings.TrimSpace(s) == "" {
				t.Errorf("wire[1].content[%d] is empty text after cross-provider replay: %v", i, b)
			}
		}
	}
	// Positive assertion: the gemini thinking text ("ponder") was
	// downgraded to a text block by TransformMessages and survived
	// conversion intact. Without that, a regression in the
	// cross-provider thinking-downgrade path would silently strip
	// content the agent depends on.
	var foundPonder, foundToolUse bool
	for _, b := range wireBlocks {
		if b["type"] == "text" && b["text"] == "ponder" {
			foundPonder = true
		}
		if b["type"] == "tool_use" && b["id"] == "call_g" {
			foundToolUse = true
		}
	}
	if !foundPonder {
		t.Errorf("gemini thinking text not downgraded to wire text block: %v", wireBlocks)
	}
	if !foundToolUse {
		t.Errorf("tool_use lost in cross-provider conversion: %v", wireBlocks)
	}
}

// Pin the Bedrock-side empty-text contract. Bedrock's streamer creates
// a text content block on the first `text_delta` regardless of its
// value, so a sequence of whitespace-only deltas accumulates into a
// whitespace-only text block — the same shape that breaks Anthropic
// replay. Bedrock-Claude inherits Anthropic's thinking-immutability
// constraint, so the streamer's end-of-stream prune call must remove
// that block. This unit test exercises the prune contract on a
// post-stream assistant message that mirrors what Bedrock would
// produce (signed thinking + accumulated whitespace text + tool_use):
// the prune must drop the whitespace block while preserving thinking
// signature and tool_use verbatim.
func TestBedrock_PostStreamPruneRemovesWhitespaceText(t *testing.T) {
	thinking := ai.NewThinkingContent("ponder")
	thinking.Thinking.ThinkingSignature = "bsig"
	whitespaceText := ai.NewTextContent(" \n\t  ")
	asst := ai.AssistantMessage{
		Role: ai.RoleAssistant, Provider: "bedrock", API: ai.ApiBedrockConverseStream, Model: "anthropic.claude-opus-4-7",
		StopReason: ai.StopReasonToolUse,
		Content: []ai.AssistantContent{
			thinking,
			whitespaceText,
			ai.NewToolCallContent("toolu_b1", "bash", map[string]any{"command": "ls"}),
		},
	}

	out := pruneEmptyAssistantTextBlocks(asst.Content)
	if len(out) != 2 {
		t.Fatalf("expected 2 blocks (thinking + tool_use) after prune, got %d: %+v", len(out), out)
	}
	if !out[0].IsThinking() || out[0].Thinking.ThinkingSignature != "bsig" {
		t.Errorf("thinking signature lost or block reordered: %+v", out[0])
	}
	if !out[1].IsToolCall() || out[1].ToolCall.Name != "bash" {
		t.Errorf("tool_use lost or mutated: %+v", out[1])
	}

	// Also pin: prune is a no-op on a clean message (no empty texts).
	clean := []ai.AssistantContent{
		thinking,
		ai.NewTextContent("real text"),
		ai.NewToolCallContent("toolu_b2", "bash", map[string]any{"command": "pwd"}),
	}
	cleanOut := pruneEmptyAssistantTextBlocks(clean)
	if len(cleanOut) != len(clean) {
		t.Errorf("pruner mutated a clean message: got %d, want %d", len(cleanOut), len(clean))
	}
}
