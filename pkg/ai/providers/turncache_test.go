package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func ttlOf(block map[string]any) string {
	cc, ok := block["cache_control"].(map[string]any)
	if !ok {
		return ""
	}
	if ttl, _ := cc["ttl"].(string); ttl != "" {
		return ttl
	}
	return "5m"
}

// breakpointTTLs lists the TTL of every breakpoint in request order.
func breakpointTTLs(params map[string]any) []string {
	var out []string
	if sys, ok := params["system"].([]map[string]any); ok {
		for _, b := range sys {
			if t := ttlOf(b); t != "" {
				out = append(out, "sys:"+t)
			}
		}
	}
	for i, m := range params["messages"].([]map[string]any) {
		for _, b := range m["content"].([]map[string]any) {
			if t := ttlOf(b); t != "" {
				out = append(out, string(rune('0'+i))+":"+t)
			}
		}
	}
	return out
}

func toolTurn(id string, ts int64) []ai.Message {
	return []ai.Message{
		ai.NewAssistantMsg(ai.AssistantMessage{Content: []ai.AssistantContent{ai.NewToolCallContent(id, "Bash", map[string]any{"cmd": "date"})}, StopReason: ai.StopReasonToolUse, Timestamp: ts}),
		ai.NewToolResultMsg(ai.ToolResultMessage{Role: ai.RoleToolResult, ToolCallID: id, ToolName: "Bash", Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}}}),
	}
}

func withTurnCache(t *testing.T, on bool) {
	t.Helper()
	t.Setenv(EnvTurnCache1h, "")
	prev := turnLongCache.Load()
	SetTurnLongCache(on)
	t.Cleanup(func() { SetTurnLongCache(prev) })
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTurnLongCache_Layout(t *testing.T) {
	withTurnCache(t, true)
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	opts := &ai.StreamOptions{CacheRetention: ai.CacheShort, SessionID: "s1"}
	stable := "stable" + ai.SystemPromptCacheBoundary + "volatile"

	// First request of the first turn: the prompt is both anchor and tail.
	ctx := ai.Context{SystemPrompt: stable, Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}
	got := breakpointTTLs(buildAnthropicParams(model, ctx, false, opts))
	if want := []string{"sys:1h", "sys:1h", "0:1h"}; !equalStrings(got, want) {
		t.Fatalf("first request: got %v want %v", got, want)
	}

	// Later request in the same turn: anchor stays 1h, tail is 5m.
	ctx.Messages = append(ctx.Messages, toolTurn("t1", 2)...)
	got = breakpointTTLs(buildAnthropicParams(model, ctx, false, opts))
	if want := []string{"sys:1h", "sys:1h", "0:1h", "2:5m"}; !equalStrings(got, want) {
		t.Fatalf("tool loop: got %v want %v", got, want)
	}

	// Next turn: previous anchor kept (lookback-proof read), new anchor 1h,
	// volatile system breakpoint trimmed to stay within four.
	ctx.Messages = append(ctx.Messages,
		ai.NewAssistantMsg(ai.AssistantMessage{Content: []ai.AssistantContent{ai.NewTextContent("done")}, StopReason: ai.StopReasonStop, Timestamp: 3}),
		ai.NewUserMsg("next", 4))
	ctx.Messages = append(ctx.Messages, toolTurn("t2", 5)...)
	got = breakpointTTLs(buildAnthropicParams(model, ctx, false, opts))
	if want := []string{"sys:1h", "0:1h", "4:1h", "6:5m"}; !equalStrings(got, want) {
		t.Fatalf("second turn: got %v want %v", got, want)
	}
}

func TestTurnLongCache_Disabled(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{SystemPrompt: "s", Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}
	ctx.Messages = append(ctx.Messages, toolTurn("t1", 2)...)
	want := []string{"sys:5m", "2:5m"}

	withTurnCache(t, false)
	if got := breakpointTTLs(buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort, SessionID: "s1"})); !equalStrings(got, want) {
		t.Fatalf("off: got %v want %v", got, want)
	}

	SetTurnLongCache(true)
	if got := breakpointTTLs(buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort})); !equalStrings(got, want) {
		t.Fatalf("no session id (one-off call): got %v want %v", got, want)
	}

	t.Setenv(EnvTurnCache1h, "0")
	if got := breakpointTTLs(buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort, SessionID: "s1"})); !equalStrings(got, want) {
		t.Fatalf("env override: got %v want %v", got, want)
	}

	t.Setenv(EnvTurnCache1h, "")
	noLong := false
	m2 := *model
	m2.Compat = &ai.AnthropicMessagesCompat{SupportsLongCacheRetention: &noLong}
	if got := breakpointTTLs(buildAnthropicParams(&m2, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort, SessionID: "s1"})); !equalStrings(got, want) {
		t.Fatalf("unsupported model: got %v want %v", got, want)
	}
}
