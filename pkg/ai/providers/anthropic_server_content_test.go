package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// Round-trip a stored ServerContent block back to the wire: the original
// content_block JSON must be emitted verbatim (post canonical JSON
// marshalling), with no signature/structure mutation along the way.
// Critical so that a server_tool_use block — which used to be flattened
// to an empty text placeholder, pruned, and turned into the adjacent-
// thinking 400 — now keeps its place in the wire and separates the
// thinking blocks structurally.
func TestConvertAnthropic_ServerContent_RoundTrip(t *testing.T) {
	rawBlock := `{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"hello"}}`
	t0 := ai.NewThinkingContent("")
	t0.Thinking.ThinkingSignature = strings.Repeat("a", 64)
	t1 := ai.NewThinkingContent("")
	t1.Thinking.ThinkingSignature = strings.Repeat("b", 64)
	asst := ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			t0,
			ai.NewServerContent("server_tool_use", json.RawMessage(rawBlock), "[server tool: web_search]"),
			t1,
			ai.NewToolCallContent("tu", "Bash", map[string]any{"cmd": "date"}),
		},
		StopReason: ai.StopReasonToolUse,
	}
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, Api: ai.ApiAnthropicMessages, Reasoning: true}
	wire := convertAnthropicMessages([]ai.Message{
		ai.NewUserMsg("go", 0),
		ai.NewAssistantMsg(asst),
	}, model, false, ai.CacheNone)

	// Find the assistant message in the wire.
	var blocks []map[string]any
	for _, m := range wire {
		if role, _ := m["role"].(string); role == "assistant" {
			blocks, _ = m["content"].([]map[string]any)
			break
		}
	}
	// Expect: thinking, server_tool_use, thinking, tool_use — 4 blocks,
	// no splice (the server_tool_use already separates the thinkings).
	if len(blocks) != 4 {
		t.Fatalf("want 4 wire blocks, got %d: %+v", len(blocks), blocks)
	}
	types := []string{}
	for _, b := range blocks {
		bt, _ := b["type"].(string)
		types = append(types, bt)
	}
	want := []string{"thinking", "server_tool_use", "thinking", "tool_use"}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("block[%d] type: want %q got %q (full: %v)", i, want[i], types[i], types)
		}
	}
	// And the server_tool_use block's fields must round-trip verbatim.
	got := blocks[1]
	if got["name"] != "web_search" {
		t.Errorf("server_tool_use name lost: %+v", got)
	}
	if got["id"] != "srv_1" {
		t.Errorf("server_tool_use id lost: %+v", got)
	}
	if input, _ := got["input"].(map[string]any); input["query"] != "hello" {
		t.Errorf("server_tool_use input lost: %+v", got)
	}
}

// Persistence: an AssistantMessage carrying ServerContent must round-trip
// through JSON encode/decode losslessly. The session jsonl uses this path.
func TestServerContent_JSONRoundTrip(t *testing.T) {
	original := ai.NewServerContent(
		"web_search_tool_result",
		json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srv_2","content":[{"url":"https://x"}]}`),
		"URL: https://x",
	)
	asst := ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{original},
	}
	body, err := json.Marshal(asst)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ai.AssistantMessage
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Content) != 1 {
		t.Fatalf("want 1 content block, got %d", len(back.Content))
	}
	c := back.Content[0]
	if !c.IsServerContent() {
		t.Fatalf("content not ServerContent after round-trip: %+v", c)
	}
	if c.Server.ProviderType != "web_search_tool_result" {
		t.Errorf("providerType: want web_search_tool_result, got %q", c.Server.ProviderType)
	}
	if c.Server.Display != "URL: https://x" {
		t.Errorf("display: want %q, got %q", "URL: https://x", c.Server.Display)
	}
	// Raw must round-trip byte-for-byte so wire replay is byte-stable.
	if string(c.Server.Raw) != `{"type":"web_search_tool_result","tool_use_id":"srv_2","content":[{"url":"https://x"}]}` {
		t.Errorf("Raw not preserved verbatim: %s", c.Server.Raw)
	}
}

// Cross-provider: ServerContent must be dropped (or downgraded to its
// Display text) when the message is replayed against a different provider.
// Keeping a server_tool_use block on a wire to OpenAI would 400.
func TestTransformMessages_ServerContent_CrossProvider(t *testing.T) {
	// Source: anthropic message with a server_tool_use block.
	asst := ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			ai.NewServerContent("server_tool_use", json.RawMessage(`{"type":"server_tool_use","name":"web_search"}`), "[server tool: web_search]"),
			ai.NewToolCallContent("tu", "Bash", map[string]any{"cmd": "date"}),
		},
		StopReason: ai.StopReasonToolUse,
	}
	// Target: OpenAI model.
	target := &ai.Model{ID: "gpt-5", Provider: ai.ProviderOpenAI, Api: ai.ApiOpenAICompletions}
	msgs := []ai.Message{
		ai.NewUserMsg("q", 0),
		ai.NewAssistantMsg(asst),
		ai.NewToolResultMsg(ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: "tu", ToolName: "Bash", Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}}}),
	}
	out := TransformMessages(msgs, target, nil)
	a := out[1].AsAssistant()
	if a == nil {
		t.Fatal("expected assistant message")
	}
	// Server block must NOT remain — it would 400 on OpenAI.
	for _, c := range a.Content {
		if c.IsServerContent() {
			t.Errorf("ServerContent leaked across providers: %+v", c)
		}
	}
	// But the Display text should be preserved as a text block so user
	// intent is not lost.
	foundDisplay := false
	for _, c := range a.Content {
		if c.IsText() && strings.Contains(c.Text.Text, "server tool") {
			foundDisplay = true
		}
	}
	if !foundDisplay {
		t.Errorf("Display text not preserved on cross-provider downgrade: %+v", a.Content)
	}
}
