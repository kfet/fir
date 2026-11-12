package ai

import (
	"context"
	"testing"

	"github.com/kfet/pinoauth"
)

// fakeOAuthProvider is a minimal OAuthProvider for registry tests.
type fakeOAuthProvider struct {
	id   string
	name string
}

func (p *fakeOAuthProvider) ID() string               { return p.id }
func (p *fakeOAuthProvider) Name() string             { return p.name }
func (p *fakeOAuthProvider) UsesCallbackServer() bool { return true }
func (p *fakeOAuthProvider) Login(_ context.Context, _ pinoauth.LoginCallbacks) (*OAuthCredentials, error) {
	return nil, nil
}
func (p *fakeOAuthProvider) RefreshToken(_ context.Context, _ *OAuthCredentials) (*OAuthCredentials, error) {
	return nil, nil
}
func (p *fakeOAuthProvider) GetAPIKey(_ *OAuthCredentials) string { return "" }
func (p *fakeOAuthProvider) ListModels(_ context.Context, _ *OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *fakeOAuthProvider) ModifyModels(models []*Model, _ *OAuthCredentials) []*Model {
	return models
}
func (p *fakeOAuthProvider) ModelDefaults(_ string, _ []*Model) *Model { return nil }

// resetRegistryForTest snapshots and restores the registry around a test so
// any providers registered elsewhere are preserved.
func resetRegistryForTest(t *testing.T) {
	t.Helper()
	oauthMu.Lock()
	saved := oauthRegistry
	oauthRegistry = map[string][]OAuthProvider{}
	oauthMu.Unlock()
	t.Cleanup(func() {
		oauthMu.Lock()
		oauthRegistry = saved
		oauthMu.Unlock()
	})
}

func TestUnregisterOAuthProviderInstance(t *testing.T) {
	resetRegistryForTest(t)
	a := &fakeOAuthProvider{id: "x", name: "A"}
	b := &fakeOAuthProvider{id: "x", name: "B"}
	RegisterOAuthProvider(a)
	RegisterOAuthProvider(b)
	UnregisterOAuthProviderInstance(b)
	if got := GetOAuthProvider("x"); got != a {
		t.Fatalf("after removing b want a, got %v", got)
	}
	RegisterOAuthProvider(b)
	UnregisterOAuthProviderInstance(a)
	if got := GetOAuthProvider("x"); got != b {
		t.Fatalf("after removing a want b, got %v", got)
	}
	RegisterOAuthProvider(b) // re-register moves to top, no duplicate
	UnregisterOAuthProviderInstance(b)
	if got := GetOAuthProvider("x"); got != nil {
		t.Fatalf("want empty, got %v", got)
	}
	UnregisterOAuthProviderInstance(b) // absent: no-op
}

func TestRegisterAndGetOAuthProvider(t *testing.T) {
	resetRegistryForTest(t)

	if got := GetOAuthProvider("nonexistent"); got != nil {
		t.Errorf("expected nil for unknown provider, got %v", got)
	}

	p := &fakeOAuthProvider{id: "fake", name: "Fake"}
	RegisterOAuthProvider(p)

	got := GetOAuthProvider("fake")
	if got == nil {
		t.Fatal("expected provider after register")
	}
	if got.ID() != "fake" || got.Name() != "Fake" {
		t.Errorf("got id=%q name=%q", got.ID(), got.Name())
	}
}

func TestRegisterOAuthProvider_LastWriteWins(t *testing.T) {
	resetRegistryForTest(t)

	RegisterOAuthProvider(&fakeOAuthProvider{id: "dup", name: "First"})
	RegisterOAuthProvider(&fakeOAuthProvider{id: "dup", name: "Second"})

	if got := GetOAuthProvider("dup"); got == nil || got.Name() != "Second" {
		t.Errorf("expected last write to win, got %v", got)
	}
}

func TestUnregisterOAuthProvider(t *testing.T) {
	resetRegistryForTest(t)

	RegisterOAuthProvider(&fakeOAuthProvider{id: "to-remove"})
	UnregisterOAuthProvider("to-remove")

	if got := GetOAuthProvider("to-remove"); got != nil {
		t.Errorf("expected nil after unregister, got %v", got)
	}
}

func TestResetOAuthProviders(t *testing.T) {
	resetRegistryForTest(t)

	RegisterOAuthProvider(&fakeOAuthProvider{id: "a"})
	RegisterOAuthProvider(&fakeOAuthProvider{id: "b"})
	ResetOAuthProviders()

	if len(GetOAuthProviders()) != 0 {
		t.Errorf("expected empty after reset, got %d", len(GetOAuthProviders()))
	}
}

func TestGetOAuthProviders_Sorted(t *testing.T) {
	resetRegistryForTest(t)
	ResetOAuthProviders()

	RegisterOAuthProvider(&fakeOAuthProvider{id: "zeta"})
	RegisterOAuthProvider(&fakeOAuthProvider{id: "alpha"})
	RegisterOAuthProvider(&fakeOAuthProvider{id: "mu"})

	ps := GetOAuthProviders()
	if len(ps) != 3 {
		t.Fatalf("expected 3, got %d", len(ps))
	}
	want := []string{"alpha", "mu", "zeta"}
	for i, w := range want {
		if ps[i].ID() != w {
			t.Errorf("idx %d: want %q got %q", i, w, ps[i].ID())
		}
	}
}
