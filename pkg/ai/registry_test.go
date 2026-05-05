// Ported from: packages/ai/src/api-registry.ts
// Upstream hash: 1caadb2e
package ai

import (
	"context"
	"testing"
)

func dummyStream(ctx context.Context, model *Model, prompt Context, options *StreamOptions) *AssistantMessageEventStream {
	return NewAssistantMessageEventStream()
}

func dummyStreamSimple(ctx context.Context, model *Model, prompt Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	return NewAssistantMessageEventStream()
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	provider := &ApiProvider{
		Api:          ApiAnthropicMessages,
		Stream:       dummyStream,
		StreamSimple: dummyStreamSimple,
	}
	r.RegisterApiProvider(provider, "test")

	got := r.GetApiProvider(ApiAnthropicMessages)
	if got == nil {
		t.Fatal("expected provider")
	}
	if got.Api != ApiAnthropicMessages {
		t.Errorf("expected api %s, got %s", ApiAnthropicMessages, got.Api)
	}
}

func TestRegistry_GetUnregistered(t *testing.T) {
	r := NewRegistry()
	got := r.GetApiProvider("nonexistent")
	if got != nil {
		t.Error("expected nil for unregistered api")
	}
}

func TestRegistry_GetApiProviders(t *testing.T) {
	r := NewRegistry()
	r.RegisterApiProvider(&ApiProvider{Api: "api1", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "")
	r.RegisterApiProvider(&ApiProvider{Api: "api2", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "")

	providers := r.GetApiProviders()
	if len(providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(providers))
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	r.RegisterApiProvider(&ApiProvider{Api: "api1", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "src1")
	r.RegisterApiProvider(&ApiProvider{Api: "api2", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "src2")

	r.UnregisterApiProviders("src1")

	if r.GetApiProvider("api1") != nil {
		t.Error("expected api1 to be unregistered")
	}
	if r.GetApiProvider("api2") == nil {
		t.Error("expected api2 to still exist")
	}
}

func TestRegistry_Clear(t *testing.T) {
	r := NewRegistry()
	r.RegisterApiProvider(&ApiProvider{Api: "api1", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "")

	r.ClearApiProviders()

	if len(r.GetApiProviders()) != 0 {
		t.Error("expected empty registry after clear")
	}
}

// TestRegistry_ResetPreservesExtSources verifies that ResetApiProviders
// drops only built-in / unsourced entries and preserves Api providers
// shipped by extensions (sourceID prefix "ext-api:" or "builtin-ext-api:").
// Regression test for the bug where ModelRegistry.Refresh() — which calls
// ResetApiProviders — would nuke wire providers registered by extensions,
// causing "no API provider registered for api: <id>" mid-session.
func TestRegistry_ResetPreservesExtSources(t *testing.T) {
	r := NewRegistry()
	builtinCalls := 0
	r.SetBuiltInRegistrar(func(reg *Registry) {
		builtinCalls++
		reg.RegisterApiProvider(&ApiProvider{Api: "builtin-api", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "builtin")
	})
	r.RegisterApiProvider(&ApiProvider{Api: "builtin-api", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "builtin")
	r.RegisterApiProvider(&ApiProvider{Api: "dynamic-api", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "")
	r.RegisterApiProvider(&ApiProvider{Api: "ext-builtin-api", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "builtin-ext-api:my-builtin-ext")
	r.RegisterApiProvider(&ApiProvider{Api: "ext-user-api", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "ext-api:my-user-ext")
	// Synthetic Api shipped by a hosted provider extension (no separate Api spec).
	r.RegisterApiProvider(&ApiProvider{Api: "ext:demo-echo", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "ext:demo")
	r.RegisterApiProvider(&ApiProvider{Api: "ext:builtin-demo-echo", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "builtin-ext:builtin-demo")

	r.ResetApiProviders()

	if builtinCalls != 1 {
		t.Errorf("builtInRegistrar called %d times, want 1", builtinCalls)
	}
	if r.GetApiProvider("builtin-api") == nil {
		t.Error("built-in api should be re-registered after reset")
	}
	if r.GetApiProvider("dynamic-api") != nil {
		t.Error("unsourced dynamic api should be cleared on reset")
	}
	if r.GetApiProvider("ext-builtin-api") == nil {
		t.Error("builtin-ext-api Api must survive reset (ext owns its lifecycle)")
	}
	if r.GetApiProvider("ext-user-api") == nil {
		t.Error("ext-api Api must survive reset (ext owns its lifecycle)")
	}
	if r.GetApiProvider("ext:demo-echo") == nil {
		t.Error("synthetic ext:* hosted-provider Api must survive reset")
	}
	if r.GetApiProvider("ext:builtin-demo-echo") == nil {
		t.Error("synthetic builtin-ext:* hosted-provider Api must survive reset")
	}
}

func TestRegistry_Override(t *testing.T) {
	r := NewRegistry()
	p1 := &ApiProvider{Api: "api1", Stream: dummyStream, StreamSimple: dummyStreamSimple}
	p2 := &ApiProvider{Api: "api1", Stream: dummyStream, StreamSimple: dummyStreamSimple}

	r.RegisterApiProvider(p1, "src1")
	r.RegisterApiProvider(p2, "src2")

	got := r.GetApiProvider("api1")
	if got != p2 {
		t.Error("expected override to return latest provider")
	}
}

func TestMustGetApiProvider_Panics(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if rec := recover(); rec == nil {
			t.Error("expected panic")
		}
	}()
	MustGetApiProvider(r, "nonexistent")
}

func TestMustGetApiProvider_Success(t *testing.T) {
	r := NewRegistry()
	r.RegisterApiProvider(&ApiProvider{Api: "api1", Stream: dummyStream, StreamSimple: dummyStreamSimple}, "")
	p := MustGetApiProvider(r, "api1")
	if p.Api != "api1" {
		t.Error("expected api1")
	}
}

// TestIsBuiltInApi covers the source-prefix recognition used by extension
// validation. Builtin-scope extensions register Apis under
// "builtin-ext-api:<name>" so they're treated as core-equivalent and
// can't be overridden by user/project extensions.
func TestIsBuiltInApi(t *testing.T) {
	r := NewRegistry()
	r.RegisterApiProvider(&ApiProvider{Api: "core-api", Stream: dummyStream}, "builtin")
	r.RegisterApiProvider(&ApiProvider{Api: "ext-builtin-api", Stream: dummyStream}, "builtin-ext-api:my-builtin-ext")
	r.RegisterApiProvider(&ApiProvider{Api: "ext-user-api", Stream: dummyStream}, "ext-api:my-corp")

	if !r.IsBuiltInApi("core-api") {
		t.Error("core 'builtin' source should be recognised as built-in")
	}
	if !r.IsBuiltInApi("ext-builtin-api") {
		t.Error("'builtin-ext-api:' source should be recognised as built-in (security regression fix)")
	}
	if r.IsBuiltInApi("ext-user-api") {
		t.Error("'ext-api:' source must NOT be recognised as built-in — user/project extensions are not protected from override")
	}
	if r.IsBuiltInApi("nonexistent") {
		t.Error("missing api should not be built-in")
	}
}

// TestIsBuiltInProviderID parallels TestIsBuiltInApi for hosted-provider
// records (Source field instead of sourceID).
func TestIsBuiltInProviderID(t *testing.T) {
	t.Cleanup(func() {
		UnregisterProvider("test-core")
		UnregisterProvider("test-ext-builtin")
		UnregisterProvider("test-ext-user")
	})
	RegisterProvider(&RegisteredProvider{ID: "test-core", Source: "builtin"})
	RegisterProvider(&RegisteredProvider{ID: "test-ext-builtin", Source: "builtin-ext:foo"})
	RegisterProvider(&RegisteredProvider{ID: "test-ext-user", Source: "ext:bar"})

	if !IsBuiltInProviderID("test-core") {
		t.Error("'builtin' Source should be recognised as built-in")
	}
	if !IsBuiltInProviderID("test-ext-builtin") {
		t.Error("'builtin-ext:' Source should be recognised as built-in (security regression fix)")
	}
	if IsBuiltInProviderID("test-ext-user") {
		t.Error("'ext:' Source must NOT be recognised as built-in")
	}
	if IsBuiltInProviderID("nonexistent") {
		t.Error("missing provider should not be built-in")
	}
}
