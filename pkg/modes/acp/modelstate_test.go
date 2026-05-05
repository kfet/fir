package acp

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/models"
)

func TestParseModelID(t *testing.T) {
	tests := []struct {
		input       string
		provider    string
		modelID     string
		expectError bool
	}{
		{"anthropic/claude-3", "anthropic", "claude-3", false},
		{"openai/gpt-4o", "openai", "gpt-4o", false},
		{"provider/model/with/slashes", "provider", "model/with/slashes", false},
		{"noslash", "", "", true},
	}
	for _, tt := range tests {
		p, m, err := ParseModelID(tt.input)
		if tt.expectError {
			if err == nil {
				t.Errorf("ParseModelID(%q): expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModelID(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if p != tt.provider || m != tt.modelID {
			t.Errorf("ParseModelID(%q) = (%q, %q), want (%q, %q)", tt.input, p, m, tt.provider, tt.modelID)
		}
	}
}

func TestSortAvailableModels(t *testing.T) {
	makeModel := func(id, provider string, swe float64, inputCost float64) *ai.Model {
		return &ai.Model{ID: id, Provider: ai.Provider(provider), Name: id, SWEScore: swe, Cost: ai.ModelCost{Input: inputCost, Output: inputCost}}
	}

	// Provider grouping: anthropic comes before openai (per
	// OrderedProviders), even when an openai model has a higher SWE
	// score. Within a provider: SWE desc, then ID alpha. Note that
	// zero-cost non-Poe models are NOT classified as free (those are
	// subscription/OAuth gated in the registry), so the free/paid
	// tiebreaker doesn't fire here — a-free wins over a-high purely
	// by ID alphabetic at equal SWE.
	anthHigh := makeModel("a-high", "anthropic", 80, 3.0)
	anthLow := makeModel("a-low", "anthropic", 40, 3.0)
	anthFree := makeModel("a-free", "anthropic", 80, 0)
	openaiHigher := makeModel("o-high", "openai", 90, 3.0)
	openaiLow := makeModel("o-low", "openai", 50, 3.0)
	unknownProv := makeModel("u", "zzz-unknown", 95, 3.0)

	input := []*ai.Model{openaiHigher, anthLow, unknownProv, anthHigh, openaiLow, anthFree}
	out := models.SortModels(input, nil)

	// Expected: anthropic block (a-free, a-high tie at SWE 80 → ID
	// alphabetic; then a-low), openai block (openaiHigher, openaiLow),
	// then unknown providers — even though unknown has highest score.
	wantOrder := []string{"a-free", "a-high", "a-low", "o-high", "o-low", "u"}
	if len(out) != len(wantOrder) {
		t.Fatalf("got %d models, want %d", len(out), len(wantOrder))
	}
	for i, want := range wantOrder {
		if out[i].ID != want {
			t.Errorf("position %d: got %q, want %q", i, out[i].ID, want)
		}
	}
}

func TestBuildModelState(t *testing.T) {
	for _, key := range envkeys.KnownApiKeyEnvVars() {
		t.Setenv(key, "")
	}

	authStore := auth.NewInMemoryAuthStorage(nil)
	reg := models.NewModelRegistry(authStore, "")

	t.Run("nil current model returns nil", func(t *testing.T) {
		state := BuildModelState(reg, nil)
		if state != nil {
			t.Errorf("expected nil, got %+v", state)
		}
	})

	t.Run("with current model but no auth returns empty available list", func(t *testing.T) {
		current := &ai.Model{ID: "claude-3-7-sonnet-20250219", Provider: "anthropic", Name: "Claude 3.7 Sonnet"}
		state := BuildModelState(reg, current)
		if state == nil {
			t.Fatal("expected non-nil state")
		}
		wantCurrentID := acpsdk.ModelId("anthropic/claude-3-7-sonnet-20250219")
		if state.CurrentModelId != wantCurrentID {
			t.Errorf("CurrentModelId = %q, want %q", state.CurrentModelId, wantCurrentID)
		}
		if len(state.AvailableModels) != 0 {
			t.Errorf("AvailableModels should be empty with no auth, got %d models", len(state.AvailableModels))
		}
	})

	t.Run("with auth configured returns models for that provider", func(t *testing.T) {
		auth2 := auth.NewInMemoryAuthStorage(nil)
		auth2.SetRuntimeApiKey("anthropic", "test-api-key")
		reg2 := models.NewModelRegistry(auth2, "")
		current := &ai.Model{ID: "claude-3-7-sonnet-20250219", Provider: "anthropic", Name: "Claude 3.7 Sonnet"}
		state := BuildModelState(reg2, current)
		if state == nil {
			t.Fatal("expected non-nil state")
		}
		if len(state.AvailableModels) == 0 {
			t.Error("AvailableModels is empty; expected anthropic models since auth is set")
		}
		for _, m := range state.AvailableModels {
			if m.ModelId == "" {
				t.Errorf("AvailableModels entry has empty ModelId: %+v", m)
			}
			if m.Name == "" {
				t.Errorf("AvailableModels entry has empty Name: %+v", m)
			}
			if !strings.HasPrefix(string(m.ModelId), "anthropic/") {
				t.Errorf("got model from provider without auth: %s", m.ModelId)
			}
		}
	})
}
