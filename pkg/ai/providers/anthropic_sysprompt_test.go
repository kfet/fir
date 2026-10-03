package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/sections"
)

func systemBlocks(t *testing.T, params map[string]any) []map[string]any {
	t.Helper()
	blocks, ok := params["system"].([]map[string]any)
	if !ok {
		t.Fatalf("system not []map[string]any, got %T", params["system"])
	}
	return blocks
}

func totalBreakpoints(t *testing.T, params map[string]any) int {
	t.Helper()
	n := countMessageBreakpoints(params["messages"].([]map[string]any))
	for _, b := range systemBlocks(t, params) {
		if b["cache_control"] != nil {
			n++
		}
	}
	return n
}

func TestAnthropic_SystemPromptSplitIntoTwoCachedBlocks(t *testing.T) {
	model := &ai.Model{ID: "claude-x", MaxTokens: 8192, Headers: map[string]string{"x-anthropic-oauth-system-prefix": "You are Claude Code."}}
	ctx := ai.Context{
		SystemPrompt: "stable" + ai.SystemPromptCacheBoundary + "\nCurrent date: 2026-01-01",
		Messages:     []ai.Message{ai.NewUserMsg("hi", 1)},
	}
	params := buildAnthropicParams(model, ctx, true, &ai.StreamOptions{CacheRetention: ai.CacheShort})
	blocks := systemBlocks(t, params)
	if len(blocks) != 3 {
		t.Fatalf("want oauth+stable+volatile blocks, got %d", len(blocks))
	}
	if blocks[0]["text"] != "You are Claude Code." || blocks[0]["cache_control"] != nil {
		t.Errorf("oauth prefix must be first and uncached: %v", blocks[0])
	}
	if blocks[1]["text"] != "stable" || blocks[1]["cache_control"] == nil {
		t.Errorf("stable block: %v", blocks[1])
	}
	if blocks[2]["text"] != "\nCurrent date: 2026-01-01" || blocks[2]["cache_control"] == nil {
		t.Errorf("volatile block: %v", blocks[2])
	}
}

func TestAnthropic_SystemPromptWithoutBoundaryIsOneBlock(t *testing.T) {
	model := &ai.Model{ID: "claude-x", MaxTokens: 8192}
	ctx := ai.Context{SystemPrompt: "Be helpful.", Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}
	blocks := systemBlocks(t, buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort}))
	if len(blocks) != 1 || blocks[0]["text"] != "Be helpful." {
		t.Fatalf("got %v", blocks)
	}
}

func TestAnthropic_SystemPromptCacheNoneHasNoBreakpoints(t *testing.T) {
	model := &ai.Model{ID: "claude-x", MaxTokens: 8192}
	ctx := ai.Context{SystemPrompt: "a" + ai.SystemPromptCacheBoundary + "b", Messages: []ai.Message{ai.NewUserMsg("hi", 1)}}
	params := buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheNone})
	if n := totalBreakpoints(t, params); n != 0 {
		t.Fatalf("want 0 breakpoints, got %d", n)
	}
}

// Side query with a sections block and two rolling anchors already uses
// three message breakpoints; the split system prompt must not push the
// request past Anthropic's limit of four.
func TestAnthropic_SystemSplitRespectsBreakpointLimitOnSideQuery(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	mk := func(turns int, q string) ai.Context {
		c := sideQueryCtx(turns, q)
		c.SystemPrompt = "stable" + ai.SystemPromptCacheBoundary + "volatile"
		block := sections.RenderBlock([]sections.Section{{Name: "mood", Text: "L1"}})
		c.Messages = append([]ai.Message{ai.NewUserMsg(block, 0)}, c.Messages...)
		return c
	}
	buildAnthropicParams(model, mk(3, "q1"), false, sideQueryOptions("sess", "claude-x"))
	params := buildAnthropicParams(model, mk(6, "q2"), false, sideQueryOptions("sess", "claude-x"))

	if n := countMessageBreakpoints(params["messages"].([]map[string]any)); n != 3 {
		t.Fatalf("fixture: want 3 message breakpoints, got %d", n)
	}
	if n := totalBreakpoints(t, params); n != maxCacheBreakpoints {
		t.Fatalf("want %d breakpoints, got %d", maxCacheBreakpoints, n)
	}
	blocks := systemBlocks(t, params)
	if blocks[0]["cache_control"] == nil {
		t.Error("stable block must keep its breakpoint")
	}
	if blocks[1]["cache_control"] != nil {
		t.Error("volatile block breakpoint should be dropped to fit the limit")
	}
}

func TestAnthropic_ExecutorPathKeepsBothSystemBreakpoints(t *testing.T) {
	model := &ai.Model{ID: "claude-x", MaxTokens: 8192}
	block := sections.RenderBlock([]sections.Section{{Name: "mood", Text: "L1"}})
	ctx := ai.Context{
		SystemPrompt: "stable" + ai.SystemPromptCacheBoundary + "volatile",
		Messages:     []ai.Message{ai.NewUserMsg(block, 0), ai.NewUserMsg("hi", 1)},
	}
	params := buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort})
	if n := totalBreakpoints(t, params); n != 4 {
		t.Fatalf("want 4 breakpoints (2 system + sections + tail), got %d", n)
	}
}
