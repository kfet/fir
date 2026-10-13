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
		ContextWindow: 200000,
		MaxTokens:     8192,
	})
	// Cleanups run LIFO, so this assertion (registered first) runs LAST —
	// after the rollback below. It pins the invariant the rollback exists to
	// respect: this test registers a scratch model under the REAL
	// amazon-bedrock provider, so it must remove exactly that model and
	// nothing else. The previous rollback was
	// ai.UnregisterProviderModels("amazon-bedrock"), which deletes the whole
	// provider key from the registry — taking all 118 genuine Bedrock models
	// with it. Since ai.GetProviders() derives from those keys, amazon-bedrock
	// then stopped being a "built-in" provider for every later test in the
	// package, and catalog-overlay validation (which may only add models to
	// built-in providers) rejected the embedded catalog with the misleading
	// `"baseUrl" is required when defining custom models`. That failure
	// surfaced only in a full-package run, never when the test was run alone.
	t.Cleanup(func() {
		if n := len(ai.GetModels("amazon-bedrock")); n == 0 {
			t.Errorf("rollback stripped every amazon-bedrock model; it must remove only this test's scratch model")
		}
	})
	t.Cleanup(func() { ai.UnregisterModel("amazon-bedrock", "anthropic.claude-3-5-sonnet") })

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
	if clone.BaseURL != "" {
		t.Errorf("clone baseURL = %q, want empty (region travels with the credential)", clone.BaseURL)
	}
	if clone.ID != "arn:aws:bedrock:eu-west-1:123:inference-profile/eu.anthropic.claude-3-5-sonnet" {
		t.Errorf("clone ID (ARN override) = %q", clone.ID)
	}
}

// TestBedrockCatalogHasNoHardcodedEndpoint pins the root fix for the
// default-account region bug: the generated catalog used to hardcode the
// us-east-1 endpoint on every Bedrock model, so an account configured for
// another region (--region, AWS_REGION, profile region) signed for that
// region but sent requests to us-east-1, and AWS rejected every call with
// "Credential should be scoped to a valid region". Catalog Bedrock models
// must carry no BaseURL so skipstone derives the endpoint from the resolved
// region, and the registry must not invent one for any account.
func TestBedrockCatalogHasNoHardcodedEndpoint(t *testing.T) {
	catalog := ai.GetModels("amazon-bedrock")
	if len(catalog) == 0 {
		t.Fatal("no amazon-bedrock catalog models")
	}
	for _, m := range catalog {
		if m.BaseURL != "" {
			t.Errorf("catalog model %s has hardcoded BaseURL %q", m.ID, m.BaseURL)
		}
	}

	authStore := auth.NewInMemoryAuthStorage(auth.AuthStorageData{
		"amazon-bedrock": {
			Type:  auth.CredentialTypeAWSIAM,
			Extra: map[string]any{"mode": "profile", "profile": "dev", "region": "us-west-2"},
		},
		"amazon-bedrock#eu": {
			Type:  auth.CredentialTypeAWSIAM,
			Label: "eu",
			Extra: map[string]any{"mode": "profile", "profile": "other", "region": "eu-west-1"},
		},
	})
	r := NewModelRegistry(authStore, "")
	seen := 0
	for _, m := range r.GetAll() {
		if base, _ := auth.SplitSlot(m.Provider); base != "amazon-bedrock" {
			continue
		}
		seen++
		if m.BaseURL != "" {
			t.Errorf("%s/%s BaseURL = %q, want empty", m.Provider, m.ID, m.BaseURL)
		}
	}
	if seen == 0 {
		t.Fatal("registry exposed no Bedrock models")
	}
}
