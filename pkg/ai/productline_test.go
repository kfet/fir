package ai

import (
	"reflect"
	"testing"
)

func TestProductLine(t *testing.T) {
	tests := []struct {
		id    string
		shape string
		vec   []int
		ok    bool
	}{
		{"claude-opus-5", "claude-opus-#", []int{5}, true},
		{"claude-opus-5-5", "claude-opus-#", []int{5, 5}, true},
		{"claude-opus-5.5", "claude-opus-#", []int{5, 5}, true},
		{"us.anthropic.claude-opus-5-5", "us.anthropic.claude-opus-#", []int{5, 5}, true},
		{"gemini-3.1-pro-preview", "gemini-#-pro-preview", []int{3, 1}, true},
		{"moonshotai/kimi-k2.6", "moonshotai/kimi-k#", []int{2, 6}, true},
		{"MiniMax-M2.7", "MiniMax-M#", []int{2, 7}, true},
		{"deepseek-v4-pro", "deepseek-v#-pro", []int{4}, true},
		{"claude-opus-4-5-20251101", "", nil, false},
		{"grok-4.20-0309-reasoning", "", nil, false},
		{"kimi-for-coding", "", nil, false},
		{"kimi-k2p6", "", nil, false},
		{"gpt-4o", "", nil, false},
		{"gpt-5-1106", "", nil, false},
		{"o1-2024-12-17", "", nil, false},
		{"gpt-5.", "", nil, false},
		{"gpt-.5", "", nil, false},
		{"gpt-5..4", "", nil, false},
		{"gpt--5", "gpt--#", []int{5}, true},
	}
	for _, tt := range tests {
		shape, vec, ok := ProductLine(tt.id)
		if shape != tt.shape || ok != tt.ok || !reflect.DeepEqual(vec, tt.vec) {
			t.Errorf("ProductLine(%q) = %q %v %v, want %q %v %v", tt.id, shape, vec, ok, tt.shape, tt.vec, tt.ok)
		}
	}
}

func TestNewestInProductLine(t *testing.T) {
	tests := []struct {
		name       string
		anchor     string
		candidates []string
		want       string
	}{
		{"anthropic opus 5 -> 5-5", "claude-opus-5",
			[]string{"claude-opus-4-6", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-6"}, "claude-opus-5-5"},
		{"bedrock us prefix stays us", "us.anthropic.claude-opus-5",
			[]string{"us.anthropic.claude-opus-5", "global.anthropic.claude-opus-6", "eu.anthropic.claude-opus-7",
				"anthropic.claude-opus-8", "us.anthropic.claude-opus-5-5"}, "us.anthropic.claude-opus-5-5"},
		{"bedrock global prefix stays global", "global.anthropic.claude-opus-5",
			[]string{"us.anthropic.claude-opus-6", "global.anthropic.claude-opus-5-5"}, "global.anthropic.claude-opus-5-5"},
		{"never crosses product line", "claude-opus-5",
			[]string{"claude-sonnet-9", "claude-haiku-9"}, "claude-opus-5"},
		{"never picks a variant", "claude-opus-5",
			[]string{"claude-opus-9-fast", "claude-opus-9-thinking"}, "claude-opus-5"},
		{"skips dated snapshots", "claude-opus-5",
			[]string{"claude-opus-9-20270101"}, "claude-opus-5"},
		{"preview stays preview", "gemini-3.1-pro-preview",
			[]string{"gemini-3.5-pro", "gemini-3.5-pro-preview"}, "gemini-3.5-pro-preview"},
		{"no older generation", "gpt-5.4",
			[]string{"gpt-5.2", "gpt-5", "gpt-5.4"}, "gpt-5.4"},
		{"gpt minor bump", "gpt-5.4", []string{"gpt-5.5", "gpt-5.5-pro", "gpt-5.5-mini"}, "gpt-5.5"},
		{"aggregator prefix stays", "moonshotai/kimi-k2.6",
			[]string{"kimi-k2.7", "moonshotai/kimi-k2.7"}, "moonshotai/kimi-k2.7"},
		{"unorderable anchor unchanged", "kimi-for-coding",
			[]string{"kimi-for-coding-2"}, "kimi-for-coding"},
		{"equal generation spelling keeps anchor", "claude-opus-5-5",
			[]string{"claude-opus-5.5"}, "claude-opus-5-5"},
		{"equal generation tie deterministic", "claude-opus-5",
			[]string{"claude-opus-5.5", "claude-opus-5-5"}, "claude-opus-5-5"},
		{"anchor not registered", "claude-opus-5", nil, "claude-opus-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewestInProductLine(tt.anchor, tt.candidates); got != tt.want {
				t.Fatalf("NewestInProductLine(%q) = %q, want %q", tt.anchor, got, tt.want)
			}
		})
	}
}
