package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/stretchr/testify/assert"
)

func TestBuildBaseOptions_NilOptions(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	opts := BuildBaseOptions(model, nil, "test-key")
	assert.Equal(t, 8192, *opts.MaxTokens)
	assert.Equal(t, "test-key", opts.APIKey)
}

func TestBuildBaseOptions_MaxTokensCapped(t *testing.T) {
	model := &ai.Model{MaxTokens: 100000}
	opts := BuildBaseOptions(model, nil, "")
	assert.Equal(t, 32000, *opts.MaxTokens)
}

func TestBuildBaseOptions_WithSimpleOptions(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	temp := 0.7
	maxTok := 4096
	simple := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			Temperature: &temp,
			MaxTokens:   &maxTok,
			APIKey:      "simple-key",
		},
	}
	opts := BuildBaseOptions(model, simple, "")
	assert.Equal(t, 0.7, *opts.Temperature)
	assert.Equal(t, 4096, *opts.MaxTokens)
	assert.Equal(t, "simple-key", opts.APIKey)
}

func TestBuildBaseOptions_ApiKeyPriority(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	simple := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{APIKey: "simple-key"},
	}
	opts := BuildBaseOptions(model, simple, "explicit-key")
	assert.Equal(t, "explicit-key", opts.APIKey)
}

func TestClampReasoning(t *testing.T) {
	assert.Equal(t, ai.ThinkingHigh, ClampReasoning(ai.ThinkingXHigh))
	assert.Equal(t, ai.ThinkingHigh, ClampReasoning(ai.ThinkingMax))
	assert.Equal(t, ai.ThinkingHigh, ClampReasoning(ai.ThinkingHigh))
	assert.Equal(t, ai.ThinkingMedium, ClampReasoning(ai.ThinkingMedium))
	assert.Equal(t, ai.ThinkingLow, ClampReasoning(ai.ThinkingLow))
	assert.Equal(t, ai.ThinkingMinimal, ClampReasoning(ai.ThinkingMinimal))
}

func TestClampReasoningForModel(t *testing.T) {
	opus48 := &ai.Model{ID: "claude-opus-4-8", API: ai.ApiAnthropicMessages}
	opus47 := &ai.Model{ID: "claude-opus-4-7", API: ai.ApiAnthropicMessages}
	opus46 := &ai.Model{ID: "claude-opus-4-6", API: ai.ApiAnthropicMessages}
	gpt53 := &ai.Model{ID: "gpt-5.3", API: ai.ApiOpenAICompletions}
	vanilla := &ai.Model{ID: "claude-sonnet-4-20250514", API: ai.ApiAnthropicMessages}

	// Opus 4.7+: supports both xhigh and max — pass through.
	assert.Equal(t, ai.ThinkingXHigh, ClampReasoningForModel(ai.ThinkingXHigh, opus48))
	assert.Equal(t, ai.ThinkingMax, ClampReasoningForModel(ai.ThinkingMax, opus48))
	assert.Equal(t, ai.ThinkingXHigh, ClampReasoningForModel(ai.ThinkingXHigh, opus47))
	assert.Equal(t, ai.ThinkingMax, ClampReasoningForModel(ai.ThinkingMax, opus47))

	// Opus 4.6: supports max but NOT xhigh — xhigh clamps to high, max stays.
	assert.Equal(t, ai.ThinkingHigh, ClampReasoningForModel(ai.ThinkingXHigh, opus46))
	assert.Equal(t, ai.ThinkingMax, ClampReasoningForModel(ai.ThinkingMax, opus46))

	// gpt-5.3: supports xhigh but not max — max clamps down to xhigh.
	assert.Equal(t, ai.ThinkingXHigh, ClampReasoningForModel(ai.ThinkingXHigh, gpt53))
	assert.Equal(t, ai.ThinkingXHigh, ClampReasoningForModel(ai.ThinkingMax, gpt53))

	// Vanilla model: neither xhigh nor max — both clamp to high.
	assert.Equal(t, ai.ThinkingHigh, ClampReasoningForModel(ai.ThinkingXHigh, vanilla))
	assert.Equal(t, ai.ThinkingHigh, ClampReasoningForModel(ai.ThinkingMax, vanilla))

	// Non-top levels pass through regardless of model.
	assert.Equal(t, ai.ThinkingMedium, ClampReasoningForModel(ai.ThinkingMedium, vanilla))
	assert.Equal(t, ai.ThinkingHigh, ClampReasoningForModel(ai.ThinkingHigh, vanilla))
}

func TestBedrockThinkingLevelToEffort(t *testing.T) {
	// Opus 4.8 — xhigh is its own effort value.
	assert.Equal(t, "xhigh", bedrockThinkingLevelToEffort(ai.ThinkingXHigh, &ai.Model{ID: "anthropic.claude-opus-4-8"}))
	assert.Equal(t, "max", bedrockThinkingLevelToEffort(ai.ThinkingMax, &ai.Model{ID: "anthropic.claude-opus-4-8"}))

	// Opus 4.7 — xhigh is its own effort value.
	assert.Equal(t, "low", bedrockThinkingLevelToEffort(ai.ThinkingMinimal, &ai.Model{ID: "anthropic.claude-opus-4-7"}))
	assert.Equal(t, "medium", bedrockThinkingLevelToEffort(ai.ThinkingMedium, &ai.Model{ID: "anthropic.claude-opus-4-7"}))
	assert.Equal(t, "high", bedrockThinkingLevelToEffort(ai.ThinkingHigh, &ai.Model{ID: "anthropic.claude-opus-4-7"}))
	assert.Equal(t, "xhigh", bedrockThinkingLevelToEffort(ai.ThinkingXHigh, &ai.Model{ID: "anthropic.claude-opus-4-7"}))
	assert.Equal(t, "max", bedrockThinkingLevelToEffort(ai.ThinkingMax, &ai.Model{ID: "anthropic.claude-opus-4-7"}))

	// Opus 4.6 — xhigh clamps to high, max still goes through.
	assert.Equal(t, "high", bedrockThinkingLevelToEffort(ai.ThinkingXHigh, &ai.Model{ID: "anthropic.claude-opus-4-6"}))
	assert.Equal(t, "max", bedrockThinkingLevelToEffort(ai.ThinkingMax, &ai.Model{ID: "anthropic.claude-opus-4-6"}))

	// Sonnet 4.6 — same rules as Opus 4.6.
	assert.Equal(t, "high", bedrockThinkingLevelToEffort(ai.ThinkingXHigh, &ai.Model{ID: "anthropic.claude-sonnet-4-6"}))
	assert.Equal(t, "max", bedrockThinkingLevelToEffort(ai.ThinkingMax, &ai.Model{ID: "anthropic.claude-sonnet-4-6"}))
}

func TestAdjustMaxTokensForThinking_Default(t *testing.T) {
	maxTok, budget := AdjustMaxTokensForThinking(8192, 32000, ai.ThinkingHigh, nil)
	assert.Equal(t, 8192+16384, maxTok)
	assert.Equal(t, 16384, budget)
}

func TestAdjustMaxTokensForThinking_Minimal(t *testing.T) {
	maxTok, budget := AdjustMaxTokensForThinking(8192, 32000, ai.ThinkingMinimal, nil)
	assert.Equal(t, 8192+1024, maxTok)
	assert.Equal(t, 1024, budget)
}

func TestAdjustMaxTokensForThinking_CappedByModel(t *testing.T) {
	maxTok, _ := AdjustMaxTokensForThinking(8192, 10000, ai.ThinkingHigh, nil)
	assert.Equal(t, 10000, maxTok)
}

func TestAdjustMaxTokensForThinking_BudgetExceedsMax(t *testing.T) {
	maxTok, budget := AdjustMaxTokensForThinking(100, 2000, ai.ThinkingHigh, nil)
	assert.Equal(t, 2000, maxTok)
	assert.Equal(t, 976, budget) // 2000 - 1024
}

func TestAdjustMaxTokensForThinking_CustomBudgets(t *testing.T) {
	val := 4096
	custom := &ai.ThinkingBudgets{High: &val}
	maxTok, budget := AdjustMaxTokensForThinking(8192, 32000, ai.ThinkingHigh, custom)
	assert.Equal(t, 8192+4096, maxTok)
	assert.Equal(t, 4096, budget)
}

func TestAdjustMaxTokensForThinking_XhighClamped(t *testing.T) {
	maxTok, budget := AdjustMaxTokensForThinking(8192, 32000, ai.ThinkingXHigh, nil)
	assert.Equal(t, 8192+16384, maxTok)
	assert.Equal(t, 16384, budget)
}

func TestBuildBaseOptions_ServerToolsPassThrough(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	simple := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: "test-key",
			ServerTools: []ai.AnthropicServerTool{
				{Type: "web_search_20250305"},
				{Type: "code_execution_20250522"},
			},
		},
	}
	opts := BuildBaseOptions(model, simple, "")
	assert.Equal(t, 2, len(opts.ServerTools))
	assert.Equal(t, "web_search_20250305", opts.ServerTools[0].Type)
	assert.Equal(t, "code_execution_20250522", opts.ServerTools[1].Type)
}

func TestBuildBaseOptions_CompactionPassThrough(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	simple := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: "test-key",
			Compaction: &ai.AnthropicCompaction{
				Enabled:       true,
				TriggerTokens: 100000,
			},
		},
	}
	opts := BuildBaseOptions(model, simple, "")
	assert.NotNil(t, opts.Compaction)
	assert.True(t, opts.Compaction.Enabled)
	assert.Equal(t, 100000, opts.Compaction.TriggerTokens)
}

func TestClampEffortToEnum(t *testing.T) {
	cases := []struct {
		name    string
		effort  string
		allowed []string
		want    string
	}{
		{"empty-allowed-passes-through", "low", nil, "low"},
		{"empty-effort-passes-through", "", []string{"medium"}, ""},
		{"exact-match", "high", []string{"low", "medium", "high"}, "high"},
		{"single-value-forces-medium", "low", []string{"medium"}, "medium"},
		{"single-value-forces-medium-from-high", "high", []string{"medium"}, "medium"},
		{"snap-up-from-minimal", "minimal", []string{"medium", "high", "xhigh"}, "medium"},
		{"snap-up-from-low", "low", []string{"medium", "high", "xhigh"}, "medium"},
		{"snap-down-from-max", "max", []string{"medium", "high", "xhigh"}, "xhigh"},
		{"tie-breaks-up", "medium", []string{"low", "high"}, "high"},
		{"extra-high-equiv-xhigh", "xhigh", []string{"medium", "extra-high"}, "extra-high"},
		{"unknown-falls-back-to-first", "bogus", []string{"medium", "high"}, "medium"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clampEffortToEnum(c.effort, c.allowed)
			assert.Equal(t, c.want, got)
		})
	}
}
