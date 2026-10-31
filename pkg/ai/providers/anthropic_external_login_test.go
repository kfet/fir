package providers

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/pinoauth"
)

// oauthTaggingProvider mimics the anthropic auth extension: ModifyModels
// tags the provider's models with the OAuth beta header, which is what makes
// the request authenticate with `Authorization: Bearer`.
type oauthTaggingProvider struct{ id string }

func (p *oauthTaggingProvider) ID() string               { return p.id }
func (p *oauthTaggingProvider) Name() string             { return "OAuth tagger " + p.id }
func (p *oauthTaggingProvider) UsesCallbackServer() bool { return false }
func (p *oauthTaggingProvider) Login(context.Context, pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *oauthTaggingProvider) RefreshToken(context.Context, *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return nil, errRefreshNotExpected
}
func (p *oauthTaggingProvider) GetAPIKey(c *ai.OAuthCredentials) string {
	if c == nil {
		return ""
	}
	return c.Access
}
func (p *oauthTaggingProvider) ListModels(context.Context, *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *oauthTaggingProvider) ModifyModels(ms []*ai.Model, _ *ai.OAuthCredentials) []*ai.Model {
	out := make([]*ai.Model, len(ms))
	for i, m := range ms {
		if m.Provider != p.id {
			out[i] = m
			continue
		}
		c := *m
		c.Headers = map[string]string{"x-anthropic-oauth-beta-prefix": "oauth-2025-04-20"}
		for k, v := range m.Headers {
			c.Headers[k] = v
		}
		out[i] = &c
	}
	return out
}
func (p *oauthTaggingProvider) ModelDefaults(string, []*ai.Model) *ai.Model { return nil }

// TestAnthropic_ExternalOAuthLoginReachesRunningRegistry is the regression
// test for: an OAuth login performed by ANOTHER fir process (new account, or
// replacing the default credential) made a running session send the OAuth
// token as `x-api-key` and fail with "401 invalid x-api-key". The registry
// had been built while the provider held an API key (or no key at all), so
// its models never got the OAuth marker, and nothing rebuilt it when
// auth.json changed on disk.
func TestAnthropic_ExternalOAuthLoginReachesRunningRegistry(t *testing.T) {
	t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")
	saved := ai.GetOAuthProvider("anthropic")
	ai.RegisterOAuthProvider(&oauthTaggingProvider{id: "anthropic"})
	t.Cleanup(func() {
		if saved != nil {
			ai.RegisterOAuthProvider(saved)
		} else {
			ai.UnregisterOAuthProvider("anthropic")
		}
	})

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeCreds := func(data auth.AuthStorageData) {
		t.Helper()
		// Another process writes auth.json; bump mtime so the change is
		// visible even on coarse-mtime filesystems.
		w := auth.NewAuthStorage(authPath)
		for k, c := range data {
			if err := w.Set(k, c); err != nil {
				t.Fatal(err)
			}
		}
		future := time.Now().Add(time.Duration(len(data)) * time.Second)
		if err := os.Chtimes(authPath, future, future); err != nil {
			t.Fatal(err)
		}
	}
	writeCreds(auth.AuthStorageData{"anthropic": {Type: auth.CredentialTypeAPIKey, Key: "sk-ant-api-old"}})

	storage := auth.NewAuthStorage(authPath)
	reg := models.NewModelRegistry(storage, filepath.Join(dir, "models.json"))
	modelID := ai.GetModels("anthropic")[0].ID
	if isOAuthModel(reg.Find("anthropic", modelID)) {
		t.Fatal("precondition: API-key model must not be OAuth-tagged")
	}

	oauth := func(tok, label string) auth.AuthCredential {
		return auth.AuthCredential{Type: auth.CredentialTypeOAuth, Access: tok, Refresh: "r", Label: label,
			Expires: time.Now().Add(8 * time.Hour).UnixMilli()}
	}
	teamSlot := auth.SlotKey("anthropic", "team")
	writeCreds(auth.AuthStorageData{
		"anthropic": oauth("oat-default", "me@example.com"),
		teamSlot:    oauth("oat-team", "me@example.com"),
	})

	for _, tc := range []struct{ provider, token string }{
		{"anthropic", "oat-default"},
		{teamSlot, "oat-team"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			// Exactly what a model switch (ACP session/set_model, /model) does.
			found := reg.Find(tc.provider, modelID)
			if found == nil {
				t.Fatalf("model %s/%s not found after external login", tc.provider, modelID)
			}

			var mu sync.Mutex
			var gotAuth, gotKey string
			srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(loadFixture(t, "anthropic_simple_response.sse"))
			})
			defer srv.Close()
			m := *found
			m.BaseURL = srv.URL

			stream := StreamAnthropic(context.Background(), &m, ai.Context{
				Messages: []ai.Message{ai.NewUserMsg("hi", 1000)},
			}, &ai.StreamOptions{APIKey: reg.RefreshApiKeyForProvider(tc.provider)})
			_ = collectEvents(t, stream)

			mu.Lock()
			defer mu.Unlock()
			if gotKey != "" {
				t.Errorf("OAuth token sent as x-api-key %q", gotKey)
			}
			if gotAuth != "Bearer "+tc.token {
				t.Errorf("Authorization = %q, want Bearer %s", gotAuth, tc.token)
			}
		})
	}
}
