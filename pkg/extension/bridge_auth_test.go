package extension

import (
	"testing"

	"github.com/kfet/fir/pkg/ai/oauth"
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
		{"google-gemini-cli", true},
		{"google-antigravity", true},
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
	oauth.ResetProviders()
	defer oauth.ResetProviders()

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{ID: "test-provider", Name: "Test Provider", UsesCallbackServer: false},
		},
	}
	bridge := NewBridge(nil, caps)

	// Before registration
	if p := oauth.GetProvider("test-provider"); p != nil {
		t.Fatal("provider should not exist before registration")
	}

	bridge.RegisterAuthProviders()

	// After registration
	p := oauth.GetProvider("test-provider")
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
	if p := oauth.GetProvider("test-provider"); p != nil {
		t.Fatal("provider should not exist after unregistration")
	}
}

func TestFrontmatterAuthProviderMismatch(t *testing.T) {
	cfg := ExtProcConfig{
		Name:          "test",
		AuthProviders: []string{"old-provider"},
	}
	caps := &InitResult{
		Name: "test",
		AuthProviders: []AuthProviderSpec{
			{ID: "new-provider", Name: "New"},
		},
	}

	mm := CheckFrontmatter(cfg, caps)
	if mm.Empty() {
		t.Fatal("expected mismatch")
	}
	if len(mm.MissingAuthProviders) != 1 || mm.MissingAuthProviders[0] != "new-provider" {
		t.Errorf("expected missing new-provider, got %v", mm.MissingAuthProviders)
	}
	if len(mm.ExtraAuthProviders) != 1 || mm.ExtraAuthProviders[0] != "old-provider" {
		t.Errorf("expected extra old-provider, got %v", mm.ExtraAuthProviders)
	}
}

func TestFrontmatterAuthProviderMatch(t *testing.T) {
	cfg := ExtProcConfig{
		Name:          "test",
		AuthProviders: []string{"my-provider"},
	}
	caps := &InitResult{
		Name: "test",
		AuthProviders: []AuthProviderSpec{
			{ID: "my-provider", Name: "My Provider"},
		},
	}

	mm := CheckFrontmatter(cfg, caps)
	if len(mm.MissingAuthProviders) != 0 || len(mm.ExtraAuthProviders) != 0 {
		t.Errorf("expected no auth provider mismatch, got missing=%v extra=%v",
			mm.MissingAuthProviders, mm.ExtraAuthProviders)
	}
}
