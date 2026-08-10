package main

import (
	"testing"
)

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

func TestParsePoeContextFromDesc(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want int
	}{
		{"context-window-colon-k", "- Context Window: 256k", 256_000},
		{"context-window-full-number", "- Context Window: 1,000,000", 1_000_000},
		{"context-length-k", "Context Length: 131k", 131_000},
		{"supports-k-context", "It supports 256k context, four reasoning modes", 256_000},
		{"context-window-of-tokens", "It supports a context window of 128,000 tokens", 128_000},
		{"offers-context-window-of", "It offers a context window of 300,000 tokens", 300_000},
		{"million-token-context-window", "It offers a 1 million token context window", 1_000_000},
		{"long-contexts-million-tokens", "extremely long contexts (\u2248 1 million tokens)", 1_000_000},
		{"no-match", "Kimi K2.5 is Moonshot AI's flagship agentic model", 0},
		{"reject-typo-huge", "Context Window: 40,000k", 0}, // 40M > cap
		{"reject-too-small", "Context Window: 100", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePoeContextFromDesc(tt.desc); got != tt.want {
				t.Errorf("parsePoeContextFromDesc(%q) = %d, want %d", tt.desc, got, tt.want)
			}
		})
	}
}

func TestPoeSiblingCtx(t *testing.T) {
	known := map[string]int{"kimi-k2.5": 128000, "qwen3.5-397b-a17b": 64000, "glm-5": 131072}
	if got := poeSiblingCtx("kimi-k2.5-fw", known); got != 128000 {
		t.Errorf("kimi sibling fallback got %d, want 128000", got)
	}
	if got := poeSiblingCtx("qwen3.5-397b-fw", known); got != 64000 {
		t.Errorf("qwen sibling fallback got %d, want 64000", got)
	}
	if got := poeSiblingCtx("glm-5.1-fw", known); got != 131072 {
		t.Errorf("glm sibling fallback got %d, want 131072", got)
	}
}
