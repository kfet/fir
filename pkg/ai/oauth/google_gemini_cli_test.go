package oauth

import (
	"encoding/json"
	"testing"
)

func TestGeminiCLIProvider_IDAndName(t *testing.T) {
	p := &GeminiCLIProvider{}
	if p.ID() != "google-gemini-cli" {
		t.Errorf("ID() = %q", p.ID())
	}
	if p.Name() != "Google Cloud Code Assist (Gemini CLI)" {
		t.Errorf("Name() = %q", p.Name())
	}
	if !p.UsesCallbackServer() {
		t.Error("expected UsesCallbackServer() == true")
	}
}

func TestGeminiCLIProvider_GetAPIKey(t *testing.T) {
	p := &GeminiCLIProvider{}
	creds := &Credentials{
		Access: "ya29.gemini-token",
		Extra:  map[string]any{"projectId": "my-gemini-project"},
	}
	key := p.GetAPIKey(creds)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(key), &parsed); err != nil {
		t.Fatalf("GetAPIKey returned invalid JSON: %v", err)
	}
	if parsed["token"] != "ya29.gemini-token" {
		t.Errorf("token = %q", parsed["token"])
	}
	if parsed["projectId"] != "my-gemini-project" {
		t.Errorf("projectId = %q", parsed["projectId"])
	}
}

func TestGeminiCLIProvider_RefreshToken_MissingProjectID(t *testing.T) {
	p := &GeminiCLIProvider{}
	_, err := p.RefreshToken(&Credentials{Refresh: "rt-test"})
	if err == nil {
		t.Error("expected error when projectId is missing")
	}
}

func TestGeminiCLIProvider_ModifyModels(t *testing.T) {
	p := &GeminiCLIProvider{}
	result := p.ModifyModels(nil, nil)
	if result != nil {
		t.Error("expected nil returned")
	}
}

func TestGeminiCLIClientID_Decoded(t *testing.T) {
	if geminiCLIClientID == "" {
		t.Error("geminiCLIClientID should be decoded")
	}
	if len(geminiCLIClientID) < 20 {
		t.Errorf("clientID suspiciously short: %q", geminiCLIClientID)
	}
}

func TestGeminiCLIClientSecret_Decoded(t *testing.T) {
	if geminiCLIClientSecret == "" {
		t.Error("geminiCLIClientSecret should be decoded")
	}
}

func TestGetDefaultTier(t *testing.T) {
	tests := []struct {
		name  string
		tiers []map[string]any
		want  string
	}{
		{"nil tiers", nil, tierLegacy},
		{"empty tiers", []map[string]any{}, tierLegacy},
		{
			"has default",
			[]map[string]any{
				{"id": "free-tier", "isDefault": false},
				{"id": "standard-tier", "isDefault": true},
			},
			"standard-tier",
		},
		{
			"no default flag",
			[]map[string]any{
				{"id": "free-tier"},
				{"id": "standard-tier"},
			},
			tierLegacy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getDefaultTier(tt.tiers)
			if got != tt.want {
				t.Errorf("getDefaultTier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsVpcScAffectedUser(t *testing.T) {
	affected := map[string]any{
		"error": map[string]any{
			"details": []any{
				map[string]any{"reason": "SECURITY_POLICY_VIOLATED"},
			},
		},
	}
	if !isVpcScAffectedUser(affected) {
		t.Error("expected true for VPC SC affected user")
	}

	notAffected := map[string]any{
		"error": map[string]any{
			"details": []any{
				map[string]any{"reason": "OTHER_REASON"},
			},
		},
	}
	if isVpcScAffectedUser(notAffected) {
		t.Error("expected false for non-VPC SC error")
	}

	noError := map[string]any{"foo": "bar"}
	if isVpcScAffectedUser(noError) {
		t.Error("expected false for no error field")
	}
}

func TestGeminiCLIScopes(t *testing.T) {
	if len(geminiCLIScopes) != 3 {
		t.Errorf("expected 3 scopes, got %d", len(geminiCLIScopes))
	}
}

// Verify GeminiCLIProvider implements the Provider interface.
var _ Provider = (*GeminiCLIProvider)(nil)
