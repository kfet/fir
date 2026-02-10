package providers

import (
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/stretchr/testify/assert"
)

func TestBuildBaseOptions_NilOptions(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	opts := BuildBaseOptions(model, nil, "test-key")
	assert.Equal(t, 8192, *opts.MaxTokens)
	assert.Equal(t, "test-key", opts.ApiKey)
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
			ApiKey:      "simple-key",
		},
	}
	opts := BuildBaseOptions(model, simple, "")
	assert.Equal(t, 0.7, *opts.Temperature)
	assert.Equal(t, 4096, *opts.MaxTokens)
	assert.Equal(t, "simple-key", opts.ApiKey)
}

func TestBuildBaseOptions_ApiKeyPriority(t *testing.T) {
	model := &ai.Model{MaxTokens: 8192}
	simple := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{ApiKey: "simple-key"},
	}
	opts := BuildBaseOptions(model, simple, "explicit-key")
	assert.Equal(t, "explicit-key", opts.ApiKey)
}

func TestClampReasoning(t *testing.T) {
	assert.Equal(t, ai.ThinkingHigh, ClampReasoning(ai.ThinkingXHigh))
	assert.Equal(t, ai.ThinkingHigh, ClampReasoning(ai.ThinkingHigh))
	assert.Equal(t, ai.ThinkingMedium, ClampReasoning(ai.ThinkingMedium))
	assert.Equal(t, ai.ThinkingLow, ClampReasoning(ai.ThinkingLow))
	assert.Equal(t, ai.ThinkingMinimal, ClampReasoning(ai.ThinkingMinimal))
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
