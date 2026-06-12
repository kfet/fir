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
