package oauth

import (
	"testing"
	"time"
)

func TestGetProvider_BuiltIns(t *testing.T) {
	expectedIDs := []string{
		"anthropic",
		"github-copilot",
		// "google-gemini-cli" is now provided by the gemini_cli_auth builtin extension.
		// "google-antigravity" is now provided by the antigravity_auth builtin extension.
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
	if len(providers) < 3 {
		t.Errorf("expected at least 3 providers, got %d", len(providers))
	}
}

func TestGetProviderInfoList(t *testing.T) {
	infos := GetProviderInfoList()
	if len(infos) < 3 {
		t.Errorf("expected at least 3 provider infos, got %d", len(infos))
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
