// Package providers — coverage for the Anthropic thinking-block replay
// invariants.
//
// Anthropic's Messages API enforces two distinct rules on the latest
// assistant turn that the converter must satisfy together:
//
//  1. Signed thinking blocks (and redacted_thinking blocks) must be replayed
//     verbatim — same type, same signature/data, same thinking text. Mutating
//     a thinking block triggers
//     "400 messages.N.content.M: thinking or redacted_thinking blocks in the
//     latest assistant message cannot be modified".
//  2. Text content blocks must be non-empty. Empty/whitespace-only text
//     blocks trigger "400 messages: text content blocks must be non-empty"
//     (request id req_011CaiKVdgvopStQzBuvt3kq) — so the converter MUST drop
//     them, even when they sit beside a signed thinking block.
//
// These tests pin both rules from several angles so future refactors cannot
// silently re-introduce either bug.
package providers

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// extractThinkingBlocks returns the raw map blocks of type "thinking" or
// "redacted_thinking" in order from a converted assistant message.
func extractThinkingBlocks(blocks []map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range blocks {
		t, _ := b["type"].(string)
		if t == "thinking" || t == "redacted_thinking" {
			out = append(out, b)
		}
	}
	return out
}

// thinkingFingerprint reduces a block sequence to the minimum that Anthropic
// validates: ordered (type, signature/data, thinking-text) tuples for every
// thinking/redacted_thinking block, plus the *positions* of those blocks in
// the overall sequence (so reordering a sibling is also detected).
type thinkingFingerprint struct {
	BlockTypes      []string         // type for every block (text, thinking, tool_use, …)
	Thinkings       []map[string]any // payload for thinking blocks only
	TotalBlockCount int
}

func fingerprintAssistantBlocks(blocks []map[string]any) thinkingFingerprint {
	fp := thinkingFingerprint{TotalBlockCount: len(blocks)}
	for _, b := range blocks {
		t, _ := b["type"].(string)
		fp.BlockTypes = append(fp.BlockTypes, t)
		if t == "thinking" {
			fp.Thinkings = append(fp.Thinkings, map[string]any{
				"type":      "thinking",
				"thinking":  b["thinking"],
				"signature": b["signature"],
			})
		} else if t == "redacted_thinking" {
			fp.Thinkings = append(fp.Thinkings, map[string]any{
				"type": "redacted_thinking",
				"data": b["data"],
			})
		}
	}
	return fp
}

// expectedFingerprintFromContent builds the fingerprint we expect the wire
// blocks to satisfy, given the original ai.AssistantContent slice. Empty /
// whitespace-only text blocks are dropped: Anthropic rejects them with
// "messages: text content blocks must be non-empty", so the converter must
// strip them — even when a signed thinking block is present in the same
// turn. The fingerprint reflects that contract.
func expectedFingerprintFromContent(cs []ai.AssistantContent) thinkingFingerprint {
	fp := thinkingFingerprint{}
	for _, c := range cs {
		switch {
		case c.IsText():
			if strings.TrimSpace(c.Text.Text) == "" {
				continue
			}
			fp.BlockTypes = append(fp.BlockTypes, "text")
		case c.IsThinking():
			if c.Thinking.Redacted {
				fp.BlockTypes = append(fp.BlockTypes, "redacted_thinking")
				fp.Thinkings = append(fp.Thinkings, map[string]any{
					"type": "redacted_thinking",
					"data": c.Thinking.ThinkingSignature,
				})
			} else {
				fp.BlockTypes = append(fp.BlockTypes, "thinking")
				fp.Thinkings = append(fp.Thinkings, map[string]any{
					"type":      "thinking",
					"thinking":  c.Thinking.Thinking,
					"signature": c.Thinking.ThinkingSignature,
				})
			}
		case c.IsToolCall():
			fp.BlockTypes = append(fp.BlockTypes, "tool_use")
		}
	}
	// Mirror separateAdjacentThinkingBlocks: a synthetic "text" block is
	// spliced between any two adjacent thinking/redacted_thinking entries
	// before the wire goes out. The fingerprint must reflect that or it
	// will spuriously diverge from the converter output.
	isThinkingType := func(t string) bool { return t == "thinking" || t == "redacted_thinking" }
	if len(fp.BlockTypes) > 1 {
		spliced := make([]string, 0, len(fp.BlockTypes)+2)
		for i, t := range fp.BlockTypes {
			if i > 0 && isThinkingType(fp.BlockTypes[i-1]) && isThinkingType(t) {
				spliced = append(spliced, "text")
			}
			spliced = append(spliced, t)
		}
		fp.BlockTypes = spliced
	}
	fp.TotalBlockCount = len(fp.BlockTypes)
	return fp
}

// assertThinkingInvariant checks that converting the assistant message keeps
// the thinking-block fingerprint identical to the original Content slice's
// fingerprint. Used as a generic "thinking-must-not-be-mutated" assertion.
func assertThinkingInvariant(t *testing.T, am ai.AssistantMessage, model *ai.Model) []map[string]any {
	t.Helper()
	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(am),
	}
	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result))
	}
	blocks, ok := result[1]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("assistant content not []map[string]any: %T", result[1]["content"])
	}

	got := fingerprintAssistantBlocks(blocks)
	want := expectedFingerprintFromContent(am.Content)

	if !reflect.DeepEqual(got.BlockTypes, want.BlockTypes) {
		t.Errorf("block-type sequence mutated:\n  got:  %v\n  want: %v", got.BlockTypes, want.BlockTypes)
	}
	if !reflect.DeepEqual(got.Thinkings, want.Thinkings) {
		t.Errorf("thinking payloads mutated:\n  got:  %#v\n  want: %#v", got.Thinkings, want.Thinkings)
	}
	if got.TotalBlockCount != want.TotalBlockCount {
		t.Errorf("block count changed: got %d, want %d", got.TotalBlockCount, want.TotalBlockCount)
	}
	return blocks
}

func anthropicSameModelModel() *ai.Model {
	return &ai.Model{
		ID:        "claude-opus-4-7",
		Provider:  ai.ProviderAnthropic,
		Api:       ai.ApiAnthropicMessages,
		BaseURL:   "https://api.anthropic.com",
		MaxTokens: 64000,
	}
}

func mkSignedThinking(text, sig string) ai.AssistantContent {
	c := ai.NewThinkingContent(text)
	c.Thinking.ThinkingSignature = sig
	return c
}

func mkRedactedThinking(payload string) ai.AssistantContent {
	c := ai.NewThinkingContent("")
	c.Thinking.Redacted = true
	c.Thinking.ThinkingSignature = payload
	return c
}

func mkAssistant(model *ai.Model, content ...ai.AssistantContent) ai.AssistantMessage {
	return ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: model.Provider,
		Api:      model.Api,
		Model:    model.ID,
		Content:  content,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Single-shot block sequences (regression matrix)
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicThinkingInvariant_BlockSequences(t *testing.T) {
	model := anthropicSameModelModel()

	cases := []struct {
		name    string
		content []ai.AssistantContent
	}{
		{
			name: "thinking_only",
			content: []ai.AssistantContent{
				mkSignedThinking("Let me reason.", "sig-1"),
			},
		},
		{
			name: "thinking_then_text",
			content: []ai.AssistantContent{
				mkSignedThinking("reasoning", "sig-A"),
				ai.NewTextContent("Final answer."),
			},
		},
		{
			name: "thinking_empty_text_tool_use",
			content: []ai.AssistantContent{
				mkSignedThinking("reasoning", "sig-B"),
				ai.NewTextContent(""),
				ai.NewToolCallContent("tc1", "echo", map[string]any{"x": 1}),
			},
		},
		{
			name: "thinking_whitespace_text_tool_use",
			content: []ai.AssistantContent{
				mkSignedThinking("reasoning", "sig-C"),
				ai.NewTextContent("  \n  "),
				ai.NewToolCallContent("tc1", "echo", map[string]any{"x": 1}),
			},
		},
		{
			name: "interleaved_thinking_tool_use_x3",
			content: []ai.AssistantContent{
				mkSignedThinking("step 1", "sig-1"),
				ai.NewToolCallContent("tc1", "search", map[string]any{"q": "a"}),
				mkSignedThinking("step 2", "sig-2"),
				ai.NewToolCallContent("tc2", "search", map[string]any{"q": "b"}),
				mkSignedThinking("step 3", "sig-3"),
				ai.NewToolCallContent("tc3", "search", map[string]any{"q": "c"}),
			},
		},
		{
			name: "interleaved_with_empty_text_between_thoughts",
			content: []ai.AssistantContent{
				mkSignedThinking("first", "sig-1"),
				ai.NewTextContent(""),
				mkSignedThinking("second", "sig-2"),
				ai.NewTextContent(""),
				ai.NewToolCallContent("tc1", "echo", map[string]any{"a": 1}),
			},
		},
		{
			name: "redacted_only",
			content: []ai.AssistantContent{
				mkRedactedThinking("EncryptedBlobAAA=="),
			},
		},
		{
			name: "redacted_then_signed_then_tool_use",
			content: []ai.AssistantContent{
				mkRedactedThinking("EncryptedBlobBBB=="),
				mkSignedThinking("more reasoning", "sig-Z"),
				ai.NewToolCallContent("t", "do", map[string]any{}),
			},
		},
		{
			name: "redacted_with_empty_text_sibling",
			content: []ai.AssistantContent{
				mkRedactedThinking("EncryptedBlobCCC=="),
				ai.NewTextContent(""),
				ai.NewToolCallContent("tc1", "echo", map[string]any{"x": 1}),
			},
		},
		{
			name: "many_blocks_emulating_observed_400",
			// Mirrors `messages.1.content.9` shape: 10 blocks, last is thinking.
			content: []ai.AssistantContent{
				mkSignedThinking("a", "sa"),
				ai.NewToolCallContent("t1", "f", map[string]any{}),
				mkSignedThinking("b", "sb"),
				ai.NewTextContent(""),
				ai.NewToolCallContent("t2", "f", map[string]any{}),
				mkSignedThinking("c", "sc"),
				ai.NewToolCallContent("t3", "f", map[string]any{}),
				ai.NewTextContent(" "),
				ai.NewToolCallContent("t4", "f", map[string]any{}),
				mkSignedThinking("d", "sd"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am := mkAssistant(model, tc.content...)
			assertThinkingInvariant(t, am, model)
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Property-style: multi-turn conversation, thinking always survives
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicThinkingInvariant_MultiTurnConversation(t *testing.T) {
	model := anthropicSameModelModel()

	// Build a 4-turn conversation with thinking on every assistant turn.
	turn := func(thoughtSig, thoughtText, toolID string) ai.AssistantMessage {
		return mkAssistant(model,
			mkSignedThinking(thoughtText, thoughtSig),
			ai.NewTextContent(""), // empty text — must survive
			ai.NewToolCallContent(toolID, "echo", map[string]any{"k": toolID}),
		)
	}

	msgs := []ai.Message{
		ai.NewUserMsg("start", 1000),
		ai.NewAssistantMsg(turn("s1", "t1", "id1")),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role: ai.RoleToolResult, ToolCallID: "id1", ToolName: "echo",
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
		}),
		ai.NewAssistantMsg(turn("s2", "t2", "id2")),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role: ai.RoleToolResult, ToolCallID: "id2", ToolName: "echo",
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
		}),
		ai.NewAssistantMsg(turn("s3", "t3", "id3")),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role: ai.RoleToolResult, ToolCallID: "id3", ToolName: "echo",
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
		}),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	// Every assistant message must keep its 3 blocks in order with thinking intact.
	asstSeen := 0
	wantSigs := []string{"s1", "s2", "s3"}
	for _, m := range result {
		if m["role"] != "assistant" {
			continue
		}
		blocks := m["content"].([]map[string]any)
		if len(blocks) != 2 {
			t.Errorf("turn %d: expected 2 blocks (thinking, tool_use — empty text dropped), got %d (%v)", asstSeen, len(blocks), blocks)
			asstSeen++
			continue
		}
		if blocks[0]["type"] != "thinking" {
			t.Errorf("turn %d: block 0 should be thinking, got %v", asstSeen, blocks[0]["type"])
		}
		if blocks[1]["type"] != "tool_use" {
			t.Errorf("turn %d: block 1 should be tool_use (empty text dropped), got %v", asstSeen, blocks[1]["type"])
		}
		if blocks[0]["signature"] != wantSigs[asstSeen] {
			t.Errorf("turn %d: signature mutated: got %v want %v", asstSeen, blocks[0]["signature"], wantSigs[asstSeen])
		}
		asstSeen++
	}
	if asstSeen != 3 {
		t.Errorf("expected 3 assistant turns, got %d", asstSeen)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Cross-model (same provider, different model ID) — TransformMessages must not
// downgrade signed thinking when isSameProvider holds.
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicThinkingInvariant_SameProviderDifferentModelID(t *testing.T) {
	model := anthropicSameModelModel() // ID: claude-opus-4-7

	// Stored assistant turn used a dated alias of the same model.
	stored := ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7-20250514", // different ID, same provider+api
		Content: []ai.AssistantContent{
			mkSignedThinking("reasoning", "sig-XYZ"),
			ai.NewTextContent(""),
			ai.NewToolCallContent("tcA", "echo", map[string]any{"k": 1}),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(stored),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role: ai.RoleToolResult, ToolCallID: "tcA", ToolName: "echo",
			Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
		}),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result))
	}
	blocks := result[1]["content"].([]map[string]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (thinking, tool_use — empty text dropped), got %d: %v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "thinking" || blocks[0]["signature"] != "sig-XYZ" {
		t.Errorf("thinking block was downgraded: %v", blocks[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool-call argument key ordering — wire bytes must be stable across replay.
//
// `tool_use.input` is a JSON object. Go's json.Marshal sorts map keys, so any
// downstream mutation of c.ToolCall.Arguments must preserve the same canonical
// form. This test pins that JSON.Marshal of the converted block produces the
// same bytes when called twice (regression guard against insertion-ordered
// arg structures sneaking in).
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicThinkingInvariant_ToolUseInputBytesStable(t *testing.T) {
	model := anthropicSameModelModel()

	args := map[string]any{
		"z": 1, "a": "x", "m": []any{1, 2, 3},
		"nested": map[string]any{"b": 2, "a": 1},
	}
	am := mkAssistant(model,
		mkSignedThinking("reasoning", "sig-1"),
		ai.NewToolCallContent("tc1", "lookup", args),
	)

	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(am),
	}

	encode := func() string {
		result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
		blocks := result[1]["content"].([]map[string]any)
		var toolUse map[string]any
		for _, b := range blocks {
			if b["type"] == "tool_use" {
				toolUse = b
				break
			}
		}
		if toolUse == nil {
			t.Fatalf("no tool_use block found")
		}
		raw, err := json.Marshal(toolUse["input"])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}

	first := encode()
	second := encode()
	if first != second {
		t.Errorf("tool_use.input not byte-stable across replays:\n  first:  %s\n  second: %s", first, second)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end: stream Anthropic SSE → store result → replay through convert.
// The thinking blocks emitted by the stream must round-trip into wire form
// byte-equal to what would have been sent in the original response.
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicThinkingInvariant_StreamingThenReplayRoundtrip(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_thinking.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.ID = "claude-opus-4-7"
	model.Provider = ai.ProviderAnthropic
	model.Api = ai.ApiAnthropicMessages

	stream := StreamAnthropic(context.Background(), model,
		ai.Context{Messages: []ai.Message{ai.NewUserMsg("q", 1000)}},
		&ai.StreamOptions{ApiKey: "test-key"})
	for range stream.Events {
	}
	got := stream.Result()
	if got == nil {
		t.Fatal("nil result")
	}

	// Replay the streamed assistant message back through convertAnthropicMessages.
	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(*got),
	}
	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result))
	}
	blocks := result[1]["content"].([]map[string]any)

	// Must contain at least one thinking block with the original signature.
	thinkingBlocks := extractThinkingBlocks(blocks)
	if len(thinkingBlocks) == 0 {
		t.Fatalf("thinking block lost in roundtrip: %v", blocks)
	}
	if thinkingBlocks[0]["signature"] != "sig123" {
		t.Errorf("signature mutated in roundtrip: got %v, want sig123", thinkingBlocks[0]["signature"])
	}
	if thinkingBlocks[0]["thinking"] != "Let me think about this..." {
		t.Errorf("thinking text mutated: got %v", thinkingBlocks[0]["thinking"])
	}

	// And the streamed empty-text-then-text block must have survived the
	// conversion (text="The answer is 42." since the streamer accumulates it).
	hasText := false
	for _, b := range blocks {
		if b["type"] == "text" && b["text"] == "The answer is 42." {
			hasText = true
		}
	}
	if !hasText {
		t.Errorf("expected text block from streamed response, got blocks: %v", blocks)
	}
}

func TestAnthropicThinkingInvariant_RedactedStreamingRoundtrip(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_redacted_thinking.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.Provider = ai.ProviderAnthropic
	model.Api = ai.ApiAnthropicMessages

	stream := StreamAnthropic(context.Background(), model,
		ai.Context{Messages: []ai.Message{ai.NewUserMsg("q", 1000)}},
		&ai.StreamOptions{ApiKey: "test-key"})
	for range stream.Events {
	}
	got := stream.Result()
	if got == nil {
		t.Fatal("nil result")
	}

	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(*got),
	}
	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result))
	}
	blocks := result[1]["content"].([]map[string]any)

	thinkingBlocks := extractThinkingBlocks(blocks)
	if len(thinkingBlocks) == 0 || thinkingBlocks[0]["type"] != "redacted_thinking" {
		t.Fatalf("redacted_thinking block lost in roundtrip: %v", blocks)
	}
	if thinkingBlocks[0]["data"] == nil || thinkingBlocks[0]["data"] == "" {
		t.Errorf("redacted_thinking data missing: %v", thinkingBlocks[0])
	}
	// Must NOT contain a "thinking" field (Anthropic rejects that on redacted).
	if _, ok := thinkingBlocks[0]["thinking"]; ok {
		t.Errorf("redacted_thinking must not carry 'thinking' field: %v", thinkingBlocks[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Negative test: when no thinking block is present, the empty-text filter still
// applies (preserves the size-vs-quality trade-off for non-thinking turns).
// ─────────────────────────────────────────────────────────────────────────────

func TestAnthropicThinkingInvariant_EmptyTextFilteredWhenNoThinking(t *testing.T) {
	model := anthropicSameModelModel()
	am := mkAssistant(model,
		ai.NewTextContent(""),
		ai.NewTextContent("hello"),
		ai.NewTextContent("   "),
	)
	msgs := []ai.Message{
		ai.NewUserMsg("q", 1000),
		ai.NewAssistantMsg(am),
	}
	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	blocks := result[1]["content"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["text"] != "hello" {
		t.Errorf("expected only the non-empty text to survive, got %v", blocks)
	}
}
