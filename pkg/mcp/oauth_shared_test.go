package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/auth"
	"github.com/stretchr/testify/require"
)

// newSharedManager builds a Manager over its own AuthStorage on authPath —
// what a second fir process sharing ~/.config/fir/auth.json looks like.
func newSharedManager(t *testing.T, cfg ServerConfig, authPath string) (*Manager, *auth.AuthStorage) {
	t.Helper()
	storage := auth.NewAuthStorage(authPath)
	mgr := NewManager(map[string]ServerConfig{"srv": cfg}, false)
	mgr.SetAuth(storage)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, storage
}

// Two processes share one credential on a server that rotates refresh tokens.
// When one refreshes, the other's in-memory refresh token is spent. Its next
// 401 must adopt the rotated credential from disk — not replay the spent
// token, get invalid_grant, and delete the credential both depend on.
func TestMCPOAuth_SharedCredentialSurvivesRotationByAnotherProcess(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{rotateRefresh: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	cfg := srv.config(nil)
	authPath := filepath.Join(t.TempDir(), "auth.json")

	mgrA, _ := newSharedManager(t, cfg, authPath)
	require.NoError(t, loginAndConnect(t, mgrA, browserCallbacks(t, nil)))
	mgrB, storageB := newSharedManager(t, cfg, authPath)
	require.NoError(t, connectAndWait(t, mgrB))
	requireToolsWork(t, mgrB)

	as.revokeAccessTokens()
	requireToolsWork(t, mgrA) // A refreshes; the refresh token rotates
	requireToolsWork(t, mgrB) // B must adopt A's rotated credential

	_, refreshes, _ := as.counters()
	require.Equal(t, 1, refreshes, "B must not spend a grant with the rotated-away refresh token")
	storageB.Reload()
	require.True(t, storageB.Has(storageKey("srv")), "the shared credential must survive")
}

// A refresh that the authorization server rejects deletes the credential only
// when it is still the one that failed; a row rewritten concurrently by
// another process is adopted instead.
func TestMCPOAuth_DropCredentialIsCompareAndDelete(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{rotateRefresh: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	cfg := srv.config(nil)
	authPath := filepath.Join(t.TempDir(), "auth.json")

	mgrA, storageA := newSharedManager(t, cfg, authPath)
	require.NoError(t, loginAndConnect(t, mgrA, browserCallbacks(t, nil)))
	sa, err := mgrA.serverAuthFor("srv", cfg)
	require.NoError(t, err)

	// Another process logs in again behind A's back.
	mgrB, _ := newSharedManager(t, cfg, authPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, mgrB.LoginServer(ctx, "srv", browserCallbacks(t, nil)))

	sa.mu.lock()
	sa.dropCredentialLocked()
	got := sa.cred
	sa.mu.unlock()
	require.NotNil(t, got, "the other process's fresh credential is adopted")
	storageA.Reload()
	require.True(t, storageA.Has(storageKey("srv")))

	// With nothing rewritten, the drop does delete.
	sa.mu.lock()
	sa.dropCredentialLocked()
	sa.mu.unlock()
	storageA.Reload()
	require.False(t, storageA.Has(storageKey("srv")))
}

// A transport failure at the token endpoint is not a rejection: the stored
// credential must be kept so a later attempt can still refresh.
func TestMCPOAuth_TransientRefreshFailureKeepsCredential(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{rotateRefresh: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	cfg := srv.config(nil)
	mgr, storage := newTestManager(t, cfg)
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))
	sa, err := mgr.serverAuthFor("srv", cfg)
	require.NoError(t, err)

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/token"
	dead.Close() // nothing listens there any more

	sa.mu.lock()
	sa.cred.TokenURL = deadURL
	gen := sa.gen
	sa.mu.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, herr := sa.handleUnauthorized(ctx, nil, gen)
	require.Error(t, herr)
	require.Empty(t, tok)
	var authErr *AuthRequiredError
	require.NotErrorAs(t, herr, &authErr)
	require.True(t, storage.Has(storageKey("srv")))
}

// A session whose server is blocked on a login picks up a credential minted
// by another process (`fir mcp login`) without a manual /mcp reload.
func TestMCPOAuth_OutOfBandLoginReconnectsWithoutReload(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	cfg := srv.config(&AuthConfig{Mode: AuthModeOAuth})
	authPath := filepath.Join(t.TempDir(), "auth.json")

	session, _ := newSharedManager(t, cfg, authPath)
	var authErr *AuthRequiredError
	require.ErrorAs(t, connectAndWait(t, session), &authErr)

	cli, _ := newSharedManager(t, cfg, authPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, cli.LoginServer(ctx, "srv", browserCallbacks(t, nil)))

	session.pollCredentials()
	requireToolsWork(t, session)
}

// The same pickup happens lazily on a tool call, before the watcher ticks.
func TestMCPOAuth_OutOfBandLoginPickedUpOnToolCall(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	cfg := srv.config(&AuthConfig{Mode: AuthModeOAuth})
	authPath := filepath.Join(t.TempDir(), "auth.json")

	session, _ := newSharedManager(t, cfg, authPath)
	var authErr *AuthRequiredError
	require.ErrorAs(t, connectAndWait(t, session), &authErr)

	cli, _ := newSharedManager(t, cfg, authPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, cli.LoginServer(ctx, "srv", browserCallbacks(t, nil)))

	requireToolsWork(t, session)
}

func TestLoginInstructions_LoopbackExplainsPasteBack(t *testing.T) {
	require.Contains(t, loginInstructions("srv", "http://127.0.0.1:8123/oauth/callback"), "paste it back")
	require.Contains(t, loginInstructions("srv", "http://localhost:8123/cb"), "code=")
	require.NotContains(t, loginInstructions("srv", "https://relay.example/oauth/callback"), "paste")
}
