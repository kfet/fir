package oauth

import (
	"fmt"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

func TestGetProvider_BuiltIns(t *testing.T) {
	expectedIDs := []string{
		"anthropic",
		"github-copilot",
		"google-gemini-cli",
		"google-antigravity",
		"openai-codex",
	}
	for _, id := range expectedIDs {
		p := GetProvider(id)
		if p == nil {
			t.Errorf("GetProvider(%q) = nil, expected registered provider", id)
		}
	}
}

func TestGetProvider_Unknown(t *testing.T) {
	p := GetProvider("nonexistent-provider")
	if p != nil {
		t.Error("expected nil for unknown provider")
	}
}

func TestGetProviders(t *testing.T) {
	providers := GetProviders()
	if len(providers) < 5 {
		t.Errorf("expected at least 5 providers, got %d", len(providers))
	}
}

func TestGetProviderInfoList(t *testing.T) {
	infos := GetProviderInfoList()
	if len(infos) < 5 {
		t.Errorf("expected at least 5 provider infos, got %d", len(infos))
	}
	for _, info := range infos {
		if info.ID == "" {
			t.Error("provider info has empty ID")
		}
		if info.Name == "" {
			t.Error("provider info has empty Name")
		}
		if !info.Available {
			t.Errorf("provider %q should be available", info.ID)
		}
	}
}

func TestIsExpired(t *testing.T) {
	// Not expired (far in future)
	creds := &Credentials{Expires: time.Now().UnixMilli() + 60*60*1000}
	if isExpired(creds) {
		t.Error("should not be expired")
	}

	// Expired (in the past)
	creds = &Credentials{Expires: time.Now().UnixMilli() - 1000}
	if !isExpired(creds) {
		t.Error("should be expired")
	}

	// No expiry set
	creds = &Credentials{Expires: 0}
	if isExpired(creds) {
		t.Error("zero expiry should not be considered expired")
	}
}

// stubProvider is a minimal Provider implementation for testing.
type stubProvider struct {
	id   string
	name string
}

func (s *stubProvider) ID() string                               { return s.id }
func (s *stubProvider) Name() string                             { return s.name }
func (s *stubProvider) Login(_ LoginCallbacks) (*Credentials, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *stubProvider) UsesCallbackServer() bool                 { return false }
func (s *stubProvider) GetAPIKey(creds *Credentials) string      { return "stub-key" }
func (s *stubProvider) RefreshToken(*Credentials) (*Credentials, error) {
	return &Credentials{}, nil
}
func (s *stubProvider) ModifyModels(models []*ai.Model, _ *Credentials) []*ai.Model {
	return models
}

func TestUnregisterProvider_Custom(t *testing.T) {
	defer ResetProviders() // restore built-ins after test

	stub := &stubProvider{id: "test-custom", name: "Test Custom"}
	RegisterProvider(stub)
	if GetProvider("test-custom") == nil {
		t.Fatal("expected custom provider to be registered")
	}

	UnregisterProvider("test-custom")
	if GetProvider("test-custom") != nil {
		t.Error("expected custom provider to be removed after unregister")
	}
}

func TestUnregisterProvider_BuiltIn_RestoresDefault(t *testing.T) {
	defer ResetProviders()

	original := GetProvider("anthropic")
	if original == nil {
		t.Fatal("anthropic should be a built-in provider")
	}

	// Override the built-in with a custom one
	stub := &stubProvider{id: "anthropic", name: "Override"}
	RegisterProvider(stub)
	if GetProvider("anthropic").Name() != "Override" {
		t.Fatal("expected override to take effect")
	}

	// Unregister should restore the built-in
	UnregisterProvider("anthropic")
	restored := GetProvider("anthropic")
	if restored == nil {
		t.Fatal("expected anthropic to be restored after unregister")
	}
	if restored.Name() == "Override" {
		t.Error("expected built-in implementation to be restored, not the override")
	}
}

func TestResetProviders(t *testing.T) {
	defer ResetProviders()

	// Register a custom provider
	stub := &stubProvider{id: "test-reset", name: "Test Reset"}
	RegisterProvider(stub)

	// Also override a built-in
	stub2 := &stubProvider{id: "anthropic", name: "Override"}
	RegisterProvider(stub2)

	ResetProviders()

	if GetProvider("test-reset") != nil {
		t.Error("custom provider should be removed after reset")
	}
	p := GetProvider("anthropic")
	if p == nil {
		t.Fatal("anthropic should be restored after reset")
	}
	if p.Name() == "Override" {
		t.Error("anthropic should be restored to built-in after reset")
	}
}
