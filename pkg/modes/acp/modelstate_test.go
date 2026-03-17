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
