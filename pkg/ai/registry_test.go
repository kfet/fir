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
