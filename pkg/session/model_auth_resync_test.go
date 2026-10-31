package session

import (
	"context"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
)

type headerTagOAuth struct{ id string }

func (p *headerTagOAuth) ID() string               { return p.id }
func (p *headerTagOAuth) Name() string             { return p.id }
func (p *headerTagOAuth) UsesCallbackServer() bool { return false }
func (p *headerTagOAuth) Login(context.Context, pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *headerTagOAuth) RefreshToken(_ context.Context, c *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return c, nil
}
func (p *headerTagOAuth) GetAPIKey(c *ai.OAuthCredentials) string { return c.Access }
func (p *headerTagOAuth) ListModels(context.Context, *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *headerTagOAuth) ModifyModels(ms []*ai.Model, _ *ai.OAuthCredentials) []*ai.Model {
	out := make([]*ai.Model, len(ms))
	for i, m := range ms {
		out[i] = m
		if m.Provider == p.id {
			c := *m
			c.Headers = map[string]string{"x-anthropic-oauth-beta-prefix": "oauth"}
			out[i] = &c
		}
	}
	return out
}
func (p *headerTagOAuth) ModelDefaults(string, []*ai.Model) *ai.Model { return nil }

// A model selected while the provider held an API key must be re-resolved
// before the next turn once an OAuth credential replaces it, or the turn
// sends the OAuth token as an API key (401 invalid x-api-key).
func TestResyncModelAuth_PicksUpOAuthLogin(t *testing.T) {
	saved := ai.GetOAuthProvider("anthropic")
	ai.RegisterOAuthProvider(&headerTagOAuth{id: "anthropic"})
	t.Cleanup(func() {
		if saved != nil {
			ai.RegisterOAuthProvider(saved)
		} else {
			ai.UnregisterOAuthProvider("anthropic")
		}
	})

	s, _ := newTestAgentSession(t)
	defer s.Close()
	reg := s.ModelRegistryRef()
	st := reg.AuthStorage()
	if err := st.Set("anthropic", auth.AuthCredential{Type: auth.CredentialTypeAPIKey, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	id := ai.GetModels("anthropic")[0].ID
	m := reg.Find("anthropic", id)
	if err := s.SetModel(m); err != nil {
		t.Fatal(err)
	}
	if s.Model().Headers["x-anthropic-oauth-beta-prefix"] != "" {
		t.Fatal("precondition: API-key model must not be OAuth-tagged")
	}

	if err := st.Set("anthropic", auth.AuthCredential{Type: auth.CredentialTypeOAuth, Access: "oat",
		Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	s.resyncModelAuth()

	got := s.Model()
	if got.Provider != "anthropic" || got.ID != id {
		t.Fatalf("model changed identity: %s/%s", got.Provider, got.ID)
	}
	if got.Headers["x-anthropic-oauth-beta-prefix"] == "" {
		t.Fatal("session model not re-resolved after OAuth login")
	}
}

// With no rebuild, resync must not re-consult the endpoint hook (an extension
// round trip) on every prompt.
func TestResyncModelAuth_NoopWithoutRebuild(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()
	calls := 0
	s.hooks.Store(&AgentSessionHooks{ResolveModelEndpoint: func(*ai.Model) *ModelEndpointCorrection {
		calls++
		return nil
	}})
	reg := s.ModelRegistryRef()
	if err := reg.AuthStorage().Set("anthropic", auth.AuthCredential{Type: auth.CredentialTypeAPIKey, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel(reg.Find("anthropic", ai.GetModels("anthropic")[0].ID)); err != nil {
		t.Fatal(err)
	}
	s.resyncModelAuth()
	s.resyncModelAuth()
	if calls != 1 {
		t.Fatalf("endpoint hook called %d times, want 1 (SetModel only)", calls)
	}
}
