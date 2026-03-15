package main

import (
	"testing"
)

func TestExtractFamily(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Claude 4.x — same generation groups together
		{"claude-opus-4-6", "claude-opus-4"},
		{"claude-opus-4-5-20251101", "claude-opus-4"},
		{"claude-opus-4-5", "claude-opus-4"},
		{"claude-opus-4-0", "claude-opus-4"},
		{"claude-opus-4-20250514", "claude-opus-4"},
		{"claude-sonnet-4-5", "claude-sonnet-4"},
		{"claude-sonnet-4-6", "claude-sonnet-4"},
		{"claude-sonnet-4", "claude-sonnet-4"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4"},

		// Claude 3.x — separate generation from 4.x
		{"claude-3-5-sonnet-20241022", "claude-3-sonnet"},
		{"claude-3-7-sonnet-20250219", "claude-3-sonnet"},
		{"claude-3-opus-20240229", "claude-3-opus"},
		{"claude-3-haiku-20240307", "claude-3-haiku"},
		{"claude-3-sonnet-20240229", "claude-3-sonnet"},

		// GPT — generation preserved, sub-versions stripped
		{"gpt-5.2", "gpt-5"},
		{"gpt-5.4-pro", "gpt-5-pro"},
		{"gpt-5", "gpt-5"},
		{"gpt-5.1-codex", "gpt-5-codex"},
		{"gpt-5.1-codex-max", "gpt-5-codex-max"},
		{"gpt-5-mini", "gpt-5-mini"},
		{"gpt-5-chat-latest", "gpt-5-chat"},

		// Gemini — generation preserved
		{"gemini-3.1-pro-preview", "gemini-3-pro"},
		{"gemini-3-flash-preview", "gemini-3-flash"},
		{"gemini-3-pro-preview", "gemini-3-pro"},
		{"gemini-2.5-pro", "gemini-2-pro"},
		{"gemini-2.5-flash", "gemini-2-flash"},
		{"gemini-2.0-flash", "gemini-2-flash"},

		// OpenRouter provider-prefixed IDs
		{"google/gemini-2.5-pro", "gemini-2-pro"},
		{"deepseek/deepseek-v3.2", "deepseek-3"},
		{"deepseek/deepseek-r1", "deepseek-r1"},
		{"minimax/minimax-m2.5", "minimax-m2"},

		// Bedrock dotted prefixes
		{"us.anthropic.claude-opus-4-6-v1:0", "claude-opus-4"},

		// Grok — generations stay separate
		{"grok-4", "grok-4"},
		{"grok-4-1-fast", "grok-4-fast"},
		{"grok-3", "grok-3"},
		{"grok-3-mini-fast-latest", "grok-3-mini-fast"},

		// MiniMax
		{"minimax-m2.5", "minimax-m2"},
		{"minimax-m2.5-free", "minimax-m2"},

		// Kimi
		{"kimi-k2-thinking", "kimi-k2-thinking"},
		{"k2p5", "k2p5"},

		// Tags stripped
		{"gemini-3.1-pro-preview-customtools", "gemini-3-pro"},
		{"deepseek/deepseek-v3.2-exp", "deepseek-3"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractFamily(tt.input)
			if got != tt.want {
				t.Errorf("extractFamily(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsVersionToken(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"4", true},
		{"4.6", true},
		{"3.1", true},
		{"v3", true},
		{"v3.2", true},
		{"20250514", true},
		{"0", true},
		{"", false},
		{"pro", false},
		{"sonnet", false},
		{"codex", false},
		{"flash", false},
		{"v", false},
		{"latest", false},
		{"r1", false}, // "r1" is not a version — it's a model name token
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isVersionToken(tt.input)
			if got != tt.want {
				t.Errorf("isVersionToken(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestInferSWEScores(t *testing.T) {
	models := []modelSpec{
		{ID: "claude-opus-4-5", Provider: "anthropic", SWEScore: 80.9},
		{ID: "claude-opus-4-6", Provider: "anthropic", SWEScore: 80.8},
		// Unscored, same family (claude-opus-4) — should inherit 80.9 + 0.1 = 81.0
		{ID: "claude-opus-4-0", Provider: "anthropic"},
		// Different generation — should NOT inherit from claude-opus-4
		{ID: "claude-3-opus-20240229", Provider: "anthropic"},
		// Already scored — should NOT be overwritten
		{ID: "claude-sonnet-4-5", Provider: "anthropic", SWEScore: 77.2},
	}

	result := inferSWEScores(models)

	// claude-opus-4-0 should be inferred
	if result[2].SWEScore != 81.0 {
		t.Errorf("claude-opus-4-0: SWEScore = %v, want 81.0", result[2].SWEScore)
	}
	if !result[2].SWEInferred {
		t.Error("claude-opus-4-0: SWEInferred should be true")
	}

	// claude-3-opus should NOT be inferred (different generation, no scored family members)
	if result[3].SWEScore != 0 {
		t.Errorf("claude-3-opus-20240229: SWEScore = %v, want 0 (different generation)", result[3].SWEScore)
	}
	if result[3].SWEInferred {
		t.Error("claude-3-opus-20240229: SWEInferred should be false")
	}

	// claude-sonnet-4-5 should keep its original score
	if result[4].SWEScore != 77.2 {
		t.Errorf("claude-sonnet-4-5: SWEScore = %v, want 77.2 (unchanged)", result[4].SWEScore)
	}
	if result[4].SWEInferred {
		t.Error("claude-sonnet-4-5: SWEInferred should be false")
	}
}

func TestApplySWEScores(t *testing.T) {
	models := []modelSpec{
		{ID: "claude-opus-4-5", Provider: "anthropic"},
		{ID: "claude-opus-4-5-20251101", Provider: "anthropic"},
		{ID: "gpt-5.2", Provider: "openai"},
		{ID: "unknown-model", Provider: "test"},
	}

	// No live leaderboard — use curated baselines only.
	result := applySWEScores(models, nil)

	if result[0].SWEScore != 80.9 {
		t.Errorf("claude-opus-4-5: SWEScore = %v, want 80.9", result[0].SWEScore)
	}
	// Date-versioned variant should also match via substring.
	if result[1].SWEScore != 80.9 {
		t.Errorf("claude-opus-4-5-20251101: SWEScore = %v, want 80.9", result[1].SWEScore)
	}
	if result[2].SWEScore != 72.8 {
		t.Errorf("gpt-5.2: SWEScore = %v, want 72.8", result[2].SWEScore)
	}
	// Unknown model should remain 0.
	if result[3].SWEScore != 0 {
		t.Errorf("unknown-model: SWEScore = %v, want 0", result[3].SWEScore)
	}
}

func TestApplySWEScores_LiveOverride(t *testing.T) {
	models := []modelSpec{
		{ID: "claude-opus-4-5", Provider: "anthropic"},
	}

	// Simulate live leaderboard — should NOT override curated baseline.
	live := map[string]float64{
		"claude opus 4.5 + some-agent": 85.0,
	}

	result := applySWEScores(models, live)

	// Curated baseline (80.9) should be preserved, not overridden by live data.
	if result[0].SWEScore != 80.9 {
		t.Errorf("claude-opus-4-5 with live data: SWEScore = %v, want 80.9 (curated baseline preserved)", result[0].SWEScore)
	}
}

func TestApplySWEScores_LiveGapFill(t *testing.T) {
	// claude-opus-4-0 has a curated score of 67.6 in sweModelPatterns.
	// claude-opus-4-1 has a curated score of 73.0.
	// Simulate a live entry that matches opus 4.1 — should NOT override (score != 0).
	// Also simulate a model key with score=0 that should be filled.
	models := []modelSpec{
		{ID: "claude-opus-4-0", Provider: "anthropic"},
		{ID: "claude-opus-4-1", Provider: "anthropic"},
	}

	live := map[string]float64{
		"claude 4 opus (20250514)": 69.0, // matches claude-opus-4-0 (score=67.6, non-zero → skip)
		"claude opus 4.1":          75.0, // matches claude-opus-4-1 (score=73.0, non-zero → skip)
	}

	result := applySWEScores(models, live)

	// Curated baselines should be preserved.
	if result[0].SWEScore != 67.6 {
		t.Errorf("claude-opus-4-0: SWEScore = %v, want 67.6 (curated preserved)", result[0].SWEScore)
	}
	if result[1].SWEScore != 73.0 {
		t.Errorf("claude-opus-4-1: SWEScore = %v, want 73.0 (curated preserved)", result[1].SWEScore)
	}
}

func TestMatchSWELeaderboardName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude opus 4.5 + swe-agent", "claude-opus-4-5"},
		{"claude 4.6 opus (high reasoning)", "claude-opus-4-6"},
		{"gpt-5.2 + mini-swe-agent", "gpt-5.2"},
		{"gemini 3.1 pro preview", "gemini-3.1-pro-preview"},
		{"minimax m2.5", "minimax-m2.5"},
		{"totally unknown system", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := matchSWELeaderboardName(tt.input)
			if got != tt.want {
				t.Errorf("matchSWELeaderboardName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
