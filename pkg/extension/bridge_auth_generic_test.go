package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

// fakeOAuthServer is a minimal OAuth 2.0 authorization server for tests.
// It serves an authorize endpoint that just records the request for the
// test, and a token endpoint that exchanges a hard-coded code for tokens.
type fakeOAuthServer struct {
	server *httptest.Server
	mu     sync.Mutex
	// Last seen authorize-request query params (for assertions).
	lastAuthorizeQuery url.Values
	// What the token endpoint should respond with.
	tokenResp string
	// Capture of the last token-endpoint request.
	lastTokenContentType string
	lastTokenBody        []byte
}

// snapshot returns the last-seen token-endpoint request fields under a
// single lock so test assertions don't race with the server goroutine.
func (f *fakeOAuthServer) snapshot() (contentType string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, len(f.lastTokenBody))
	copy(out, f.lastTokenBody)
	return f.lastTokenContentType, out
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	t.Helper()
	f := &fakeOAuthServer{
		tokenResp: `{"access_token":"AT","refresh_token":"RT","expires_in":3600,"token_type":"Bearer"}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastAuthorizeQuery = r.URL.Query()
		f.mu.Unlock()
		// Mimic what a real auth server does: redirect to the
		// callback URL with code+state on the query string. For
		// tests we just synthesise a code.
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastTokenContentType = r.Header.Get("Content-Type")
		f.lastTokenBody = body
		resp := f.tokenResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOAuthServer) authorizeURL() string { return f.server.URL + "/authorize" }
func (f *fakeOAuthServer) tokenURL() string     { return f.server.URL + "/token" }

// TestGenericAuthProvider_StandardFlow exercises the generic provider's
// happy path: PKCE + authorize URL + manual paste of the code + token
// exchange via pinoauth, with no extension hooks at all. Verifies the
// default credential mapping kicks in.
func TestGenericAuthProvider_StandardFlow(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	// We bypass the callback server (port-bind is flaky in CI) by
	// failing the bind via an unusable address and providing a
	// manual-paste callback that synthesises the code.
	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-generic",
				Name:               "Test Generic",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:          "test-client",
					AuthorizeURL:      srv.authorizeURL(),
					TokenURL:          srv.tokenURL(),
					Scope:             "read",
					CallbackAddr:      "256.0.0.1:0", // invalid → forces manual fallback
					ManualRedirectURI: "http://manual/callback",
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	provider := ai.GetOAuthProvider("test-generic")
	if provider == nil {
		t.Fatal("provider not registered")
	}

	// Simulate the user pasting a code by tracking what auth URL was
	// surfaced and what verifier it used (state == verifier).
	var (
		seenURLOnce sync.Once
		seenURL     string
	)
	creds, err := provider.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			seenURLOnce.Do(func() { seenURL = info.URL })
		},
		OnManualCodeInput: func() (string, error) {
			// Pull the state out of the auth URL we were given —
			// genericAuthProvider verifies state == verifier on
			// return. We mimic the user pasting the redirect URL.
			u, err := url.Parse(seenURL)
			if err != nil {
				return "", err
			}
			state := u.Query().Get("state")
			return fmt.Sprintf("http://manual/callback?code=THE_CODE&state=%s", state), nil
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if creds.Access != "AT" || creds.Refresh != "RT" {
		t.Errorf("default mapping: access=%q refresh=%q (expected AT/RT)", creds.Access, creds.Refresh)
	}
	if creds.Expires == 0 {
		t.Errorf("expected non-zero Expires from expires_in")
	}

	// Verify the token request hit the right endpoint with the right
	// body shape (form-encoded by default).
	ct, body := srv.snapshot()
	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		t.Errorf("token Content-Type = %q, want form", ct)
	}
	for _, frag := range []string{
		"grant_type=authorization_code",
		"code=THE_CODE",
		"client_id=test-client",
		"redirect_uri=http%3A%2F%2Fmanual%2Fcallback",
	} {
		if !strings.Contains(string(body), frag) {
			t.Errorf("token body missing %q in %q", frag, body)
		}
	}
}

// TestGenericAuthProvider_JSONBody verifies token_body_json=true
// switches to pinoauth.JSONBodyEncoder.
func TestGenericAuthProvider_JSONBody(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-json-body",
				Name:               "Test JSON Body",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:          "test-client",
					AuthorizeURL:      srv.authorizeURL(),
					TokenURL:          srv.tokenURL(),
					CallbackAddr:      "256.0.0.1:0",
					ManualRedirectURI: "http://manual/callback",
					TokenBodyJSON:     true,
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	provider := ai.GetOAuthProvider("test-json-body")

	var seenURL string
	_, err := provider.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) { seenURL = info.URL },
		OnManualCodeInput: func() (string, error) {
			u, err := url.Parse(seenURL)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("http://manual/callback?code=C&state=%s", u.Query().Get("state")), nil
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	ct, rawBody := srv.snapshot()
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("token Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("token body not JSON: %v (%q)", err, rawBody)
	}
	if body["grant_type"] != "authorization_code" || body["code"] != "C" {
		t.Errorf("unexpected JSON body: %v", body)
	}
}

// TestGenericAuthProvider_TokenBodyExtraStatePlaceholder verifies that
// the "{state}" placeholder in TokenBodyExtra values is substituted
// with the per-session OAuth state value on Exchange. This is the
// mechanism by which providers whose token endpoint requires state in
// the body (notably Anthropic's platform.claude.com — a non-standard
// quirk of the Claude-Code OAuth client) get that field. Regression
// guard for the v0.43.x → main extraction that briefly dropped the
// state passthrough.
func TestGenericAuthProvider_TokenBodyExtraStatePlaceholder(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-state-echo",
				Name:               "Test State Echo",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:          "test-client",
					AuthorizeURL:      srv.authorizeURL(),
					TokenURL:          srv.tokenURL(),
					CallbackAddr:      "256.0.0.1:0",
					ManualRedirectURI: "http://manual/callback",
					TokenBodyJSON:     true,
					TokenBodyExtra: map[string]string{
						"state":    "{state}",
						"audience": "test-aud",
					},
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	provider := ai.GetOAuthProvider("test-state-echo")

	var seenURL string
	var capturedState string
	_, err := provider.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) { seenURL = info.URL },
		OnManualCodeInput: func() (string, error) {
			u, err := url.Parse(seenURL)
			if err != nil {
				return "", err
			}
			capturedState = u.Query().Get("state")
			return fmt.Sprintf("http://manual/callback?code=C&state=%s", capturedState), nil
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if capturedState == "" {
		t.Fatal("did not observe state on authorize URL")
	}

	_, rawBody := srv.snapshot()
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("token body not JSON: %v (%q)", err, rawBody)
	}
	if body["state"] != capturedState {
		t.Errorf("token body state=%v, want %q (body=%v)", body["state"], capturedState, body)
	}
	if body["audience"] != "test-aud" {
		t.Errorf("token body audience=%v, want test-aud (no substitution on non-placeholder values)", body["audience"])
	}
}

// TestGenericAuthProvider_RefreshIncludesTokenBodyExtra verifies that
// TokenBodyExtra flows into the refresh request too — symmetry with
// Exchange. Refresh has no per-session state, so "{state}"
// placeholders substitute to the empty string on refresh.
func TestGenericAuthProvider_RefreshIncludesTokenBodyExtra(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)
	srv.tokenResp = `{"access_token":"NEW","refresh_token":"NEW_RT","expires_in":60,"token_type":"Bearer"}`

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-refresh-extra",
				Name:               "Test Refresh Extra",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:     "test-client",
					AuthorizeURL: srv.authorizeURL(),
					TokenURL:     srv.tokenURL(),
					TokenBodyExtra: map[string]string{
						"audience": "test-aud",
						"state":    "{state}", // → "" on refresh
					},
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	provider := ai.GetOAuthProvider("test-refresh-extra")
	_, err := provider.RefreshToken(context.Background(), &ai.OAuthCredentials{Refresh: "OLD_RT"})
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}

	_, body := srv.snapshot()
	if !strings.Contains(string(body), "audience=test-aud") {
		t.Errorf("refresh body missing audience: %q", body)
	}
	if !strings.Contains(string(body), "state=&") && !strings.HasSuffix(string(body), "state=") {
		t.Errorf("refresh body should contain empty state= (placeholder → empty on refresh): %q", body)
	}
}

// TestGenericAuthProvider_StandardRefresh exercises the default refresh
// path (no custom hook) — pinoauth.Refresh is called with the spec's
// token URL and credentials.
func TestGenericAuthProvider_StandardRefresh(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)
	srv.tokenResp = `{"access_token":"NEW_AT","refresh_token":"NEW_RT","expires_in":1800,"token_type":"Bearer"}`

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-refresh",
				Name:               "Test Refresh",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:     "test-client",
					AuthorizeURL: srv.authorizeURL(),
					TokenURL:     srv.tokenURL(),
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	provider := ai.GetOAuthProvider("test-refresh")

	creds := &ai.OAuthCredentials{Refresh: "OLD_RT", Access: "OLD_AT"}
	newCreds, err := provider.RefreshToken(context.Background(), creds)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if newCreds.Access != "NEW_AT" || newCreds.Refresh != "NEW_RT" {
		t.Errorf("refreshed creds: access=%q refresh=%q", newCreds.Access, newCreds.Refresh)
	}

	_, rawBody := srv.snapshot()
	body := string(rawBody)
	if !strings.Contains(body, "grant_type=refresh_token") {
		t.Errorf("refresh body missing grant_type=refresh_token: %q", body)
	}
	if !strings.Contains(body, "refresh_token=OLD_RT") {
		t.Errorf("refresh body missing refresh_token=OLD_RT: %q", body)
	}
}

// TestGenericAuthProvider_NoRefreshTokenError covers the explicit
// "no refresh token available" path when default-refresh is used.
func TestGenericAuthProvider_NoRefreshTokenError(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-no-rt",
				Name:               "Test No RT",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:     "c",
					AuthorizeURL: "https://x/auth",
					TokenURL:     "https://x/token",
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-no-rt")
	_, err := p.RefreshToken(context.Background(), &ai.OAuthCredentials{})
	if err == nil {
		t.Fatal("expected error from missing refresh token")
	}
	if !strings.Contains(err.Error(), "no refresh token") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGenericAuthProvider_CtxCancellation makes sure the flow honours
// the caller's context (Login waits on AwaitAuthCode, which selects on
// ctx.Done).
func TestGenericAuthProvider_CtxCancellation(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-cancel",
				Name:               "Test Cancel",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:     "c",
					AuthorizeURL: srv.authorizeURL(),
					TokenURL:     srv.tokenURL(),
					// Real bind so callback server is the only
					// path; we cancel ctx instead of providing
					// a manual-input handler.
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-cancel")
	ctx, cancel := context.WithCancel(context.Background())

	// onAuthCalled signals Login has reached the OAuth-URL display
	// step (which happens after PKCE + callback bind), so the cancel
	// below is guaranteed to land while AwaitAuthCode is blocked
	// rather than racing against earlier-stage code.
	onAuthCalled := make(chan struct{})

	var loginErr atomic.Value // error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := p.Login(ctx, pinoauth.LoginCallbacks{
			OnAuth: func(pinoauth.AuthInfo) { close(onAuthCalled) },
		})
		if err != nil {
			loginErr.Store(err)
		}
	}()

	select {
	case <-onAuthCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Login did not reach OnAuth callback within 2s")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Login did not return after ctx cancel")
	}
	if v := loginErr.Load(); v == nil {
		t.Fatal("expected error after ctx cancel, got nil")
	}
}

// TestGenericAuthProvider_StateMismatch verifies that a state value
// returned by the auth server which doesn't match the PKCE verifier
// fails the login (CSRF protection).
func TestGenericAuthProvider_StateMismatch(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-state-mismatch",
				Name:               "Test State Mismatch",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:          "c",
					AuthorizeURL:      srv.authorizeURL(),
					TokenURL:          srv.tokenURL(),
					CallbackAddr:      "256.0.0.1:0", // invalid → manual fallback
					ManualRedirectURI: "http://manual/cb",
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-state-mismatch")
	_, err := p.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(pinoauth.AuthInfo) {},
		OnManualCodeInput: func() (string, error) {
			// Hand-crafted redirect URL with the WRONG state.
			return "http://manual/cb?code=C&state=WRONG_STATE", nil
		},
	})
	if err == nil {
		t.Fatal("expected state-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGenericAuthProvider_DisableCallbackServer confirms the
// disable_callback_server: true path forces manual-paste with no
// local bind attempt.
func TestGenericAuthProvider_DisableCallbackServer(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-disabled",
				Name:               "Test Disabled CB",
				UsesCallbackServer: false,
				Flow: &OAuthFlowSpec{
					ClientID:              "c",
					AuthorizeURL:          srv.authorizeURL(),
					TokenURL:              srv.tokenURL(),
					DisableCallbackServer: true,
					ManualRedirectURI:     "http://custom-redirect/cb",
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-disabled")

	var seenURL string
	manualCalled := 0
	creds, err := p.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) { seenURL = info.URL },
		OnManualCodeInput: func() (string, error) {
			manualCalled++
			u, err := url.Parse(seenURL)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("http://custom-redirect/cb?code=X&state=%s", u.Query().Get("state")), nil
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if manualCalled != 1 {
		t.Errorf("OnManualCodeInput called %d times, want 1", manualCalled)
	}
	if creds.Access != "AT" {
		t.Errorf("creds.Access = %q, want AT", creds.Access)
	}
	// Verify the redirect_uri sent to the token endpoint is the
	// manual one, not a derived localhost URL.
	_, body := srv.snapshot()
	if !strings.Contains(string(body), "redirect_uri=http%3A%2F%2Fcustom-redirect%2Fcb") {
		t.Errorf("token body missing manual redirect_uri: %q", body)
	}
}

// TestGenericAuthProvider_DisableCallbackServerNoFallback covers the
// configuration error: disable_callback_server=true with no manual
// redirect URI is rejected up front.
func TestGenericAuthProvider_DisableCallbackServerNoFallback(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-bad-disabled",
				Name:               "Bad Disabled CB",
				UsesCallbackServer: false,
				Flow: &OAuthFlowSpec{
					ClientID:              "c",
					AuthorizeURL:          "https://x/auth",
					TokenURL:              "https://x/token",
					DisableCallbackServer: true,
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-bad-disabled")
	_, err := p.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(pinoauth.AuthInfo) {},
	})
	if err == nil {
		t.Fatal("expected error from disable_callback_server with no manual redirect")
	}
	if !strings.Contains(err.Error(), "manual_redirect_uri") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGenericAuthProvider_NoManualHandler covers the case where the
// callback server fails to bind AND no manual-input handler is
// supplied — Login must fail with a clear error rather than block
// forever.
func TestGenericAuthProvider_NoManualHandler(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-no-manual",
				Name:               "No Manual",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:          "c",
					AuthorizeURL:      srv.authorizeURL(),
					TokenURL:          srv.tokenURL(),
					CallbackAddr:      "256.0.0.1:0", // invalid → bind fails
					ManualRedirectURI: "http://manual/cb",
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-no-manual")
	_, err := p.Login(context.Background(), pinoauth.LoginCallbacks{
		OnAuth: func(pinoauth.AuthInfo) {},
		// OnManualCodeInput intentionally omitted.
	})
	if err == nil {
		t.Fatal("expected error when callback bind fails and no manual handler set")
	}
	if !strings.Contains(err.Error(), "manual input handler") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGenericAuthProvider_CallbackServerClosedOnReturn confirms the
// local OAuth callback server is shut down by the time Login returns,
// even when the parent ctx is long-lived (e.g. context.Background()
// from a CLI flow). Regression for a goroutine + listener leak that
// existed when the cleanup goroutine was tied only to the parent ctx.
func TestGenericAuthProvider_CallbackServerClosedOnReturn(t *testing.T) {
	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()

	srv := newFakeOAuthServer(t)

	caps := &InitResult{
		Name: "test-ext",
		AuthProviders: []AuthProviderSpec{
			{
				ID:                 "test-leak",
				Name:               "Test Leak",
				UsesCallbackServer: true,
				Flow: &OAuthFlowSpec{
					ClientID:     "c",
					AuthorizeURL: srv.authorizeURL(),
					TokenURL:     srv.tokenURL(),
					CallbackPath: "/cb",
					// Real bind on auto-port so we get a live listener.
				},
			},
		},
	}
	bridge := NewBridge(nil, caps)
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	p := ai.GetOAuthProvider("test-leak")

	// Use context.Background() — the parent never cancels.
	parent := context.Background()
	var redirect string
	creds, err := p.Login(parent, pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			u, _ := url.Parse(info.URL)
			redirect = u.Query().Get("redirect_uri")
		},
		OnManualCodeInput: func() (string, error) {
			// Simulate the user pasting a redirect URL with a
			// matching state, completing Login synchronously.
			ru, err := url.Parse(redirect)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf(
				"http://%s%s?code=C&state=%s",
				ru.Host, ru.Path, "STATE_PLACEHOLDER",
			), nil
		},
	})
	// State will mismatch because we hardcoded STATE_PLACEHOLDER, but
	// that's fine — we only care that Login returned (didn't hang) and
	// the callback listener bound during Login is no longer accepting
	// connections.
	_ = creds
	_ = err

	// The redirect_uri the spec built points at the local listener.
	// Try to dial it; the connection should be refused (server closed).
	if redirect == "" {
		t.Fatal("OnAuth was not called with an authorize URL")
	}
	ru, parseErr := url.Parse(redirect)
	if parseErr != nil {
		t.Fatalf("redirect parse: %v", parseErr)
	}
	// Give the deferred cancel + close goroutine a moment to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		conn, dErr := net.DialTimeout("tcp", ru.Host, 50*time.Millisecond)
		if dErr != nil {
			return // listener gone — leak fixed
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("callback server still accepting connections at %s after Login returned", ru.Host)
}
