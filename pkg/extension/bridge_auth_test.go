package extension

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestValidateAuthProviderID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"my-corp", false},
		{"acme", false},
		{"a1-b2", false},
		{"", true},
		{"1bad", true},
		{"BAD", true},
		{"has space", true},
		// Built-in collisions
		{"anthropic", true},
		{"github-copilot", true},
		{"openai-codex", true},
	}
	for _, tt := range tests {
		err := ValidateAuthProviderID(tt.id, false)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateAuthProviderID(%q) error=%v, wantErr=%v", tt.id, err, tt.wantErr)
		}
	}
}

func TestAuthProviderSpecInInitResult(t *testing.T) {
	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{ID: "my-corp", Name: "My Corp SSO", UsesCallbackServer: true},
		},
	}
	if len(caps.AuthProviders) != 1 {
		t.Fatalf("expected 1 auth provider, got %d", len(caps.AuthProviders))
	}
	if caps.AuthProviders[0].ID != "my-corp" {
		t.Errorf("expected id my-corp, got %s", caps.AuthProviders[0].ID)
	}
}

func TestRegisterUnregisterAuthProviders(t *testing.T) {
	// Clean state
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{ID: "test-provider", Name: "Test Provider", UsesCallbackServer: false},
		},
	}
	bridge := NewBridge(nil, caps)

	// Before registration
	if p := ai.GetOAuthProvider("test-provider"); p != nil {
		t.Fatal("provider should not exist before registration")
	}

	bridge.RegisterAuthProviders()

	// After registration
	p := ai.GetOAuthProvider("test-provider")
	if p == nil {
		t.Fatal("provider should exist after registration")
	}
	if p.ID() != "test-provider" {
		t.Errorf("expected id test-provider, got %s", p.ID())
	}
	if p.Name() != "Test Provider" {
		t.Errorf("expected name Test Provider, got %s", p.Name())
	}
	if p.UsesCallbackServer() {
		t.Error("expected UsesCallbackServer=false")
	}

	bridge.UnregisterAuthProviders()

	// After unregistration
	if p := ai.GetOAuthProvider("test-provider"); p != nil {
		t.Fatal("provider should not exist after unregistration")
	}
}
