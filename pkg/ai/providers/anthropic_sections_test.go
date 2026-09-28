package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/sections"
)

func TestAnthropic_SectionsBlockGetsCacheBreakpoint(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	block := sections.RenderBlock([]sections.Section{{Name: "mood", Text: "L1"}})
	ctx := ai.Context{
		SystemPrompt: "Be helpful.",
		Messages: []ai.Message{
			ai.NewUserMsg(block, 0),
			ai.NewUserMsg("hi", 1000),
			ai.NewAssistantMsg(ai.AssistantMessage{Content: []ai.AssistantContent{ai.NewTextContent("yo")}, StopReason: ai.StopReasonStop, Timestamp: 1001}),
			ai.NewUserMsg("again", 1002),
		},
	}
	params := buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort})
	msgs := params["messages"].([]map[string]any)
	first := msgs[0]["content"].([]map[string]any)
	if first[len(first)-1]["cache_control"] == nil {
		t.Fatal("sections block should carry cache_control")
	}
	second := msgs[1]["content"].([]map[string]any)
	if second[len(second)-1]["cache_control"] != nil {
		t.Fatal("ordinary user message must not get a breakpoint")
	}

	// An ordinary leading user message gets no extra breakpoint.
	ctx.Messages = ctx.Messages[1:]
	params = buildAnthropicParams(model, ctx, false, &ai.StreamOptions{CacheRetention: ai.CacheShort})
	msgs = params["messages"].([]map[string]any)
	if msgs[0]["content"].([]map[string]any)[0]["cache_control"] != nil {
		t.Fatal("non-sections first message must not get a breakpoint")
	}
}
