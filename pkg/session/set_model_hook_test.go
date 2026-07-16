package session

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// TestSetModel_NoHook confirms SetModel works unchanged when no resolver hook
// is installed.
func TestSetModel_NoHook(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()

	m := &ai.Model{ID: "m1", Provider: "poe", API: ai.ApiOpenAICompletions, BaseURL: "https://api.poe.com/v1"}
	if err := s.SetModel(m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Model() == nil || s.Model().ID != "m1" {
		t.Fatalf("expected model m1 set, got %+v", s.Model())
	}
}

// TestSetModel_HookAppliesCorrection verifies a base_url/api correction from
// the resolver is applied to the selected model without mutating the original.
func TestSetModel_HookAppliesCorrection(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()

	orig := &ai.Model{ID: "m2", Provider: "poe", API: ai.ApiOpenAICompletions, BaseURL: "https://api.poe.com/v1"}
	s.SetHooks(&AgentSessionHooks{
		ResolveModelEndpoint: func(model *ai.Model) *ModelEndpointCorrection {
			return &ModelEndpointCorrection{API: string(ai.ApiOpenAIResponses)}
		},
	})

	if err := s.SetModel(orig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.Model(); got == nil || got.API != ai.ApiOpenAIResponses {
		t.Fatalf("expected responses API applied, got %+v", got)
	}
	if orig.API != ai.ApiOpenAICompletions {
		t.Errorf("original model must not be mutated, got API=%s", orig.API)
	}
}

// TestSetModel_HookRejectsUncallable verifies a callable=false verdict refuses
// the selection with a clean error and does not change the active model.
func TestSetModel_HookRejectsUncallable(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()

	// Set a known-good model first.
	base := &ai.Model{ID: "good", Provider: "poe", API: ai.ApiOpenAICompletions, BaseURL: "https://api.poe.com/v1"}
	if err := s.SetModel(base); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	no := false
	s.SetHooks(&AgentSessionHooks{
		ResolveModelEndpoint: func(model *ai.Model) *ModelEndpointCorrection {
			return &ModelEndpointCorrection{Callable: &no}
		},
	})

	dead := &ai.Model{ID: "dead", Provider: "poe", API: ai.ApiOpenAICompletions, BaseURL: "https://api.poe.com/v1"}
	err := s.SetModel(dead)
	if err == nil {
		t.Fatal("expected error for uncallable model")
	}
	if s.Model() == nil || s.Model().ID != "good" {
		t.Errorf("active model should remain 'good', got %+v", s.Model())
	}
}
