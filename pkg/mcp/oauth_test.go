package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/require"
)

// browserCallbacks returns LoginCallbacks whose OnAuth "opens" the
// authorization URL by fetching it with an HTTP client that follows the
// redirect back to fir's loopback listener — exactly what a real browser does.
//
// The fetch is synchronous: the loopback callback channel is buffered, so the
// result is safely parked before AwaitAuthCode starts reading. That keeps the
// test deterministic with no sleeps.
func browserCallbacks(t *testing.T, visits *atomic.Int32) pinoauth.LoginCallbacks {
	t.Helper()
	return pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			if visits != nil {
				visits.Add(1)
			}
			resp, err := http.Get(info.URL) //nolint:gosec,noctx // loopback test server
			if err != nil {
				t.Errorf("browser fetch of %s: %v", info.URL, err)
				return
			}
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusOK {
				t.Errorf("authorization flow ended with HTTP %d", resp.StatusCode)
			}
		},
	}
}

// newTestManager builds a Manager for a single server named "srv" with
// file-backed credential storage in a temp dir. There is no login-UI argument
// by design: the Manager can never start a browser flow on its own, so a test
// that wants credentials must call LoginServer, exactly as a user runs
// `fir mcp login`.
func newTestManager(t *testing.T, cfg ServerConfig) (*Manager, *auth.AuthStorage) {
	t.Helper()
	storage := auth.NewAuthStorage(filepath.Join(t.TempDir(), "auth.json"))
	mgr := NewManager(map[string]ServerConfig{"srv": cfg}, false)
	mgr.SetAuth(storage)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, storage
}

// loginAndConnect mints a token via LoginServer and then starts the manager —
// the real sequence a user follows (`fir mcp login`, then a session). Returns
// the first error encountered.
func loginAndConnect(t *testing.T, mgr *Manager, ui pinoauth.LoginCallbacks) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.LoginServer(ctx, "srv", ui); err != nil {
		return err
	}
	return connectAndWait(t, mgr)
}

// connectAndWait starts the manager and blocks until the initial connection
// attempt has finished, returning the recorded per-server error (nil on
// success).
func connectAndWait(t *testing.T, mgr *Manager) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mgr.Start(ctx)
	require.NoError(t, mgr.WaitReady(ctx))
	for _, s := range mgr.Status() {
		if s.Name == "srv" {
			return s.Error
		}
	}
	t.Fatal("server srv missing from status")
	return nil
}

// requireToolsWork asserts the session is live by calling the fake server's
// ping tool.
func requireToolsWork(t *testing.T, mgr *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.NoError(t, err)
	require.False(t, res.IsError)
}

// --- Zero config: the whole point ------------------------------------------

func TestMCPOAuth_ZeroConfigDiscoveryFromChallengeHeader(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{scopes: []string{"mcp:read", "mcp:write"}})

	var visits atomic.Int32
	ui := browserCallbacks(t, &visits)
	// A bare {"url":…, "transport":"streamable"} entry — no auth block at all.
	mgr, _ := newTestManager(t, srv.config(nil))

	require.NoError(t, loginAndConnect(t, mgr, ui))
	requireToolsWork(t, mgr)

	require.Equal(t, int32(1), visits.Load(), "exactly one browser flow")
	registrations, _, issued := as.counters()
	require.Equal(t, 1, registrations, "dynamic client registration ran once")
	require.Equal(t, 1, issued, "one access token issued")
	require.Equal(t, srv.resource(), as.resourceParam(), "RFC 8707 resource indicator sent")

	unauthorized, authorized := srv.counters()
	// The unauthenticated probe is the initialize POST; the streamable
	// transport's standalone SSE GET may also race in before the retry, so the
	// count is small but not exactly one. What matters is that it stops.
	require.LessOrEqual(t, unauthorized, 2)
	require.Positive(t, authorized)
}

func TestMCPOAuth_DiscoveryFallbackWithoutChallengeHeader(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	// 401 with no WWW-Authenticate: discovery must derive the RFC 9728
	// well-known URL from the server URL itself.
	srv := newFakeMCPServer(t, as, mcpOptions{noChallenge: true})

	ui := browserCallbacks(t, nil)
	mgr, _ := newTestManager(t, srv.config(nil))

	require.NoError(t, loginAndConnect(t, mgr, ui))
	requireToolsWork(t, mgr)
	registrations, _, _ := as.counters()
	require.Equal(t, 1, registrations)
}

func TestMCPOAuth_DiscoveryFallbackWithoutResourceMetadata(t *testing.T) {
	// No RFC 9728 document at all: the authorization server issuer falls back
	// to the MCP server's own origin. Point the fake AS at that origin by
	// serving its metadata from the MCP server host.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{noPRM: true, noChallenge: true})

	mgr, _ := newTestManager(t, srv.config(nil))

	err := loginAndConnect(t, mgr, browserCallbacks(t, nil))
	// The MCP server host publishes no authorization-server metadata, so
	// discovery must fail with a clear message rather than hanging or
	// panicking on the nil metadata that GetAuthServerMeta returns for a 4xx.
	require.Error(t, err)
	require.Contains(t, err.Error(), "no OAuth metadata found")
}

// --- Dynamic registration vs a pre-registered client -----------------------

func TestMCPOAuth_PreRegisteredClientIDWithoutDCR(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{noDCR: true, preRegistered: "preregistered-client"})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	ui := browserCallbacks(t, nil)
	mgr, _ := newTestManager(t, srv.config(&AuthConfig{
		ClientID: "preregistered-client",
		Scopes:   []string{"mcp:read"},
	}))

	require.NoError(t, loginAndConnect(t, mgr, ui))
	requireToolsWork(t, mgr)
	registrations, _, issued := as.counters()
	require.Zero(t, registrations, "no dynamic registration when client_id is configured")
	require.Equal(t, 1, issued)
}

func TestMCPOAuth_NoDCRAndNoClientIDIsActionable(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{noDCR: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	mgr, _ := newTestManager(t, srv.config(nil))

	err := loginAndConnect(t, mgr, browserCallbacks(t, nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not support dynamic client registration")
	require.Contains(t, err.Error(), "auth.client_id")
}

// --- Refresh ----------------------------------------------------------------

func TestMCPOAuth_ProactiveRefreshBeforeExpiry(t *testing.T) {
	// A 30s access token is always inside the 60s proactive window, so every
	// request must refresh before it is sent.
	as := newFakeAuthServer(t, asOptions{accessTokenTTL: 30, rotateRefresh: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	ui := browserCallbacks(t, nil)
	mgr, storage := newTestManager(t, srv.config(nil))

	require.NoError(t, loginAndConnect(t, mgr, ui))
	requireToolsWork(t, mgr)

	_, refreshes, issued := as.counters()
	require.Positive(t, refreshes, "proactive refresh ran")
	require.Greater(t, issued, 1)

	// The rotated refresh token must be persisted, or a restart strands the
	// user with a dead credential.
	cred := storage.Get(storageKey("srv"))
	require.NotNil(t, cred)
	require.NotEmpty(t, cred.Refresh)
	require.True(t, as.refreshLive(cred.Refresh), "the persisted refresh token is the live one")
}

func TestMCPOAuth_SilentRefreshAfter401(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	var visits atomic.Int32
	ui := browserCallbacks(t, &visits)
	mgr, _ := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, ui))

	// The resource server stops accepting the access token but the refresh
	// token stays live: recovery must be silent, with no second browser flow.
	as.revokeAccessTokens()
	requireToolsWork(t, mgr)

	_, refreshes, _ := as.counters()
	require.Positive(t, refreshes)
	require.Equal(t, int32(1), visits.Load(), "no second browser flow for a recoverable 401")
}

func TestMCPOAuth_ReauthRequiredAfterFullRevoke(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	var visits atomic.Int32
	ui := browserCallbacks(t, &visits)
	mgr, storage := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, ui))
	require.NotNil(t, storage.Get(storageKey("srv")))

	// Both the access token and the refresh token are revoked: no silent path
	// back. The dead credential must be dropped so a restart does not retry it.
	as.revokeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.Error(t, err)

	var authErr *AuthRequiredError
	require.True(t, errors.As(err, &authErr), "got %T: %v", err, err)
	require.Equal(t, "srv", authErr.Server)
	require.Contains(t, authErr.Error(), "/mcp login srv (or: fir mcp login srv)")
	require.Nil(t, storage.Get(storageKey("srv")), "revoked credential deleted")
}

// --- Static bearer and opt-out ---------------------------------------------

func TestMCPOAuth_StaticBearerToken(t *testing.T) {
	srv := newFakeMCPServer(t, nil, mcpOptions{staticToken: "pat-abc123"})

	mgr, storage := newTestManager(t, srv.config(&AuthConfig{Token: "pat-abc123"}))
	require.NoError(t, connectAndWait(t, mgr))
	requireToolsWork(t, mgr)

	unauthorized, authorized := srv.counters()
	require.Zero(t, unauthorized, "the token is attached to the very first request")
	require.Positive(t, authorized)
	require.Nil(t, storage.Get(storageKey("srv")), "a static token is never persisted")
}

func TestMCPOAuth_StaticBearerTokenFromEnv(t *testing.T) {
	t.Setenv("FIR_TEST_MCP_PAT", "pat-from-env")
	srv := newFakeMCPServer(t, nil, mcpOptions{staticToken: "pat-from-env"})

	mgr, _ := newTestManager(t, srv.config(&AuthConfig{
		Mode:  AuthModeBearer,
		Token: "${FIR_TEST_MCP_PAT}",
	}))
	require.NoError(t, connectAndWait(t, mgr))
	requireToolsWork(t, mgr)
}

func TestMCPOAuth_OptOutSurfacesTheRawFailure(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	mgr, _ := newTestManager(t, srv.config(&AuthConfig{Mode: AuthModeNone}))

	err := connectAndWait(t, mgr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Unauthorized")
	registrations, _, issued := as.counters()
	require.Zero(t, registrations)
	require.Zero(t, issued)
}

func TestMCPOAuth_OptOutStillWorksOnAnOpenServer(t *testing.T) {
	srv := newFakeMCPServer(t, nil, mcpOptions{open: true})
	mgr, _ := newTestManager(t, srv.config(&AuthConfig{Mode: AuthModeNone}))
	require.NoError(t, connectAndWait(t, mgr))
	requireToolsWork(t, mgr)
}

// --- Non-interactive and login gating --------------------------------------

func TestMCPOAuth_ConnectWithoutCredentialsIsActionable(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	// Nobody has run `fir mcp login` yet.
	mgr, _ := newTestManager(t, srv.config(nil))
	err := connectAndWait(t, mgr)
	require.Error(t, err)

	var authErr *AuthRequiredError
	require.True(t, errors.As(err, &authErr), "got %T: %v", err, err)
	require.Contains(t, err.Error(), "/mcp login srv (or: fir mcp login srv)")
	registrations, _, _ := as.counters()
	require.Zero(t, registrations, "the dial path performs no discovery of its own")
}

func TestMCPOAuth_DialPathNeverStartsALogin(t *testing.T) {
	// The Manager has no login-UI hook at all, so this is structural rather
	// than conditional: no amount of reconnect churn can produce a browser
	// window or even a discovery request. This is the invariant behind the
	// claim "fir never opens a browser on its own".
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	mgr, storage := newTestManager(t, srv.config(nil))

	// Shorten reconnect backoff so many cycles run inside the test.
	restoreBackoff(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mgr.Start(ctx)
	_ = mgr.WaitReady(ctx)
	<-ctx.Done()

	registrations, _, issued := as.counters()
	require.Zero(t, registrations, "the dial path must never register a client")
	require.Zero(t, issued, "the dial path must never mint a token")
	require.Nil(t, storage.Get(storageKey("srv")))

	unauthorized, authorized := srv.counters()
	require.Positive(t, unauthorized, "it did keep retrying")
	require.Zero(t, authorized)

	for _, st := range mgr.Status() {
		if st.Name == "srv" {
			var authErr *AuthRequiredError
			require.True(t, errors.As(st.Error, &authErr),
				"every failed cycle reports the actionable error, got %v", st.Error)
		}
	}
}

// restoreBackoff shortens the reconnect backoff for the duration of a test.
func restoreBackoff(t *testing.T) {
	t.Helper()
	oldInitial, oldMax := reconnectInitialDelay, reconnectMaxDelay
	reconnectInitialDelay = 10 * time.Millisecond
	reconnectMaxDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		reconnectInitialDelay, reconnectMaxDelay = oldInitial, oldMax
	})
}

// --- Persistence ------------------------------------------------------------

func TestMCPOAuth_TokenSurvivesRestart(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	authPath := filepath.Join(t.TempDir(), "auth.json")
	ui := browserCallbacks(t, nil)

	first := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	first.SetAuth(auth.NewAuthStorage(authPath))
	require.NoError(t, loginAndConnect(t, first, ui))
	require.NoError(t, first.Close())

	// A fresh process reads the same auth.json and must connect with no login.
	second := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	// Deliberately non-interactive: if the stored token were not reused this
	// would fail with AuthRequiredError.
	second.SetAuth(auth.NewAuthStorage(authPath))
	t.Cleanup(func() { _ = second.Close() })
	require.NoError(t, connectAndWait(t, second))
	requireToolsWork(t, second)

	registrations, _, _ := as.counters()
	require.Equal(t, 1, registrations, "the second process reuses the stored credential")
}

func TestMCPOAuth_StoredTokenIsBoundToItsServerURL(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	real := newFakeMCPServer(t, as, mcpOptions{})
	authPath := filepath.Join(t.TempDir(), "auth.json")
	ui := browserCallbacks(t, nil)

	first := NewManager(map[string]ServerConfig{"srv": real.config(nil)}, false)
	first.SetAuth(auth.NewAuthStorage(authPath))
	require.NoError(t, loginAndConnect(t, first, ui))
	require.NoError(t, first.Close())

	// A project-local mcp.json now points the SAME server name at a different
	// host. The stored bearer token must not be handed to it.
	impostor := newFakeMCPServer(t, as, mcpOptions{})
	second := NewManager(map[string]ServerConfig{"srv": impostor.config(nil)}, false)
	second.SetAuth(auth.NewAuthStorage(authPath))
	t.Cleanup(func() { _ = second.Close() })

	err := connectAndWait(t, second)
	require.Error(t, err, "the impostor URL must not inherit the stored token")
	unauthorized, authorized := impostor.counters()
	require.Positive(t, unauthorized)
	require.Zero(t, authorized, "no authenticated request ever reached the impostor")
}

func TestMCPOAuth_LogoutRemovesStoredCredential(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	ui := browserCallbacks(t, nil)
	mgr, storage := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, ui))
	require.NotNil(t, storage.Get(storageKey("srv")))

	require.NoError(t, mgr.LogoutServer("srv"))
	require.Nil(t, storage.Get(storageKey("srv")))
	require.False(t, storage.Has(storageKey("srv")))
}

func TestMCPOAuth_LoginServerMintsAheadOfTime(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	// `fir mcp login` builds a Manager it never starts.
	storage := auth.NewAuthStorage(filepath.Join(t.TempDir(), "auth.json"))
	mgr := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	mgr.SetAuth(storage)
	t.Cleanup(func() { _ = mgr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, mgr.LoginServer(ctx, "srv", browserCallbacks(t, nil)))

	cred := storage.Get(storageKey("srv"))
	require.NotNil(t, cred)
	require.Equal(t, auth.CredentialTypeOAuth, cred.Type)
	require.NotEmpty(t, cred.Access)
	require.Equal(t, srv.resource(), cred.Extra[extraResource])

	require.Contains(t, mgr.AuthStatus("srv"), "OAuth token stored")
}

func TestMCPOAuth_LoginServerRejectsNonOAuthServers(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{
		"stdio":  {Command: "true"},
		"bearer": {Transport: "streamable", URL: "https://example.com/mcp", Auth: &AuthConfig{Token: "x"}},
		"off":    {Transport: "streamable", URL: "https://example.com/mcp", Auth: &AuthConfig{Mode: AuthModeNone}},
	}, false)
	t.Cleanup(func() { _ = mgr.Close() })

	ctx := context.Background()
	require.ErrorContains(t, mgr.LoginServer(ctx, "stdio", pinoauth.LoginCallbacks{}), "stdio transport")
	require.ErrorContains(t, mgr.LoginServer(ctx, "bearer", pinoauth.LoginCallbacks{}), "static bearer token")
	require.ErrorContains(t, mgr.LoginServer(ctx, "off", pinoauth.LoginCallbacks{}), "authentication disabled")
	require.ErrorContains(t, mgr.LoginServer(ctx, "nope", pinoauth.LoginCallbacks{}), "not configured")
}

func TestMCPOAuth_AuthStatusLabels(t *testing.T) {
	t.Setenv("FIR_TEST_MISSING_PAT", "")
	mgr := NewManager(map[string]ServerConfig{
		"stdio":   {Command: "true"},
		"plain":   {Transport: "streamable", URL: "https://example.com/mcp"},
		"bearer":  {Transport: "streamable", URL: "https://example.com/mcp", Auth: &AuthConfig{Token: "x"}},
		"unsetok": {Transport: "streamable", URL: "https://example.com/mcp", Auth: &AuthConfig{Mode: AuthModeBearer, Token: "${FIR_TEST_MISSING_PAT}"}},
		"off":     {Transport: "streamable", URL: "https://example.com/mcp", Auth: &AuthConfig{Mode: AuthModeNone}},
		"broken":  {Transport: "streamable", URL: "://nope", Auth: &AuthConfig{Mode: AuthModeOAuth}},
	}, false)
	t.Cleanup(func() { _ = mgr.Close() })

	require.Empty(t, mgr.AuthStatus("stdio"))
	require.Empty(t, mgr.AuthStatus("missing"))
	require.Contains(t, mgr.AuthStatus("plain"), "no stored OAuth token")
	require.Equal(t, `auth: static bearer token`, mgr.AuthStatus("bearer"))
	require.Contains(t, mgr.AuthStatus("unsetok"), "NOT SET")
	require.Contains(t, mgr.AuthStatus("off"), "disabled")
	require.Contains(t, mgr.AuthStatus("broken"), "misconfigured")
}

// An expired, unrefreshable token sends the user to a login, naming the
// in-session slash form first and the terminal form second.
func TestMCPOAuth_AuthStatusExpiredNamesBothLoginForms(t *testing.T) {
	const resource = "https://mcp.example.com/mcp"
	storage := auth.NewAuthStorage(filepath.Join(t.TempDir(), "auth.json"))
	require.NoError(t, newCredentialStore(storage).Save("srv", &oauthCredential{
		Resource: resource,
		Issuer:   "https://issuer.example",
		TokenURL: "https://issuer.example/token",
		Token: &pinoauth.Token{
			AccessToken: "stale",
			ExpiresAt:   time.Now().Add(-time.Hour),
		},
	}))

	mgr := NewManager(map[string]ServerConfig{
		"srv": {Transport: "streamable", URL: resource},
	}, false)
	mgr.SetAuth(storage)
	t.Cleanup(func() { _ = mgr.Close() })

	require.Equal(t,
		"auth: OAuth token expired — run: /mcp login srv (or: fir mcp login srv)",
		mgr.AuthStatus("srv"))
}

// --- Credential storage isolation ------------------------------------------

func TestMCPCredentialsAreNotProviderAccounts(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	storage := auth.NewAuthStorage(authPath)
	require.NoError(t, storage.Set("anthropic", auth.AuthCredential{Type: auth.CredentialTypeOAuth, Access: "a"}))
	store := newCredentialStore(storage)
	require.NoError(t, store.Save("github", &oauthCredential{
		Resource: "https://api.githubcopilot.com/mcp",
		Token:    &pinoauth.Token{AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour)},
	}))

	require.Equal(t, []string{"anthropic"}, storage.List())
	accounts := storage.AllAccounts()
	require.Len(t, accounts, 1)
	require.Equal(t, "anthropic", accounts[0].Provider)
	require.Empty(t, storage.AccountsForProvider("mcp:github"))

	// The credential is still on disk under its namespaced key.
	raw, err := os.ReadFile(authPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"mcp:github"`)
}

func TestCredentialStoreRejectsMismatchedResource(t *testing.T) {
	store := newCredentialStore(auth.NewAuthStorage(filepath.Join(t.TempDir(), "auth.json")))
	require.NoError(t, store.Save("srv", &oauthCredential{
		Resource: "https://good.example/mcp",
		Token:    &pinoauth.Token{AccessToken: "tok", RefreshToken: "r"},
		TokenURL: "https://as.example/token",
	}))
	require.NotNil(t, store.Load("srv", "https://good.example/mcp"))
	require.Nil(t, store.Load("srv", "https://evil.example/mcp"))
}

func TestCredentialStoreNilStorageIsInert(t *testing.T) {
	var store credentialStore
	require.Nil(t, store.Load("srv", "https://x/mcp"))
	require.NoError(t, store.Save("srv", &oauthCredential{Token: &pinoauth.Token{AccessToken: "a"}}))
	require.NoError(t, store.Delete("srv"))
}

func TestStorageKeyEscapesAccountSeparator(t *testing.T) {
	// "#" is the provider/account separator in auth.json keys; leaving it raw
	// would make SplitSlot mis-parse an MCP key as a provider account.
	key := storageKey("weird#name")
	require.Equal(t, "mcp:weird%23name", key)
	require.False(t, auth.IsSlotKey(key))
	require.True(t, auth.IsMCPKey(key))
}

// --- Pure helpers -----------------------------------------------------------

func TestCanonicalResource(t *testing.T) {
	cases := map[string]string{
		"https://Example.COM/mcp":           "https://example.com/mcp",
		"https://example.com:443/mcp":       "https://example.com/mcp",
		"http://example.com:80/mcp":         "http://example.com/mcp",
		"https://example.com:8443/mcp":      "https://example.com:8443/mcp",
		"https://example.com/mcp?x=1#frag":  "https://example.com/mcp",
		"https://example.com/":              "https://example.com",
		"https://example.com":               "https://example.com",
		"https://example.com/deep/path/":    "https://example.com/deep/path/",
		"https://user:pw@example.com/mcp":   "https://example.com/mcp",
		"https://example.com/MixedCasePath": "https://example.com/MixedCasePath",
	}
	for in, want := range cases {
		got, err := canonicalResource(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "/mcp", "not a url", "://nope"} {
		_, err := canonicalResource(bad)
		require.Error(t, err, bad)
	}
}

func TestParseChallenge(t *testing.T) {
	ci := parseChallenge([]string{`Bearer realm="mcp", resource_metadata="https://x/.well-known/oauth-protected-resource", scope="a b"`})
	require.Equal(t, "https://x/.well-known/oauth-protected-resource", ci.ResourceMetadata)
	require.Equal(t, "a b", ci.Scope)

	require.Equal(t, challengeInfo{}, parseChallenge(nil))
	require.Equal(t, challengeInfo{}, parseChallenge([]string{`Basic realm="x"`}))
	// A malformed header degrades to "no information", not an error.
	require.Equal(t, challengeInfo{}, parseChallenge([]string{`"broken`}))
}

func TestPRMCandidates(t *testing.T) {
	got, err := prmCandidates("https://example.com/mcp")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://example.com/.well-known/oauth-protected-resource/mcp",
		"https://example.com/.well-known/oauth-protected-resource",
	}, got)

	got, err = prmCandidates("https://example.com")
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/.well-known/oauth-protected-resource"}, got)
}

func TestASCandidates(t *testing.T) {
	got, err := asCandidates("https://as.example.com/tenant1")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://as.example.com/.well-known/oauth-authorization-server/tenant1",
		"https://as.example.com/.well-known/openid-configuration/tenant1",
		"https://as.example.com/tenant1/.well-known/openid-configuration",
	}, got)

	got, err = asCandidates("https://as.example.com")
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://as.example.com/.well-known/oauth-authorization-server",
		"https://as.example.com/.well-known/openid-configuration",
	}, got)
}

func TestDiscoverAuthServerWithNoIssuers(t *testing.T) {
	_, err := discoverAuthServer(context.Background(), http.DefaultClient, nil)
	require.ErrorIs(t, err, errNoMetadata)
}

func TestBuildAuthorizeURL(t *testing.T) {
	pkce := pinoauth.GeneratePKCE()
	raw, err := buildAuthorizeURL("https://as.example.com/authorize?tenant=x", authorizeParams{
		ClientID:    "cid",
		RedirectURI: "http://127.0.0.1:1234/oauth/callback",
		State:       "st",
		Scope:       "a b",
		Resource:    "https://mcp.example.com/mcp",
		PKCE:        pkce,
	})
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "cid", q.Get("client_id"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.Equal(t, pkce.Challenge, q.Get("code_challenge"))
	require.Equal(t, "https://mcp.example.com/mcp", q.Get("resource"))
	require.Equal(t, "a b", q.Get("scope"))
	require.Equal(t, "x", q.Get("tenant"), "pre-existing query params are preserved")

	// An empty scope is omitted entirely rather than sent blank.
	raw, err = buildAuthorizeURL("https://as.example.com/authorize", authorizeParams{PKCE: pkce})
	require.NoError(t, err)
	require.NotContains(t, raw, "scope=")

	_, err = buildAuthorizeURL("://bad", authorizeParams{PKCE: pkce})
	require.Error(t, err)
}

func TestResolveScopePrecedence(t *testing.T) {
	sa := &serverAuth{}
	require.Empty(t, sa.resolveScope(nil))

	sa.challenge = challengeInfo{Scope: "from-challenge"}
	require.Equal(t, "from-challenge", sa.resolveScope(nil))

	prm := &oauthex.ProtectedResourceMetadata{ScopesSupported: []string{"from", "prm"}}
	sa2 := &serverAuth{}
	require.Equal(t, "from prm", sa2.resolveScope(prm))

	sa.cfg.Scopes = []string{"configured", "wins"}
	require.Equal(t, "configured wins", sa.resolveScope(prm))
}

func TestAuthConfigValidateAndMode(t *testing.T) {
	var nilCfg *AuthConfig
	require.NoError(t, nilCfg.Validate())
	require.Equal(t, AuthModeAuto, nilCfg.ResolveMode())
	require.Empty(t, nilCfg.BearerToken())

	require.Equal(t, AuthModeBearer, (&AuthConfig{Token: "t"}).ResolveMode())
	require.Equal(t, AuthModeOAuth, (&AuthConfig{Mode: AuthModeOAuth, Token: "t"}).ResolveMode())
	require.NoError(t, (&AuthConfig{Mode: AuthModeNone}).Validate())
	require.Error(t, (&AuthConfig{Mode: "weird"}).Validate())
	require.ErrorContains(t, (&AuthConfig{Mode: AuthModeBearer}).Validate(), "auth.token is required")

	t.Setenv("FIR_TEST_TOK", "secret")
	require.Equal(t, "secret", (&AuthConfig{Token: "${FIR_TEST_TOK}"}).BearerToken())
	require.Equal(t, "secret", (&AuthConfig{Token: "$FIR_TEST_TOK"}).BearerToken())
	require.NoError(t, (&AuthConfig{Token: "$FIR_TEST_TOK"}).Validate())
	require.ErrorContains(t, (&AuthConfig{Token: "${FIR_TEST_UNSET_TOK}"}).Validate(), "auth.token is required")
	// A literal token containing "$" is never mangled.
	require.Equal(t, "pa$$word", (&AuthConfig{Token: "pa$$word"}).BearerToken())
	require.Equal(t, "$ leading space", (&AuthConfig{Token: "$ leading space"}).BearerToken())
}

func TestAuthConfigRoundTripsThroughJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
	  "mcpServers": {
	    "zero":   {"transport": "streamable", "url": "https://a.example/mcp"},
	    "pat":    {"transport": "streamable", "url": "https://b.example/mcp", "auth": {"token": "${TOK}"}},
	    "client": {"transport": "streamable", "url": "https://c.example/mcp",
	               "auth": {"client_id": "cid", "scopes": ["x", "y"]}},
	    "forced": {"transport": "streamable", "url": "https://e.example/mcp",
	               "auth": {"authorization_servers": ["https://as.example"]}},
	    "off":    {"transport": "streamable", "url": "https://d.example/mcp", "auth": {"mode": "none"}}
	  }
	}`), 0o600))

	cfg, err := LoadConfigFile(path)
	require.NoError(t, err)
	require.Nil(t, cfg.MCPServers["zero"].Auth)
	require.Equal(t, "${TOK}", cfg.MCPServers["pat"].Auth.Token)
	require.Equal(t, AuthModeBearer, cfg.MCPServers["pat"].Auth.ResolveMode())
	require.Equal(t, []string{"x", "y"}, cfg.MCPServers["client"].Auth.Scopes)
	require.Equal(t, []string{"https://as.example"}, cfg.MCPServers["forced"].Auth.AuthorizationServers)
	require.Equal(t, AuthModeAuto, cfg.MCPServers["forced"].Auth.ResolveMode())
	require.Equal(t, AuthModeNone, cfg.MCPServers["off"].Auth.ResolveMode())

	// A config change must invalidate cached auth state (URL binding).
	sa, err := newServerAuth("client", cfg.MCPServers["client"], credentialStore{})
	require.NoError(t, err)
	require.True(t, sa.matches(cfg.MCPServers["client"]))
	require.False(t, sa.matches(cfg.MCPServers["zero"]))
	changed := cfg.MCPServers["client"]
	changed.Auth = &AuthConfig{ClientID: "other"}
	require.False(t, sa.matches(changed))
}

func TestNewServerAuthDisabledAndInvalid(t *testing.T) {
	sa, err := newServerAuth("x", ServerConfig{Transport: "streamable", URL: "https://x/mcp",
		Auth: &AuthConfig{Mode: AuthModeNone}}, credentialStore{})
	require.NoError(t, err)
	require.Nil(t, sa)

	_, err = newServerAuth("x", ServerConfig{Transport: "streamable", URL: "https://x/mcp",
		Auth: &AuthConfig{Mode: "bogus"}}, credentialStore{})
	require.ErrorContains(t, err, "unsupported auth.mode")

	_, err = newServerAuth("x", ServerConfig{Transport: "streamable", URL: "nope"}, credentialStore{})
	require.ErrorContains(t, err, "must be absolute")
}

func TestAuthRequiredErrorMessage(t *testing.T) {
	base := errors.New("boom")
	err := &AuthRequiredError{Server: "gh", Reason: "token revoked", Cause: base}
	require.Equal(t, `MCP server "gh" requires OAuth authentication (token revoked); run: /mcp login gh (or: fir mcp login gh)`, err.Error())
	require.ErrorIs(t, err, base)

	bare := &AuthRequiredError{Server: "gh"}
	require.True(t, strings.HasSuffix(bare.Error(), "run: /mcp login gh (or: fir mcp login gh)"))
}

// --- Token containment ------------------------------------------------------

func TestMCPOAuth_TokenIsNeverSentToARedirectTarget(t *testing.T) {
	// A hostile (or compromised) MCP server answers 302 to a host it chooses.
	// net/http's own "strip Authorization across hosts" protection runs above
	// the transport, so the redirected request would come back through our
	// RoundTripper and be re-stamped with the bearer. It must not be.
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sink.Close)

	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	ui := browserCallbacks(t, nil)
	mgr, _ := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, ui))

	// Now make the server redirect and drive a request through it.
	srv.redirectTo(sink.URL + "/stolen")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.Error(t, err, "a redirect on the MCP data path must fail, not be followed")
	require.Zero(t, leaked.Load(), "the bearer token must never reach the redirect target")
}

func TestServerAuthSameOrigin(t *testing.T) {
	sa, err := newServerAuth("srv", ServerConfig{
		Transport: "streamable", URL: "https://mcp.example.com/mcp",
	}, credentialStore{})
	require.NoError(t, err)

	for _, ok := range []string{
		"https://mcp.example.com/mcp",
		"https://mcp.example.com/other",
		"https://MCP.EXAMPLE.COM/mcp",
		"https://mcp.example.com:443/mcp",
	} {
		u, perr := url.Parse(ok)
		require.NoError(t, perr)
		require.True(t, sa.sameOrigin(u), ok)
	}
	for _, bad := range []string{
		"https://evil.example.com/mcp",
		"https://mcp.example.com.evil.com/mcp",
		"http://mcp.example.com/mcp",
		"https://mcp.example.com:8443/mcp",
	} {
		u, perr := url.Parse(bad)
		require.NoError(t, perr)
		require.False(t, sa.sameOrigin(u), bad)
	}
	require.False(t, sa.sameOrigin(nil))
}

// --- Bearer mode failure ----------------------------------------------------

func TestMCPOAuth_RejectedStaticBearerIsNotAnOAuthProblem(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{staticToken: "the-right-token"})

	mgr, storage := newTestManager(t, srv.config(&AuthConfig{Token: "the-wrong-token"}))

	err := connectAndWait(t, mgr)
	require.Error(t, err)
	require.Contains(t, err.Error(), "static bearer token")
	require.Contains(t, err.Error(), "auth.token")
	require.NotContains(t, err.Error(), "mcp login",
		"there is nothing to log in to for a static token")

	var authErr *AuthRequiredError
	require.False(t, errors.As(err, &authErr))
	registrations, _, issued := as.counters()
	require.Zero(t, registrations)
	require.Zero(t, issued)
	require.Nil(t, storage.Get(storageKey("srv")))
}

func TestMCPOAuth_ExistingOAuthCredentialSurvivesABearerRejection(t *testing.T) {
	// Switching a server to a (wrong) static token must not delete the OAuth
	// credential the user already has.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	authPath := filepath.Join(t.TempDir(), "auth.json")
	ui := browserCallbacks(t, nil)

	first := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	first.SetAuth(auth.NewAuthStorage(authPath))
	require.NoError(t, loginAndConnect(t, first, ui))
	require.NoError(t, first.Close())

	bearerSrv := newFakeMCPServer(t, nil, mcpOptions{staticToken: "right"})
	cfg := bearerSrv.config(&AuthConfig{Token: "wrong"})
	cfg.URL = srv.resource() // same server, now configured with a static token
	second := NewManager(map[string]ServerConfig{"srv": cfg}, false)
	storage := auth.NewAuthStorage(authPath)
	second.SetAuth(storage)
	t.Cleanup(func() { _ = second.Close() })

	require.Error(t, connectAndWait(t, second))
	require.NotNil(t, storage.Get(storageKey("srv")), "the OAuth credential must survive")
}

// --- Login revives a server whose initial connect failed --------------------

func TestMCPOAuth_LoginServerRevivesAFailedServer(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	// Non-interactive: the initial dial fails with AuthRequiredError and no
	// reconnect loop is ever started, so a bare kick would be a no-op.
	mgr, _ := newTestManager(t, srv.config(nil))
	require.Error(t, connectAndWait(t, mgr))
	require.False(t, mgr.hasSession("srv"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, mgr.LoginServer(ctx, "srv", browserCallbacks(t, nil)))

	require.True(t, mgr.hasSession("srv"), "login must bring the server up")
	requireToolsWork(t, mgr)
}

// --- Reload picks up an out-of-band login -----------------------------------

func TestMCPOAuth_ReloadPicksUpCredentialMintedByAnotherProcess(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	authPath := filepath.Join(t.TempDir(), "auth.json")
	cfgs := map[string]ServerConfig{"srv": srv.config(nil)}

	// The running session: non-interactive, so the initial dial fails for want
	// of a credential and the server sits down.
	session := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	session.SetAuth(auth.NewAuthStorage(authPath))
	t.Cleanup(func() { _ = session.Close() })
	require.Error(t, connectAndWait(t, session))
	require.False(t, session.hasSession("srv"))

	// A second process — `fir mcp login srv` — mints a token into the same
	// auth.json. The running session's in-memory view is now stale.
	cli := NewManager(map[string]ServerConfig{"srv": srv.config(nil)}, false)
	cli.SetAuth(auth.NewAuthStorage(authPath))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, cli.LoginServer(ctx, "srv", browserCallbacks(t, nil)))
	require.NoError(t, cli.Close())

	// /mcp reload — same config, so nothing but the credential has changed.
	_, err := session.Reload(ctx, cfgs)
	require.NoError(t, err)
	require.True(t, session.hasSession("srv"), "reload must pick up the stored token")
	requireToolsWork(t, session)
}

func TestMCPOAuth_ReloadKeepsInMemoryTokenWithoutStorage(t *testing.T) {
	// With no auth.AuthStorage the in-memory token is the only copy: a reload
	// must not invalidate it, or /mcp reload would log the session out.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	cfgs := map[string]ServerConfig{"srv": srv.config(nil)}

	mgr, _ := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := mgr.Reload(ctx, cfgs)
	require.NoError(t, err)
	require.True(t, mgr.hasSession("srv"))
	requireToolsWork(t, mgr)

	_, _, issued := as.counters()
	require.Equal(t, 1, issued, "the cached token is reused; no second grant")
}

// --- 403 is not a login trigger ---------------------------------------------

func TestMCPOAuth_ForbiddenDoesNotTriggerLogin(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{forbid: true})

	mgr, _ := newTestManager(t, srv.config(nil))

	err := connectAndWait(t, mgr)
	require.Error(t, err)
	var authErr *AuthRequiredError
	require.False(t, errors.As(err, &authErr), "403 is an authorization failure, not a login prompt")
	registrations, _, issued := as.counters()
	require.Zero(t, registrations)
	require.Zero(t, issued)
}

func TestCanonicalResourceIPv6(t *testing.T) {
	got, err := canonicalResource("https://[::1]:443/mcp")
	require.NoError(t, err)
	require.Equal(t, "https://[::1]/mcp", got)

	got, err = canonicalResource("https://[::1]:8443/mcp")
	require.NoError(t, err)
	require.Equal(t, "https://[::1]:8443/mcp", got)

	got, err = canonicalResource("http://[2001:DB8::1]:80/mcp")
	require.NoError(t, err)
	require.Equal(t, "http://[2001:db8::1]/mcp", got)
}

func TestStorageKeyIsInjective(t *testing.T) {
	require.NotEqual(t, storageKey("a#b"), storageKey("a%23b"))
	require.False(t, auth.IsSlotKey(storageKey("a#b")))
	require.False(t, auth.IsSlotKey(storageKey("a%23b")))
}

// --- F1: credentials never ride plaintext to a routable host ---------------

func TestRequireSecureTransport(t *testing.T) {
	for _, ok := range []string{
		"https://example.com/mcp",
		"https://example.com:8443/mcp",
		"http://127.0.0.1:8080/mcp",
		"http://localhost:3000/mcp",
		"http://LOCALHOST/mcp",
		"http://[::1]:9000/mcp",
		"http://127.0.0.5/mcp",
	} {
		require.NoError(t, requireSecureTransport(ok), ok)
	}
	for _, bad := range []string{
		"http://example.com/mcp",
		"http://mcp.internal:8080/mcp",
		"http://192.168.1.10:8080/mcp",
		"http://10.0.0.1/mcp",
		// "localhost" as a suffix is not loopback.
		"http://notlocalhost/mcp",
		"http://evil.localhost.example.com/mcp",
	} {
		err := requireSecureTransport(bad)
		require.Error(t, err, bad)
		require.Contains(t, err.Error(), "plaintext", bad)
	}
}

func TestMCPOAuth_PlaintextServerIsRejectedBeforeAnyRequest(t *testing.T) {
	// Both credential-bearing modes must refuse; the config never gets as far
	// as opening a connection, so no token can be observed on the wire.
	for name, ac := range map[string]*AuthConfig{
		"oauth":  nil,
		"bearer": {Token: "pat-123"},
		"forced": {Mode: AuthModeOAuth},
	} {
		t.Run(name, func(t *testing.T) {
			mgr, _ := newTestManager(t, ServerConfig{
				Transport: "streamable",
				URL:       "http://mcp.internal:8080/mcp",
				Auth:      ac,
			})
			err := connectAndWait(t, mgr)
			require.Error(t, err)
			require.Contains(t, err.Error(), "refusing to send credentials")
			require.Contains(t, err.Error(), "auth.mode")
		})
	}
}

func TestMCPOAuth_PlaintextIsFineWhenNoCredentialsAreSent(t *testing.T) {
	// mode "none" sends nothing, so there is nothing to protect. This is the
	// documented escape hatch for a plain-http server on a trusted network.
	//
	// Check the exemption directly against a *routable* plaintext host: the
	// end-to-end case below runs on loopback, which would pass the rule
	// regardless and so proves nothing about the exemption itself.
	sa, err := newServerAuth("srv", ServerConfig{
		Transport: "streamable",
		URL:       "http://mcp.internal:8080/mcp",
		Auth:      &AuthConfig{Mode: AuthModeNone},
	}, credentialStore{})
	require.NoError(t, err, "mode none must be exempt from the https requirement")
	require.Nil(t, sa, "mode none installs no auth state at all")

	srv := newFakeMCPServer(t, nil, mcpOptions{open: true})
	cfg := srv.config(&AuthConfig{Mode: AuthModeNone})
	mgr, _ := newTestManager(t, cfg)
	require.NoError(t, connectAndWait(t, mgr))
	requireToolsWork(t, mgr)
}

func TestMCPOAuth_LoopbackPlaintextIsAllowed(t *testing.T) {
	// The whole existing suite relies on this (httptest serves plain http on
	// 127.0.0.1), but pin it explicitly: local dev servers must keep working.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	require.True(t, strings.HasPrefix(srv.resource(), "http://127.0.0.1:"))

	mgr, _ := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))
	requireToolsWork(t, mgr)
}

// --- F2: a request never waits on a human ----------------------------------

func TestMCPOAuth_RequestFailsFastWhileALoginIsInFlight(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})

	sa, err := newServerAuth("srv", srv.config(nil), credentialStore{})
	require.NoError(t, err)

	// Model a login parked on the browser: OnAuth blocks until we release it.
	releaseBrowser := make(chan struct{})
	loginEntered := make(chan struct{})
	loginDone := make(chan error, 1)
	ui := pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			close(loginEntered)
			<-releaseBrowser
			resp, ferr := http.Get(info.URL) //nolint:gosec,noctx // loopback test server
			if ferr != nil {
				t.Errorf("browser fetch: %v", ferr)
				return
			}
			_ = resp.Body.Close()
		},
	}
	go func() { loginDone <- sa.Login(context.Background(), ui) }()
	<-loginEntered

	// A tool call arriving now must NOT sit on the mutex until the human is
	// done. It must come back immediately, with a message that explains why.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	_, _, err = sa.accessToken(ctx)
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Contains(t, err.Error(), "interactive login is in progress")
	require.Less(t, elapsed, 2*time.Second, "the request must not block on the browser")
	require.NoError(t, ctx.Err(), "the caller's deadline must still be intact")

	close(releaseBrowser)
	require.NoError(t, <-loginDone)

	// Once the login is done the lock is free and the fresh token is served.
	tok, _, err := sa.accessToken(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tok)
}

func TestMCPOAuth_AuthLockRespectsCallerDeadline(t *testing.T) {
	sa, err := newServerAuth("srv", ServerConfig{
		Transport: "streamable", URL: "https://mcp.example.com/mcp",
	}, credentialStore{})
	require.NoError(t, err)

	// Hold the lock without marking a login in flight — the "some other
	// request is refreshing" case. The caller must give up on its own
	// deadline rather than blocking forever.
	sa.mu.lock()
	defer sa.mu.unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = sa.accessToken(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

// --- Discovery really does use the challenge header ------------------------

func TestMCPOAuth_ChallengeHeaderPointsDiscoveryAtANonStandardPath(t *testing.T) {
	// The RFC 9728 document lives somewhere well-known derivation cannot
	// guess; only the resource_metadata parameter of the 401 challenge names
	// it. A successful login therefore proves the header was honoured.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{prmOnlyAtChallengePath: true})

	mgr, _ := newTestManager(t, srv.config(nil))
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))
	requireToolsWork(t, mgr)

	registrations, _, issued := as.counters()
	require.Equal(t, 1, registrations)
	require.Equal(t, 1, issued)
}

// --- Forced authorization servers ------------------------------------------

func TestResolveIssuersPrecedence(t *testing.T) {
	sa := &serverAuth{resource: "https://mcp.example.com/mcp"}
	// No PRM at all: fall back to the resource's own origin.
	require.Equal(t, []string{"https://mcp.example.com"}, sa.resolveIssuers(nil))

	prm := &oauthex.ProtectedResourceMetadata{
		AuthorizationServers: []string{"https://advertised.example"},
	}
	require.Equal(t, []string{"https://advertised.example"}, sa.resolveIssuers(prm))

	// A configured list replaces the advertised one outright — not merged.
	sa.cfg.AuthorizationServers = []string{"https://forced.example", "https://backup.example"}
	require.Equal(t, []string{"https://forced.example", "https://backup.example"}, sa.resolveIssuers(prm))
	require.Equal(t, []string{"https://forced.example", "https://backup.example"}, sa.resolveIssuers(nil))
}

func TestMCPOAuth_ForcedAuthorizationServersOverrideWrongMetadata(t *testing.T) {
	// The MCP server advertises an issuer that does not work; the forced list
	// names the one that does. A successful login proves the config won.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{
		bogusAuthServers: []string{"https://wrong.invalid"},
	})

	mgr, _ := newTestManager(t, srv.config(&AuthConfig{
		AuthorizationServers: []string{as.URL()},
	}))
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))
	requireToolsWork(t, mgr)

	registrations, _, issued := as.counters()
	require.Equal(t, 1, registrations)
	require.Equal(t, 1, issued)
}

func TestMCPOAuth_ForcedAuthorizationServersSurviveMissingResourceMetadata(t *testing.T) {
	// No RFC 9728 document anywhere and no challenge header to point at one:
	// discovery has nothing to go on but the forced list.
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{noPRM: true, noChallenge: true})

	mgr, _ := newTestManager(t, srv.config(&AuthConfig{
		Mode:                 AuthModeOAuth,
		AuthorizationServers: []string{as.URL()},
		Scopes:               []string{"mcp:read"},
	}))
	require.NoError(t, loginAndConnect(t, mgr, browserCallbacks(t, nil)))
	requireToolsWork(t, mgr)

	_, _, issued := as.counters()
	require.Equal(t, 1, issued)
}

func TestMCPOAuth_ForcedIssuerChangeInvalidatesStoredCredential(t *testing.T) {
	const resource = "https://mcp.example.com/mcp"
	storage := auth.NewAuthStorage(filepath.Join(t.TempDir(), "auth.json"))
	store := newCredentialStore(storage)

	save := func(issuer string) {
		require.NoError(t, store.Save("srv", &oauthCredential{
			Resource: resource,
			Issuer:   issuer,
			TokenURL: "https://forced.example/token",
			Token: &pinoauth.Token{
				AccessToken: "stored-token", RefreshToken: "r",
				ExpiresAt: time.Now().Add(time.Hour),
			},
		}))
	}
	newAuthMode := func(mode AuthMode, forced []string) *serverAuth {
		sa, err := newServerAuth("srv", ServerConfig{
			Transport: "streamable", URL: resource,
			Auth: &AuthConfig{Mode: mode, AuthorizationServers: forced},
		}, store)
		require.NoError(t, err)
		return sa
	}
	newAuth := func(forced []string) *serverAuth {
		return newAuthMode(AuthModeAuto, forced)
	}
	load := func(sa *serverAuth) *oauthCredential {
		sa.mu.lock()
		defer sa.mu.unlock()
		sa.ensureLoadedLocked()
		return sa.cred
	}

	// A credential minted by a forced issuer is reused. The trailing slash
	// proves the comparison is canonicalised rather than byte-exact.
	save("https://forced.example")
	cred := load(newAuth([]string{"https://forced.example/", "https://other.example"}))
	require.NotNil(t, cred)
	require.Equal(t, "stored-token", cred.Token.AccessToken)

	// The user repoints the forced list: the old credential must not be used.
	sa := newAuth([]string{"https://elsewhere.example"})
	require.Nil(t, load(sa))
	// …but it is only dropped from memory, so flipping back restores it.
	require.True(t, storage.Has(storageKey("srv")), "the stored row is left alone")
	require.NotNil(t, load(newAuth([]string{"https://forced.example"})))

	// A credential with no recorded issuer has unknown provenance.
	save("")
	require.Nil(t, load(newAuth([]string{"https://forced.example"})))
	// With nothing forced, any issuer is by definition what discovery picked.
	require.NotNil(t, load(newAuth(nil)))

	// An unusable stored credential leaves the auth state demanding a login
	// rather than silently sending an unauthenticated request.
	save("https://forced.example")
	sa = newAuthMode(AuthModeOAuth, []string{"https://elsewhere.example"})
	_, _, err := sa.accessToken(context.Background())
	var authErr *AuthRequiredError
	require.ErrorAs(t, err, &authErr)
	require.Equal(t, "no stored credential", authErr.Reason)
}

func TestAuthConfigValidateAuthorizationServers(t *testing.T) {
	ok := func(servers ...string) error {
		return (&AuthConfig{AuthorizationServers: servers}).Validate()
	}
	require.NoError(t, ok("https://as.example"))
	require.NoError(t, ok("https://as.example/tenant/1", "https://backup.example"))
	require.NoError(t, ok("http://localhost:9000"), "loopback http is allowed for local dev")
	require.NoError(t, ok("http://127.0.0.1:9000/as"))
	require.NoError(t, (&AuthConfig{
		Mode: AuthModeOAuth, AuthorizationServers: []string{"https://as.example"},
	}).Validate())

	require.ErrorContains(t, ok(""), "must not be empty")
	require.ErrorContains(t, ok("as.example"), "must be an absolute URL")
	// A port with no host: url.Host is ":443" and passes a bare non-empty
	// check, so the guard is on Hostname() instead.
	require.ErrorContains(t, ok("https://:443"), "must be an absolute URL")
	require.ErrorContains(t, ok("://nope"), "auth.authorization_servers")
	require.ErrorContains(t, ok("http://as.example"), "must use https")
	require.ErrorContains(t, ok("https://as.example?tenant=1"), "query or fragment")
	require.ErrorContains(t, ok("https://as.example#frag"), "query or fragment")

	// Meaningless where the OAuth chain never runs — including the bearer
	// mode inferred from a bare token, which would otherwise ignore it.
	for _, a := range []*AuthConfig{
		{Mode: AuthModeBearer, Token: "t", AuthorizationServers: []string{"https://as.example"}},
		{Token: "t", AuthorizationServers: []string{"https://as.example"}},
		{Mode: AuthModeNone, AuthorizationServers: []string{"https://as.example"}},
	} {
		require.ErrorContains(t, a.Validate(), "auth.authorization_servers is not used in mode")
	}

	// The field participates in cached-credential invalidation.
	cfg := ServerConfig{Transport: "streamable", URL: "https://mcp.example.com/mcp",
		Auth: &AuthConfig{AuthorizationServers: []string{"https://as.example"}}}
	sa, err := newServerAuth("srv", cfg, credentialStore{})
	require.NoError(t, err)
	require.True(t, sa.matches(cfg))
	changed := cfg
	changed.Auth = &AuthConfig{AuthorizationServers: []string{"https://other.example"}}
	require.False(t, sa.matches(changed))
	dropped := cfg
	dropped.Auth = nil
	require.False(t, sa.matches(dropped))

	// The validation is not merely advisory: newServerAuth is the only
	// production constructor of the state that reaches resolveIssuers, and it
	// refuses a bad entry, so an unvalidated issuer can never be dialled.
	bad := cfg
	bad.Auth = &AuthConfig{AuthorizationServers: []string{"http://evil.example"}}
	_, err = newServerAuth("srv", bad, credentialStore{})
	require.ErrorContains(t, err, `server "srv"`)
	require.ErrorContains(t, err, "must use https")
}
