package providers

import (
	"context"
	"encoding/json"
	"net/http"
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
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			t0,
			ai.NewServerContent("server_tool_use", json.RawMessage(rawBlock), "[server tool: web_search]"),
			ai.NewServerContent("web_search_tool_result", json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"url":"https://x"}]}`), "URL: https://x"),
			t1,
			ai.NewToolCallContent("tu", "Bash", map[string]any{"cmd": "date"}),
		},
		StopReason: ai.StopReasonToolUse,
	}
	model := &ai.Model{ID: "claude-opus-4-7", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, Reasoning: true}
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
	// Expect: thinking, server_tool_use, web_search_tool_result, thinking,
	// tool_use — 5 blocks, no splice (server blocks already separate the
	// thinkings, and the server_tool_use is paired with its result so it is
	// not dropped as an orphan).
	if len(blocks) != 5 {
		t.Fatalf("want 5 wire blocks, got %d: %+v", len(blocks), blocks)
	}
	types := []string{}
	for _, b := range blocks {
		bt, _ := b["type"].(string)
		types = append(types, bt)
	}
	want := []string{"thinking", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
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
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			ai.NewServerContent("server_tool_use", json.RawMessage(`{"type":"server_tool_use","name":"web_search"}`), "[server tool: web_search]"),
			ai.NewToolCallContent("tu", "Bash", map[string]any{"cmd": "date"}),
		},
		StopReason: ai.StopReasonToolUse,
	}
	// Target: OpenAI model.
	target := &ai.Model{ID: "gpt-5", Provider: ai.ProviderOpenAI, API: ai.ApiOpenAICompletions}
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

// TestConvertAnthropic_OrphanedServerToolUse_Dropped is a live-confirmed
// regression for the 400 "`text_editor_code_execution` tool use with id ...
// was found without a corresponding `text_editor_code_execution_tool_result`
// block" (req_011Cbc9z9qxJmH2Yrg6vGVSa). A server_tool_use whose result block
// was never captured must be dropped on replay; a properly paired one must be
// kept verbatim.
func TestConvertAnthropic_OrphanedServerToolUse_Dropped(t *testing.T) {
	model := &ai.Model{ID: "claude-opus-4-8", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, Reasoning: true}

	mkServer := func(typ, raw string) ai.AssistantContent {
		return ai.NewServerContent(typ, json.RawMessage(raw), "")
	}
	wireFor := func(content ...ai.AssistantContent) []map[string]any {
		asst := ai.AssistantMessage{
			Role: ai.RoleAssistant, Provider: ai.ProviderAnthropic,
			API: ai.ApiAnthropicMessages, Model: "claude-opus-4-8",
			Content: content, StopReason: ai.StopReasonToolUse,
		}
		wire := convertAnthropicMessages([]ai.Message{ai.NewUserMsg("go", 0), ai.NewAssistantMsg(asst)}, model, false, ai.CacheNone)
		for _, m := range wire {
			if role, _ := m["role"].(string); role == "assistant" {
				b, _ := m["content"].([]map[string]any)
				return b
			}
		}
		return nil
	}
	hasType := func(blocks []map[string]any, typ string) bool {
		for _, b := range blocks {
			if t, _ := b["type"].(string); t == typ {
				return true
			}
		}
		return false
	}

	t.Run("orphaned server_tool_use is dropped", func(t *testing.T) {
		blocks := wireFor(
			ai.NewTextContent("editing"),
			mkServer("server_tool_use", `{"type":"server_tool_use","id":"srv_x","name":"text_editor_code_execution","input":{}}`),
			ai.NewToolCallContent("tu", "Bash", map[string]any{"cmd": "ls"}),
		)
		if hasType(blocks, "server_tool_use") {
			t.Fatalf("orphaned server_tool_use must be dropped, got %v", blocks)
		}
		if !hasType(blocks, "tool_use") {
			t.Fatalf("sibling client tool_use must survive, got %v", blocks)
		}
	})

	t.Run("paired server_tool_use is kept", func(t *testing.T) {
		blocks := wireFor(
			ai.NewTextContent("editing"),
			mkServer("server_tool_use", `{"type":"server_tool_use","id":"srv_y","name":"text_editor_code_execution","input":{}}`),
			mkServer("text_editor_code_execution_tool_result", `{"type":"text_editor_code_execution_tool_result","tool_use_id":"srv_y","content":{"stdout":"ok"}}`),
		)
		if !hasType(blocks, "server_tool_use") || !hasType(blocks, "text_editor_code_execution_tool_result") {
			t.Fatalf("paired server tool blocks must be kept, got %v", blocks)
		}
	})
}

// TestStreamAnthropic_CapturesTextEditorCodeExecResult verifies the stream
// parser stores a `text_editor_code_execution_tool_result` block as
// ServerContent (the root-cause fix: previously this result type was not in
// the parser allow-list, so it was dropped, orphaning its server_tool_use and
// triggering req_011Cbc9z9qxJmH2Yrg6vGVSa on the next turn).
func TestStreamAnthropic_CapturesTextEditorCodeExecResult(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text_editor_code_execution_tool_result","tool_use_id":"srv_z","content":{"type":"text_editor_code_execution_result","stdout":"hi"}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sse))
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.ID = "claude-opus-4-8"
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("edit it", 10)}}
	stream := StreamAnthropic(context.Background(), model, ctx, &ai.StreamOptions{APIKey: "k"})
	collectEvents(t, stream)
	res := stream.Result()
	if res == nil {
		t.Fatal("nil result")
	}
	var found bool
	for _, c := range res.Content {
		if c.IsServerContent() && c.Server.ProviderType == "text_editor_code_execution_tool_result" {
			found = true
		}
	}
	if !found {
		t.Fatalf("text_editor_code_execution_tool_result not captured as ServerContent; content=%+v", res.Content)
	}
}

// TestStreamAnthropic_CapturesUnknownServerBlock is the future-proofing
// guarantee: a server block type the parser has never seen (a hypothetical new
// Anthropic server tool) must still be captured verbatim as ServerContent via
// the default passthrough, not silently dropped. This is what prevents the
// "new server tool → dropped result → orphaned server_tool_use → 400" class
// from recurring whenever Anthropic ships a new tool.
func TestStreamAnthropic_CapturesUnknownServerBlock(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"future_widget_tool_result","tool_use_id":"srv_new","content":{"ok":true}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sse))
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.ID = "claude-opus-4-8"
	stream := StreamAnthropic(context.Background(), model, ai.Context{Messages: []ai.Message{ai.NewUserMsg("go", 5)}}, &ai.StreamOptions{APIKey: "k"})
	collectEvents(t, stream)
	res := stream.Result()
	var found bool
	for _, c := range res.Content {
		if c.IsServerContent() && c.Server.ProviderType == "future_widget_tool_result" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown server block type must be captured via default passthrough; content=%+v", res.Content)
	}
}
