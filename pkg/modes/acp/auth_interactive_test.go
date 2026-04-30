package acp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/auth"
)

// scriptedOAuthProvider is a test double that runs a user-supplied script
// against the LoginCallbacks so each test can simulate a particular flow
// (URL emit, paste request, error, etc.).
type scriptedOAuthProvider struct {
	id    string
	login func(callbacks oauth.LoginCallbacks) (*oauth.Credentials, error)
}

func (p *scriptedOAuthProvider) ID() string               { return p.id }
func (p *scriptedOAuthProvider) Name() string             { return "Scripted " + p.id }
func (p *scriptedOAuthProvider) UsesCallbackServer() bool { return true }
func (p *scriptedOAuthProvider) Login(cb oauth.LoginCallbacks) (*oauth.Credentials, error) {
	return p.login(cb)
}
func (p *scriptedOAuthProvider) RefreshToken(creds *oauth.Credentials) (*oauth.Credentials, error) {
	return creds, nil
}
func (p *scriptedOAuthProvider) GetAPIKey(creds *oauth.Credentials) string {
	if creds == nil {
		return ""
	}
	return creds.Access
}
func (p *scriptedOAuthProvider) ListModels(_ context.Context, _ *oauth.Credentials) ([]string, error) {
	return nil, nil
}
func (p *scriptedOAuthProvider) ModifyModels(models []*ai.Model, _ *oauth.Credentials) []*ai.Model {
	return models
}

// newAgentForAuthTest returns a firAgent ready for interactive auth tests,
// with a single scripted OAuth provider registered. It returns a cleanup
// that unregisters the provider.
func newAgentForAuthTest(t *testing.T, providerID string, login func(oauth.LoginCallbacks) (*oauth.Credentials, error)) (*firAgent, func()) {
	t.Helper()
	prov := &scriptedOAuthProvider{id: providerID, login: login}
	oauth.RegisterProvider(prov)
	pa := &firAgent{
		sessions:     make(map[string]*firSession),
		pendingAuths: make(map[string]*pendingAuth),
		authStorage:  auth.NewInMemoryAuthStorage(nil),
		authMethods: []ExtendedAuthMethod{
			{Id: "oauth-" + providerID, Name: "Scripted", Type: AuthMethodTypeAgent},
		},
	}
	return pa, func() { oauth.UnregisterProvider(providerID) }
}

func TestInteractiveAuth_HappyPath(t *testing.T) {
	const wantURL = "https://example.com/authorize?x=1"
	const wantPaste = "https://localhost/cb?code=abc&state=xyz"

	pasted := make(chan string, 1)
	pa, cleanup := newAgentForAuthTest(t, "scripted-happy", func(cb oauth.LoginCallbacks) (*oauth.Credentials, error) {
		cb.OnAuth(oauth.AuthInfo{URL: wantURL, Instructions: "go here"})
		v, err := cb.OnManualCodeInput()
		if err != nil {
			return nil, err
		}
		pasted <- v
		return &oauth.Credentials{Access: "tok"}, nil
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Call 1.
	resp, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-happy",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	gotState, gotID, gotURL, _ := authMetaFromResponse(t, resp)
	if gotState != authStateNeedsRedirect {
		t.Fatalf("state = %q, want needs_redirect", gotState)
	}
	if gotURL != wantURL {
		t.Fatalf("url = %q, want %q", gotURL, wantURL)
	}
	if gotID == "" {
		t.Fatal("missing id in call 1 response")
	}

	// Call 2.
	resp2, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-happy",
		Meta: map[string]any{"auth": map[string]any{
			"interactive": true,
			"id":          gotID,
			"redirect":    wantPaste,
		}},
	})
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if state, _, _, _ := authMetaFromResponse(t, resp2); state != authStateOK {
		t.Fatalf("state = %q, want ok", state)
	}

	select {
	case got := <-pasted:
		if got != wantPaste {
			t.Errorf("provider received paste = %q, want %q", got, wantPaste)
		}
	case <-time.After(time.Second):
		t.Fatal("provider never received paste")
	}

	// Pending entry must be cleaned up.
	if pa.lookupPendingAuth(gotID) != nil {
		t.Error("pendingAuth not cleaned up after successful login")
	}
}

func TestInteractiveAuth_CachedCredsNoUserInput(t *testing.T) {
	// Login completes immediately without ever calling OnAuth (cached creds).
	pa, cleanup := newAgentForAuthTest(t, "scripted-cached", func(_ oauth.LoginCallbacks) (*oauth.Credentials, error) {
		return &oauth.Credentials{Access: "tok"}, nil
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-cached",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if state, _, _, _ := authMetaFromResponse(t, resp); state != authStateOK {
		t.Fatalf("state = %q, want ok", state)
	}
	// Cached-creds path: no id needs to be remembered because there's no
	// pending entry — verify there are zero entries.
	pa.mu.Lock()
	n := len(pa.pendingAuths)
	pa.mu.Unlock()
	if n != 0 {
		t.Errorf("pendingAuths leak: %d", n)
	}
}

func TestInteractiveAuth_LoginErrorBeforeURL(t *testing.T) {
	bad := errors.New("boom")
	pa, cleanup := newAgentForAuthTest(t, "scripted-err", func(_ oauth.LoginCallbacks) (*oauth.Credentials, error) {
		return nil, bad
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-err",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	pa.mu.Lock()
	n := len(pa.pendingAuths)
	pa.mu.Unlock()
	if n != 0 {
		t.Errorf("pendingAuths leak after early-error: %d", n)
	}
}

func TestInteractiveAuth_Cancel(t *testing.T) {
	releaseLogin := make(chan struct{})
	pa, cleanup := newAgentForAuthTest(t, "scripted-cancel", func(cb oauth.LoginCallbacks) (*oauth.Credentials, error) {
		cb.OnAuth(oauth.AuthInfo{URL: "https://example/auth"})
		// Block in OnManualCodeInput until the test cancels us.
		_, err := cb.OnManualCodeInput()
		<-releaseLogin
		return nil, err
	})
	defer cleanup()
	defer close(releaseLogin)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Call 1.
	resp, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-cancel",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	_, gotID, _, _ := authMetaFromResponse(t, resp)
	if gotID == "" {
		t.Fatal("missing id")
	}

	// Cancel.
	resp2, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-cancel",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true, "cancel": true, "id": gotID}},
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if state, _, _, _ := authMetaFromResponse(t, resp2); state != authStateCancelled {
		t.Fatalf("state = %q, want cancelled", state)
	}
	if pa.lookupPendingAuth(gotID) != nil {
		t.Error("pendingAuth not removed on cancel")
	}
}

func TestInteractiveAuth_RedirectWithoutPending(t *testing.T) {
	pa, cleanup := newAgentForAuthTest(t, "scripted-orphan", func(_ oauth.LoginCallbacks) (*oauth.Credentials, error) {
		return &oauth.Credentials{Access: "tok"}, nil
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-orphan",
		Meta: map[string]any{"auth": map[string]any{
			"interactive": true,
			"id":          "auth-bogus",
			"redirect":    "https://x/cb?code=1",
		}},
	})
	if err == nil {
		t.Fatal("expected error for redirect without pending login")
	}
}

func TestInteractiveAuth_LegacyPathUnchanged(t *testing.T) {
	// Without _meta.auth.interactive, the legacy authenticateOAuth branch
	// is taken. Our scripted provider returns UsesCallbackServer()==true so
	// it bypasses the "interactive input not supported" guard, but the
	// legacy path runs the full Login synchronously.
	called := make(chan struct{}, 1)
	pa, cleanup := newAgentForAuthTest(t, "scripted-legacy", func(cb oauth.LoginCallbacks) (*oauth.Credentials, error) {
		// Legacy callbacks: OnAuth tries to open a browser server-side
		// (we don't actually open one here; just confirm the path is taken
		// by observing that no OnManualCodeInput is ever wired).
		if cb.OnManualCodeInput != nil {
			t.Error("legacy path should not wire OnManualCodeInput")
		}
		called <- struct{}{}
		return &oauth.Credentials{Access: "tok"}, nil
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-legacy",
	}); err != nil {
		t.Fatalf("legacy authenticate: %v", err)
	}

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("legacy Login was not invoked")
	}
}

func TestInteractiveAuth_ConcurrentSameMethod(t *testing.T) {
	// Two concurrent call-1s for the same methodId now coexist (each gets
	// a unique id). Pasting to id-1 must not affect the flow keyed by id-2.
	var calls atomic.Int32
	type slot struct {
		paste chan string
		done  chan string // value Login received
	}
	slots := []slot{
		{paste: make(chan string), done: make(chan string, 1)},
		{paste: make(chan string), done: make(chan string, 1)},
	}

	pa, cleanup := newAgentForAuthTest(t, "scripted-concurrent", func(cb oauth.LoginCallbacks) (*oauth.Credentials, error) {
		idx := int(calls.Add(1)) - 1
		cb.OnAuth(oauth.AuthInfo{URL: "https://login/" + string(rune('A'+idx))})
		// Tie this Login goroutine's paste read to its dedicated channel.
		// The OnManualCodeInput callback registered by startPendingAuth
		// reads from the pending's internal paste channel — we don't have
		// access to it here. Use OnManualCodeInput indirectly: each call
		// will drain whichever paste comes in for its pending. Just record
		// what we got.
		v, err := cb.OnManualCodeInput()
		if err != nil {
			return nil, err
		}
		slots[idx].done <- v
		return &oauth.Credentials{Access: "tok"}, nil
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start two pendings.
	r1, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-concurrent",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	_, id1, _, _ := authMetaFromResponse(t, r1)
	r2, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-concurrent",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	_, id2, _, _ := authMetaFromResponse(t, r2)
	if id1 == id2 || id1 == "" || id2 == "" {
		t.Fatalf("ids must be distinct and non-empty: %q %q", id1, id2)
	}

	// Complete both, in reverse order.
	if _, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-concurrent",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true, "id": id2, "redirect": "paste-2"}},
	}); err != nil {
		t.Fatalf("complete 2: %v", err)
	}
	if _, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-scripted-concurrent",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true, "id": id1, "redirect": "paste-1"}},
	}); err != nil {
		t.Fatalf("complete 1: %v", err)
	}

	// Verify each Login received its own paste — login goroutines run in
	// the order calls Add(1) returns; both should have completed.
	got := []string{<-slots[0].done, <-slots[1].done}
	want := map[string]bool{"paste-1": false, "paste-2": false}
	for _, g := range got {
		want[g] = true
	}
	if !want["paste-1"] || !want["paste-2"] {
		t.Errorf("each Login should have received exactly one of {paste-1, paste-2}; got %v", got)
	}
}

// authMetaFromResponse extracts state/id/url/instructions from an authenticate
// response's Meta. Fails the test on shape mismatch.
func authMetaFromResponse(t *testing.T, resp acpsdk.AuthenticateResponse) (state, id, url, instructions string) {
	t.Helper()
	m, ok := resp.Meta.(map[string]any)
	if !ok {
		t.Fatalf("response Meta is %T, want map[string]any", resp.Meta)
	}
	a, ok := m["auth"].(map[string]any)
	if !ok {
		t.Fatalf("response Meta.auth is %T, want map[string]any", m["auth"])
	}
	state, _ = a["state"].(string)
	id, _ = a["id"].(string)
	url, _ = a["url"].(string)
	instructions, _ = a["instructions"].(string)
	return
}
