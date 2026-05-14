package providers

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// Reproduces the wire shape from session
// 2026-05-13T22-15-58-493498Z_d186b377-5e06-4aee-b9d1-08772452ba2d.jsonl
// where assistant message 5d052ce2-ef7 ended up with two adjacent signed
// thinking blocks after fir's streamer flattened a server_tool_use block
// to an empty text placeholder and the end-of-stream pruner dropped it.
// Anthropic's input validator then 400'd with the misleading
// "thinking blocks cannot be modified" — request id
// req_011Cb1vcfcbfqsJWGM7KfmyT (and many siblings).
//
// The fix splices a synthetic non-thinking text block between the
// adjacent thinkings at wire build time, which Anthropic accepts.
func TestConvertAnthropic_AdjacentThinkingBlocks_SpliceSeparator(t *testing.T) {
	t0 := ai.NewThinkingContent("")
	t0.Thinking.ThinkingSignature = strings.Repeat("s0", 50)
	t1 := ai.NewThinkingContent("")
	t1.Thinking.ThinkingSignature = strings.Repeat("s1", 50)
	t2 := ai.NewThinkingContent("")
	t2.Thinking.ThinkingSignature = strings.Repeat("s2", 50)
	asst := ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			t0, t1, // adjacent — must be separated
			ai.NewTextContent("URL list from web search"),
			t2,
			ai.NewToolCallContent("toolu_x", "Bash", map[string]any{"cmd": "date"}),
		},
		StopReason: ai.StopReasonToolUse,
	}
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages, Reasoning: true}
	wire := convertAnthropicMessages([]ai.Message{
		ai.NewUserMsg("go", 0),
		ai.NewAssistantMsg(asst),
	}, model, false, ai.CacheNone)

	if len(wire) < 2 {
		t.Fatalf("want >=2 wire messages, got %d", len(wire))
	}
	// Find the assistant message (TransformMessages may inject a
	// synthetic tool_result after it for the orphan tool_use).
	var blocks []map[string]any
	for _, m := range wire {
		if role, _ := m["role"].(string); role == "assistant" {
			blocks, _ = m["content"].([]map[string]any)
			break
		}
	}
	if len(blocks) == 0 {
		t.Fatal("assistant content empty")
	}
	// Walk and assert no adjacency.
	isThinking := func(b map[string]any) bool {
		t, _ := b["type"].(string)
		return t == "thinking" || t == "redacted_thinking"
	}
	for i := 1; i < len(blocks); i++ {
		if isThinking(blocks[i-1]) && isThinking(blocks[i]) {
			t.Fatalf("adjacent thinking blocks at idx %d-%d still present: %+v", i-1, i, blocks)
		}
	}
	// And the three thinking signatures must be preserved verbatim — the
	// fix may not drop or mutate any signed block.
	gotSigs := []string{}
	for _, b := range blocks {
		if isThinking(b) {
			if sig, _ := b["signature"].(string); sig != "" {
				gotSigs = append(gotSigs, sig)
			}
		}
	}
	wantSigs := []string{t0.Thinking.ThinkingSignature, t1.Thinking.ThinkingSignature, t2.Thinking.ThinkingSignature}
	if len(gotSigs) != len(wantSigs) {
		t.Fatalf("want %d thinking sigs on wire, got %d", len(wantSigs), len(gotSigs))
	}
	for i := range wantSigs {
		if gotSigs[i] != wantSigs[i] {
			t.Errorf("sig[%d] mutated: want %q got %q", i, wantSigs[i], gotSigs[i])
		}
	}
}

// No-op case: a single thinking block + non-thinking content should not be
// touched by the adjacency guard.
func TestConvertAnthropic_AdjacentThinkingBlocks_NoOpWhenSafe(t *testing.T) {
	thinking := ai.NewThinkingContent("")
	thinking.Thinking.ThinkingSignature = "sig"
	asst := ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			thinking,
			ai.NewToolCallContent("toolu_x", "Bash", map[string]any{"cmd": "date"}),
		},
		StopReason: ai.StopReasonToolUse,
	}
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages, Reasoning: true}
	wire := convertAnthropicMessages([]ai.Message{
		ai.NewUserMsg("go", 0),
		ai.NewAssistantMsg(asst),
	}, model, false, ai.CacheNone)
	var blocks []map[string]any
	for _, m := range wire {
		if role, _ := m["role"].(string); role == "assistant" {
			blocks, _ = m["content"].([]map[string]any)
			break
		}
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 wire blocks (thinking + tool_use), got %d: %+v", len(blocks), blocks)
	}
	for _, b := range blocks {
		if txt, _ := b["text"].(string); strings.Contains(txt, "omitted on replay") {
			t.Errorf("adjacency guard fired when it shouldn't: %+v", blocks)
		}
	}
}

// separateAdjacentThinkingBlocks unit test — pure function.
func TestSeparateAdjacentThinkingBlocks(t *testing.T) {
	cases := []struct {
		name       string
		in         []map[string]any
		wantLen    int
		wantSplice bool
	}{
		{
			"no thinking",
			[]map[string]any{{"type": "text", "text": "x"}},
			1, false,
		},
		{
			"single thinking",
			[]map[string]any{{"type": "thinking", "signature": "s"}, {"type": "tool_use"}},
			2, false,
		},
		{
			"two adjacent",
			[]map[string]any{{"type": "thinking", "signature": "a"}, {"type": "thinking", "signature": "b"}, {"type": "tool_use"}},
			4, true,
		},
		{
			"three adjacent",
			[]map[string]any{
				{"type": "thinking", "signature": "a"},
				{"type": "thinking", "signature": "b"},
				{"type": "thinking", "signature": "c"},
				{"type": "tool_use"},
			},
			6, true,
		},
		{
			"separated by text — no splice",
			[]map[string]any{
				{"type": "thinking", "signature": "a"},
				{"type": "text", "text": "x"},
				{"type": "thinking", "signature": "b"},
				{"type": "tool_use"},
			},
			4, false,
		},
		{
			"redacted_thinking counts as thinking",
			[]map[string]any{
				{"type": "thinking", "signature": "a"},
				{"type": "redacted_thinking", "data": "d"},
				{"type": "tool_use"},
			},
			4, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := separateAdjacentThinkingBlocks(tc.in)
			if len(out) != tc.wantLen {
				t.Fatalf("len: want %d, got %d (%+v)", tc.wantLen, len(out), out)
			}
			spliced := false
			for _, b := range out {
				if txt, _ := b["text"].(string); strings.Contains(txt, "omitted on replay") {
					spliced = true
				}
			}
			if spliced != tc.wantSplice {
				t.Errorf("splice: want %v got %v (%+v)", tc.wantSplice, spliced, out)
			}
		})
	}
}
