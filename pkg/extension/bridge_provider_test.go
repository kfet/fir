package extension

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/models"
)

func TestValidateProviderID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"my-corp", false},
		{"acme-cloud", false},
		{"a1-b2", false},
		{"", true},
		{"1bad", true},
		{"BAD", true},
		{"has space", true},
		// Built-in collisions
		{"anthropic", true},
		{"openai", true},
		{"amazon-bedrock", true},
	}
	for _, tt := range tests {
		err := ValidateProviderID(tt.id, false)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateProviderID(%q) error=%v, wantErr=%v", tt.id, err, tt.wantErr)
		}
	}
	// Built-in override path is allowed.
	if err := ValidateProviderID("anthropic", true); err != nil {
		t.Errorf("ValidateProviderID(anthropic, allowOverride=true) unexpected err: %v", err)
	}
}

func TestRegisterUnregisterProviders(t *testing.T) {
	caps := &InitResult{
		Name: "ext-test",
		Providers: []ProviderSpec{{
			ID:               "test-cloud",
			DisplayName:      "Test Cloud",
			ShortName:        "tc",
			Priority:         500,
			KeyLink:          "https://example.com/keys",
			EnvKeys:          EnvKeysSpec{Primary: "TEST_CLOUD_API_KEY"},
			SupportsLiveList: true,
			Models: []ProviderModelSpec{
				{ID: "tc-flash", Name: "TC Flash", ContextWindow: 100_000},
			},
		}},
	}
	bridge := NewBridge(nil, caps)

	// Pre-state checks.
	if rec := ai.GetProviderRecord("test-cloud"); rec != nil {
		t.Fatalf("provider record should not exist before registration")
	}

	bridge.RegisterProviders()
	t.Cleanup(bridge.UnregisterProviders)

	rec := ai.GetProviderRecord("test-cloud")
	if rec == nil {
		t.Fatal("provider record missing after registration")
	}
	if rec.DisplayName != "Test Cloud" || rec.Source != "ext:ext-test" {
		t.Errorf("unexpected record: %+v", rec)
	}

	// Synthetic Api wired into ai.DefaultRegistry.
	if p := ai.DefaultRegistry.GetApiProvider("ext:test-cloud"); p == nil {
		t.Fatal("synthetic Api provider not registered")
	}

	// Model present and bound to synthetic Api.
	m := ai.GetModel("test-cloud", "tc-flash")
	if m == nil {
		t.Fatal("model not registered")
	}
	if m.API != "ext:test-cloud" {
		t.Errorf("model Api = %q, want ext:test-cloud", m.API)
	}

	// Lister registered.
	if l := models.GetModelLister("test-cloud"); l == nil {
		t.Error("ModelLister not registered for live-listing provider")
	}

	bridge.UnregisterProviders()

	if rec := ai.GetProviderRecord("test-cloud"); rec != nil {
		t.Error("provider record should be cleared after Unregister")
	}
	if p := ai.DefaultRegistry.GetApiProvider("ext:test-cloud"); p != nil {
		t.Error("synthetic Api should be cleared after Unregister")
	}
	if m := ai.GetModel("test-cloud", "tc-flash"); m != nil {
		t.Error("model should be cleared after Unregister")
	}
	if l := models.GetModelLister("test-cloud"); l != nil {
		t.Error("ModelLister should be cleared after Unregister")
	}
}

// TestRegisterProvidersApiPassthrough verifies that ProviderSpec.Api set to a
// built-in wire protocol skips the synthetic ext:<id> allocation and stream
// adapter registration, while still wiring the provider record + models +
// lister. Used by metadata-only ext-shipped providers that ride on a
// host stream function.
func TestRegisterProvidersApiPassthrough(t *testing.T) {
	// Make sure built-in Api adapters are registered for this test process.
	providers.RegisterDefaultProviders()

	// Pick any built-in Api so we can confirm we don't trample it.
	const builtinApi ai.Api = ai.ApiOpenAICompletions
	preBuiltin := ai.DefaultRegistry.GetApiProvider(builtinApi)
	if preBuiltin == nil {
		t.Fatalf("test precondition: built-in Api %q not registered", builtinApi)
	}

	caps := &InitResult{
		Name: "ext-passthrough",
		Providers: []ProviderSpec{{
			ID:          "test-passthrough",
			Api:         string(builtinApi),
			DisplayName: "Passthrough",
			Models: []ProviderModelSpec{
				{ID: "tp-1", Name: "TP One", ContextWindow: 32_000},
			},
		}},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterProviders()
	t.Cleanup(bridge.UnregisterProviders)

	// Provider record landed.
	if rec := ai.GetProviderRecord("test-passthrough"); rec == nil {
		t.Fatal("provider record missing after passthrough registration")
	}

	// No synthetic Api should have been allocated.
	if p := ai.DefaultRegistry.GetApiProvider("ext:test-passthrough"); p != nil {
		t.Error("passthrough mode should NOT allocate a synthetic ext:<id> Api")
	}

	// Built-in Api still in place — bridge must not have replaced it.
	if got := ai.DefaultRegistry.GetApiProvider(builtinApi); got != preBuiltin {
		t.Errorf("built-in Api provider %q changed during passthrough register", builtinApi)
	}

	// Model is bound to the built-in Api, not the synthetic one.
	m := ai.GetModel("test-passthrough", "tp-1")
	if m == nil {
		t.Fatal("model not registered under passthrough provider")
	}
	if m.API != builtinApi {
		t.Errorf("model.API = %q, want %q", m.API, builtinApi)
	}

	// Unregister: must NOT touch the built-in Api.
	bridge.UnregisterProviders()
	if got := ai.DefaultRegistry.GetApiProvider(builtinApi); got != preBuiltin {
		t.Errorf("Unregister cleared the built-in Api %q (must not happen)", builtinApi)
	}
	if rec := ai.GetProviderRecord("test-passthrough"); rec != nil {
		t.Error("provider record should be cleared after Unregister")
	}
	if m := ai.GetModel("test-passthrough", "tp-1"); m != nil {
		t.Error("model should be cleared after Unregister")
	}
}
