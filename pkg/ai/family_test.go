package ai

import (
	"reflect"
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
			got := ExtractFamily(tt.input)
			if got != tt.want {
				t.Errorf("ExtractFamily(%q) = %q, want %q", tt.input, got, tt.want)
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

func TestExtractLineage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// The whole point: successive generations share a lineage.
		{"claude-opus-4-6", "claude-opus"},
		{"claude-opus-5", "claude-opus"},
		{"claude-opus-4-5-20251101", "claude-opus"},
		{"us.anthropic.claude-opus-5-v1:0", "claude-opus"},
		{"claude-3-5-sonnet-20241022", "claude-sonnet"},
		{"claude-sonnet-4-6", "claude-sonnet"},
		{"gpt-5.4", "gpt"},
		{"gpt-5.5", "gpt"},
		{"gpt-5.4-pro", "gpt-pro"},
		{"gemini-3.1-pro-preview", "gemini-pro"},
		{"gemini-3-pro", "gemini-pro"},
		{"google/gemini-2.5-pro", "gemini-pro"},

		// No version tokens at all: lineage is the whole id.
		{"kimi-k2-thinking", "kimi-k2-thinking"},
		{"deepseek/deepseek-r1", "deepseek-r1"},
		{"k2p5", "k2p5"},

		// Nothing but a version: no comparable lineage.
		{"4-6", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ExtractLineage(tt.input); got != tt.want {
				t.Errorf("ExtractLineage(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGenerationVector(t *testing.T) {
	tests := []struct {
		input string
		want  []int
		ok    bool
	}{
		{"claude-opus-4-6", []int{4, 6}, true},
		{"claude-opus-5", []int{5}, true},
		{"claude-opus-5-20260115", []int{5}, true}, // date stamp is not a generation
		{"us.anthropic.claude-opus-4-6-v1:0", []int{4, 6}, true},
		{"gpt-5.4", []int{5, 4}, true},
		{"gemini-3.1-pro-preview", []int{3, 1}, true},
		{"gemini-3.1-pro", []int{3, 1}, true},
		{"claude-3-5-sonnet-20241022", []int{3, 5}, true},

		// Unversioned ids must report ok=false so callers stay silent.
		{"kimi-k2-thinking", nil, false},
		{"kimi-k3", nil, false},
		{"deepseek/deepseek-r1", nil, false},
		{"k2p5", nil, false},
		{"gpt-5-chat-latest", []int{5}, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := GenerationVector(tt.input)
			if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GenerationVector(%q) = %v, %v; want %v, %v", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCompareGenerations(t *testing.T) {
	tests := []struct {
		a, b []int
		want int
	}{
		{[]int{4, 6}, []int{5}, -1},    // opus 4.6 predates opus 5
		{[]int{5}, []int{4, 6}, 1},     // and the reverse
		{[]int{5}, []int{5}, 0},        // identical
		{[]int{5}, []int{5, 1}, -1},    // missing element compares low
		{[]int{5, 0}, []int{5}, 0},     // explicit zero == absent
		{[]int{3, 1}, []int{3, 1}, 0},  // preview vs plain, once tags are stripped
		{[]int{5, 4}, []int{5, 5}, -1}, // gpt-5.4 vs gpt-5.5
		{[]int{10}, []int{9}, 1},       // numeric, not lexical
	}
	for _, tt := range tests {
		if got := CompareGenerations(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareGenerations(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
