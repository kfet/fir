package models

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

// --- synthesiseFromSibling / commonPrefixLen / humaniseID ---

func TestCommonPrefixLen(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 3},
		{"abc", "abd", 2},
		{"abc", "abcd", 3},
		{"x", "y", 0},
		{"claude-sonnet-4-5", "claude-sonnet-4-7", 16},
	}
	for _, c := range cases {
		if got := commonPrefixLen(c.a, c.b); got != c.want {
			t.Errorf("commonPrefixLen(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestHumaniseID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"gpt-4o", "Gpt 4o"},
		{"claude-sonnet-4-5", "Claude Sonnet 4 5"},
		{"foo_bar/baz:qux", "Foo Bar Baz Qux"},
		{"", ""},
	}
	for _, c := range cases {
		if got := humaniseID(c.in); got != c.want {
			t.Errorf("humaniseID(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestSynthesiseFromSibling(t *testing.T) {
	siblings := []*ai.Model{
		{ID: "a-1", Provider: "p", Api: "api1", BaseURL: "u1", ContextWindow: 100},
		{ID: "claude-sonnet-4-5", Provider: "p", Api: "api2", BaseURL: "u2", ContextWindow: 200},
		{ID: "claude-opus-4-1", Provider: "p", Api: "api3", BaseURL: "u3", ContextWindow: 300},
	}
	// Should pick the longest-prefix match (claude-sonnet-4-5).
	m := synthesiseFromSibling("p", "claude-sonnet-4-7-20260601", siblings)
	if m == nil {
		t.Fatal("expected non-nil synth")
	}
	if m.ID != "claude-sonnet-4-7-20260601" {
		t.Errorf("ID: %q", m.ID)
	}
	if m.ContextWindow != 200 {
		t.Errorf("expected ContextWindow=200 (sonnet sibling), got %d", m.ContextWindow)
	}
	if !m.SWEInferred {
		t.Error("expected SWEInferred=true")
	}
}

func TestSynthesiseFromSibling_EmptySiblings(t *testing.T) {
	if m := synthesiseFromSibling("p", "x", nil); m != nil {
		t.Errorf("expected nil for empty siblings, got %+v", m)
	}
}

// --- ModelDefaults: openAI ---

func TestOpenAIFamily(t *testing.T) {
	cases := map[string]string{
		"gpt-4o":             "gpt-4o",
		"gpt-4o-mini":        "gpt-4o",
		"gpt-4.1-2024-12-01": "gpt-4.1",
		"gpt-5-turbo":        "gpt-5",
		"o1":                 "o1",
		"o3-mini-2025-01-31": "o3",
		"o4":                 "o4",
		"text-davinci-003":   "",
		"":                   "",
	}
	for in, want := range cases {
		if got := openAIFamily(in); got != want {
			t.Errorf("openAIFamily(%q)=%q want %q", in, got, want)
		}
	}
}

func TestOpenAIDefaults_PicksFamilyMatch(t *testing.T) {
	siblings := []*ai.Model{
		{ID: "gpt-4o-2024-08-06", Provider: "openai", ContextWindow: 128000, MaxTokens: 16384},
		{ID: "gpt-4o-mini", Provider: "openai", ContextWindow: 128000, MaxTokens: 16384},
		{ID: "o3-mini", Provider: "openai", ContextWindow: 200000, MaxTokens: 100000, Reasoning: true},
	}
	lister := &openAIModelLister{}

	// New gpt-4o variant -> clones from gpt-4o family (most recent sibling).
	m := lister.ModelDefaults("openai", "gpt-4o-2024-11-20", siblings)
	if m == nil || m.Provider != "openai" || m.ID != "gpt-4o-2024-11-20" {
		t.Fatalf("openai gpt-4o synth wrong: %+v", m)
	}
	if m.Reasoning {
		t.Error("expected non-reasoning for gpt-4o family")
	}

	// New o3 variant -> reasoning sibling.
	m = lister.ModelDefaults("openai", "o3-pro-2026-01-01", siblings)
	if m == nil || !m.Reasoning {
		t.Errorf("expected reasoning=true for o3 family, got %+v", m)
	}

	// Unknown family -> nil (defers to fallback).
	m = lister.ModelDefaults("openai", "babbage-002", siblings)
	if m != nil {
		t.Errorf("expected nil for unknown family, got %+v", m)
	}

	// Provider not in switch -> nil (no special handling).
	m = lister.ModelDefaults("xai", "grok-7", siblings)
	if m != nil {
		t.Errorf("expected nil for non-special-cased provider, got %+v", m)
	}
}

// --- ModelDefaults: openrouter ---

func TestOpenRouterVendor(t *testing.T) {
	if openRouterVendor("anthropic/claude-x") != "anthropic" {
		t.Error("vendor parse failed")
	}
	if openRouterVendor("flat-id") != "" {
		t.Error("expected empty vendor for non-slashed id")
	}
}

func TestOpenRouterDefaults(t *testing.T) {
	siblings := []*ai.Model{
		{ID: "anthropic/claude-sonnet-4-5", Provider: "openrouter", ContextWindow: 200000},
		{ID: "openai/gpt-4o", Provider: "openrouter", ContextWindow: 128000},
	}
	lister := &openAIModelLister{}
	m := lister.ModelDefaults("openrouter", "anthropic/claude-sonnet-4-7", siblings)
	if m == nil || m.ContextWindow != 200000 {
		t.Errorf("expected anthropic vendor clone with 200k ctx, got %+v", m)
	}
	// Unknown vendor -> nil.
	m = lister.ModelDefaults("openrouter", "unknown/x", siblings)
	if m != nil {
		t.Errorf("expected nil for unknown vendor, got %+v", m)
	}
}

// --- ModelDefaults: anthropic ---

func TestAnthropicFamily(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-5":          "sonnet",
		"claude-opus-4-1-20250930":   "opus",
		"claude-haiku-4":             "haiku",
		"claude-3-5-sonnet-20241022": "", // old naming, "3" isn't a family
		"gpt-4o":                     "",
		"":                           "",
	}
	for in, want := range cases {
		if got := anthropicFamily(in); got != want {
			t.Errorf("anthropicFamily(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAnthropicHumanName(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-7-20260601": "Claude Sonnet 4 7 (2026-06-01)",
		"claude-opus-4-1":            "Claude Opus 4 1",
		"claude-haiku-4-20251231":    "Claude Haiku 4 (2025-12-31)",
	}
	for in, want := range cases {
		if got := anthropicHumanName(in); got != want {
			t.Errorf("anthropicHumanName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAllDigits(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"123":      true,
		"12a3":     false,
		"00000000": true,
	}
	for in, want := range cases {
		if got := allDigits(in); got != want {
			t.Errorf("allDigits(%q)=%v want %v", in, got, want)
		}
	}
}

func TestAnthropicDefaults(t *testing.T) {
	siblings := []*ai.Model{
		{ID: "claude-sonnet-4-5-20250101", Provider: "anthropic", Api: "anthropic-messages", ContextWindow: 200000, MaxTokens: 8192},
		{ID: "claude-opus-4-1-20250930", Provider: "anthropic", Api: "anthropic-messages", ContextWindow: 200000, MaxTokens: 32000},
	}
	lister := &anthropicModelLister{}

	m := lister.ModelDefaults("anthropic", "claude-sonnet-4-7-20260601", siblings)
	if m == nil {
		t.Fatal("expected synth")
	}
	if m.MaxTokens != 8192 {
		t.Errorf("expected MaxTokens from sonnet sibling=8192, got %d", m.MaxTokens)
	}
	if m.Name != "Claude Sonnet 4 7 (2026-06-01)" {
		t.Errorf("Name: %q", m.Name)
	}

	// Unknown family -> nil.
	if got := lister.ModelDefaults("anthropic", "claude-future-1", siblings); got != nil {
		t.Errorf("expected nil for unknown family, got %+v", got)
	}
}

// --- Resolution order in synthesise() ---

func TestSynthesise_Pipeline(t *testing.T) {
	// Built-in sibling so the fallback works. Use a provider with no
	// registered ModelDefaulter so we exercise the fallback path.
	ai.RegisterModel(&ai.Model{
		ID:            "synth-base",
		Provider:      "synth-test-provider",
		Api:           "anthropic-messages",
		BaseURL:       "https://example.com",
		ContextWindow: 50000,
		MaxTokens:     1000,
	})
	authStore := auth.NewAuthStorage("")
	r := NewModelRegistry(authStore, "")

	// Fall back to sibling-clone.
	m := r.synthesise(context.Background(), "synth-test-provider", "synth-new")
	if m == nil || m.ContextWindow != 50000 {
		t.Fatalf("fallback failed: %+v", m)
	}
	if m.Provider != "synth-test-provider" || m.ID != "synth-new" {
		t.Errorf("provider/id not stamped: %+v", m)
	}

	// Cache returns same instance second time.
	m2 := r.synthesise(context.Background(), "synth-test-provider", "synth-new")
	if m2 != m {
		t.Error("expected cached instance")
	}
}

func TestSynthesise_NoSiblings_ReturnsNil(t *testing.T) {
	authStore := auth.NewAuthStorage("")
	r := NewModelRegistry(authStore, "")
	m := r.synthesise(context.Background(), "totally-unknown-provider", "x")
	if m != nil {
		t.Errorf("expected nil with no siblings, got %+v", m)
	}
}

// --- Find() honours live-list authority ---

func TestFind_LiveListAuthoritative(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID: "find-auth-real", Provider: "find-auth-test", Api: "anthropic-messages",
		BaseURL: "https://example.com", ContextWindow: 100000, MaxTokens: 4096,
	})
	authStore := auth.NewAuthStorage("")
	r := NewModelRegistry(authStore, "")

	// Inject a live-list state with one known ID.
	state := newLiveModelState()
	state.set([]*ai.Model{
		{ID: "find-auth-live", Provider: "find-auth-test", ContextWindow: 50000},
	})
	r.liveModelsMu.Lock()
	r.liveModels["find-auth-test"] = state
	r.liveModelsMu.Unlock()

	// Built-in still resolves.
	if m := r.Find("find-auth-test", "find-auth-real"); m == nil {
		t.Error("expected built-in to resolve")
	}
	// Live-list ID resolves.
	if m := r.Find("find-auth-test", "find-auth-live"); m == nil || m.ContextWindow != 50000 {
		t.Errorf("expected live-list synth, got %+v", m)
	}
	// Mistyped ID returns nil — live-list is authoritative.
	if m := r.Find("find-auth-test", "find-auth-typo"); m != nil {
		t.Errorf("expected nil for mistyped ID when live-list present, got %+v", m)
	}
}
