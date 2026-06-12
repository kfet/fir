package models

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

// TestModelFanOutPerAccount verifies loadBuiltInModels clones a provider's
// models under each additional named account's composite provider id.
func TestModelFanOutPerAccount(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "fan-base",
		Name:          "Fan Base",
		Provider:      "fanmodel-prov",
		API:           "anthropic-messages",
		BaseURL:       "https://example.com",
		ContextWindow: 100000,
		MaxTokens:     4096,
	})
	t.Cleanup(func() { ai.UnregisterProviderModels("fanmodel-prov") })

	authStore := auth.NewInMemoryAuthStorage(auth.AuthStorageData{
		"fanmodel-prov":      {Type: auth.CredentialTypeOAuth, Access: "a"},
		"fanmodel-prov#work": {Type: auth.CredentialTypeOAuth, Access: "b", Label: "work@x.com"},
	})
	r := NewModelRegistry(authStore, "")

	var base, clone *ai.Model
	for _, m := range r.GetAll() {
		switch m.Provider {
		case "fanmodel-prov":
			if m.ID == "fan-base" {
				base = m
			}
		case "fanmodel-prov#work":
			if m.ID == "fan-base" {
				clone = m
			}
		}
	}
	if base == nil {
		t.Fatal("default-account model missing")
	}
	if clone == nil {
		t.Fatal("named-account clone missing")
	}
	if clone.Name != "Fan Base (work@x.com)" {
		t.Errorf("clone name = %q want labelled", clone.Name)
	}
	if clone.BaseURL != base.BaseURL {
		t.Errorf("clone baseURL = %q want %q", clone.BaseURL, base.BaseURL)
	}
}

// TestBedrockAccountModelOverrides verifies per-account region (regional
// endpoint) and model-id/ARN overrides are applied to cloned Bedrock models.
func TestBedrockAccountModelOverrides(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "anthropic.claude-3-5-sonnet",
		Name:          "Claude Sonnet (Bedrock)",
		Provider:      "amazon-bedrock",
		API:           "amazon-bedrock",
		BaseURL:       "https://bedrock-runtime.us-east-1.amazonaws.com",
		ContextWindow: 200000,
		MaxTokens:     8192,
	})
	t.Cleanup(func() { ai.UnregisterProviderModels("amazon-bedrock") })

	authStore := auth.NewInMemoryAuthStorage(auth.AuthStorageData{
		"amazon-bedrock": {Type: auth.CredentialTypeAWSIAM, Extra: map[string]any{"mode": "profile", "profile": "default"}},
		"amazon-bedrock#work": {
			Type:  auth.CredentialTypeAWSIAM,
			Label: "work",
			Extra: map[string]any{
				"mode":    "profile",
				"profile": "work",
				"region":  "eu-west-1",
				"modelOverrides": map[string]any{
					"anthropic.claude-3-5-sonnet": "arn:aws:bedrock:eu-west-1:123:inference-profile/eu.anthropic.claude-3-5-sonnet",
				},
			},
		},
	})
	r := NewModelRegistry(authStore, "")

	var clone *ai.Model
	for _, m := range r.GetAll() {
		if m.Provider == "amazon-bedrock#work" && m.Name == "Claude Sonnet (Bedrock) (work)" {
			clone = m
			break
		}
	}
	if clone == nil {
		t.Fatal("work-account Bedrock clone missing")
	}
	if clone.BaseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("clone regional baseURL = %q", clone.BaseURL)
	}
	if clone.ID != "arn:aws:bedrock:eu-west-1:123:inference-profile/eu.anthropic.claude-3-5-sonnet" {
		t.Errorf("clone ID (ARN override) = %q", clone.ID)
	}
}
