package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
)

// revokeOnRotateProvider is a stub OAuth provider that resolves a stored
// credential to its access token. It deliberately FAILS any refresh grant:
// this test asserts that a session recovers by re-reading the credential
// another process rotated, not by spending a grant of its own.
type revokeOnRotateProvider struct{ id string }

func (p *revokeOnRotateProvider) ID() string               { return p.id }
func (p *revokeOnRotateProvider) Name() string             { return "Revoke-on-rotate " + p.id }
func (p *revokeOnRotateProvider) UsesCallbackServer() bool { return false }
func (p *revokeOnRotateProvider) Login(context.Context, pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *revokeOnRotateProvider) RefreshToken(context.Context, *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return nil, errRefreshNotExpected
}
func (p *revokeOnRotateProvider) GetAPIKey(c *ai.OAuthCredentials) string {
	if c == nil {
		return ""
	}
	return c.Access
}
func (p *revokeOnRotateProvider) ListModels(context.Context, *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *revokeOnRotateProvider) ModifyModels([]*ai.Model, *ai.OAuthCredentials) []*ai.Model {
	return nil
}
func (p *revokeOnRotateProvider) ModelDefaults(string, []*ai.Model) *ai.Model { return nil }

type constErr string

func (e constErr) Error() string { return string(e) }

const errRefreshNotExpected = constErr("refresh grant must not be spent on a rotation this process can simply re-read")

// writeOAuthCredential rewrites an auth.json holding a single OAuth slot whose
// access token is `access`. Expires is deliberately far in the future: the
// whole point of revoke-on-rotate is that the token dies while fir still
// believes it is valid, so nothing in the expiry-driven path may fire.
func writeOAuthCredential(t *testing.T, path, slot, access string) {
	t.Helper()
	b, err := json.MarshalIndent(auth.AuthStorageData{
		slot: {
			Type:    auth.CredentialTypeOAuth,
			Access:  access,
			Refresh: "refresh-token",
			Expires: time.Now().Add(8 * time.Hour).UnixMilli(),
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestAnthropic_RevokedTokenRecoveredByRereadingAuthFile is the end-to-end
// regression test for the revoke-on-rotate wedge.
//
// Anthropic revokes the previous access token the instant a refresh grant
// rotates the credential — the old token starts 401ing immediately, long
// before its stated expiry. So when `fir auth refresh` runs from cron (or a
// second fir session refreshes), every live session is holding a dead token
// while its cached `expires` still reads hours out. Before the fix, the
// RefreshAPIKey callback resolved from that same in-memory cache, returned the
// identical revoked token, the provider saw no change and gave up — the
// session 401'd forever against an auth.json that was perfectly healthy.
//
// The fake upstream here models the revocation exactly: the revoked token gets
// HTTP 401, the rotated token gets a normal SSE response. The credential is
// rotated on disk between the two attempts, by a writer this process never
// tells its AuthStorage about — just as cron would.
func TestAnthropic_RevokedTokenRecoveredByRereadingAuthFile(t *testing.T) {
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	const (
		slot         = "anthropic"
		revokedToken = "oat-revoked"
		rotatedToken = "oat-rotated"
	)
	ai.RegisterOAuthProvider(&revokeOnRotateProvider{id: slot})

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeOAuthCredential(t, authPath, slot, revokedToken)

	storage := auth.NewAuthStorage(authPath)

	successData := loadFixture(t, "anthropic_simple_response.sse")
	var seenTokens []string
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		seenTokens = append(seenTokens, token)
		if token != "Bearer "+rotatedToken {
			// Upstream revoked this token the moment the credential rotated.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"OAuth access token has been revoked"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(successData)
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.Provider = slot
	// Mark the model OAuth-backed so the request authenticates with
	// `Authorization: Bearer`, the way a Claude Pro/Max login does.
	if model.Headers == nil {
		model.Headers = map[string]string{}
	}
	model.Headers["x-anthropic-oauth-beta-prefix"] = "oauth-2025-04-20"

	// The session resolved its key before the rotation and is holding it.
	apiKey := storage.GetApiKey(slot)
	if apiKey != revokedToken {
		t.Fatalf("precondition: resolved key = %q, want %q", apiKey, revokedToken)
	}

	// Cron runs `fir auth refresh`: the credential rotates on disk and the
	// token this session holds is revoked upstream. Note that `storage` is
	// never told — it must notice by itself.
	writeOAuthCredential(t, authPath, slot, rotatedToken)

	refreshCalls := 0
	opts := &ai.StreamOptions{
		APIKey: apiKey,
		// Exactly the wiring pkg/session/sdk.go installs.
		RefreshAPIKey: func(provider string) string {
			refreshCalls++
			return storage.RefreshApiKey(provider)
		},
	}

	stream := StreamAnthropic(context.Background(), model, ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)},
	}, opts)
	_ = collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason == ai.StopReasonError {
		t.Fatalf("session did not recover from the rotation: %s", result.ErrorMessage)
	}
	if refreshCalls == 0 {
		t.Error("RefreshAPIKey was never consulted on the 401")
	}
	if len(seenTokens) < 2 {
		t.Fatalf("expected a retry (2+ upstream attempts), got %d: %v", len(seenTokens), seenTokens)
	}
	if seenTokens[0] != "Bearer "+revokedToken {
		t.Errorf("first attempt used %q, want the revoked token", seenTokens[0])
	}
	if seenTokens[len(seenTokens)-1] != "Bearer "+rotatedToken {
		t.Errorf("retry used %q, want the rotated token re-read from auth.json",
			seenTokens[len(seenTokens)-1])
	}
}
