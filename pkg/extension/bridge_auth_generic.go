package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

// genericAuthProvider drives a standard OAuth 2.0 authorization-code flow
// with PKCE entirely from Go (via pinoauth), using the static config in
// [AuthProviderSpec.Flow]. The extension is consulted only for genuinely
// provider-specific steps (post-exchange enrichment, api_key resolution,
// model listing, model header injection, custom refresh).
//
// This avoids the JSON-RPC round-trips an imperative extension would
// otherwise make for every PKCE/callback/browser/exchange step.
type genericAuthProvider struct {
	spec   AuthProviderSpec
	bridge *Bridge
}

func (p *genericAuthProvider) ID() string               { return p.spec.ID }
func (p *genericAuthProvider) Name() string             { return p.spec.Name }
func (p *genericAuthProvider) UsesCallbackServer() bool { return p.spec.UsesCallbackServer }

// Login runs the full authorization-code+PKCE flow without bridge calls
// for the generic steps.
func (p *genericAuthProvider) Login(ctx context.Context, callbacks pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	// Stash callbacks/ctx so any optional post_exchange hook can use
	// the same context plumbing as the imperative extAuthProvider.
	//
	// NOTE: the bridge has a single-slot authCtx/authCallbacks pair —
	// concurrent logins on the same bridge would race silently. That
	// matches the pre-existing extAuthProvider contract; both provider
	// types assume at most one active login per bridge.
	p.bridge.setAuthCallbacks(ctx, &callbacks)
	defer p.bridge.setAuthCallbacks(nil, nil)

	// Local cancel scope so callback-server lifecycle and the watchdog
	// goroutine inside openCallbackChannel are torn down on every
	// Login return path — not just on caller-supplied ctx cancellation.
	// Without this the listener + goroutine leak for the lifetime of
	// the parent ctx (often context.Background() in CLI flows).
	loginCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	fl := p.spec.Flow
	if fl == nil {
		return nil, fmt.Errorf("provider %q: missing flow spec", p.spec.ID)
	}

	pkce := pinoauth.GeneratePKCE()

	callbackCh, redirectURI, err := p.openCallbackChannel(loginCtx, fl, pkce.Verifier)
	if err != nil {
		return nil, err
	}

	authURL := buildAuthorizeURL(fl, pkce, redirectURI)
	shortURL := buildShortURL(fl, pkce, redirectURI)

	if callbacks.OnAuth != nil {
		callbacks.OnAuth(pinoauth.AuthInfo{URL: authURL, ShortURL: shortURL, Instructions: fl.OpenURLInstructions})
	}
	if callbacks.OnProgress != nil {
		callbacks.OnProgress("Waiting for OAuth callback...")
	}

	code, state, err := awaitCode(loginCtx, callbackCh, callbacks, pkce.Verifier)
	if err != nil {
		return nil, err
	}
	if state != pkce.Verifier {
		return nil, fmt.Errorf("OAuth state mismatch")
	}

	if callbacks.OnProgress != nil {
		callbacks.OnProgress("Exchanging authorization code for tokens...")
	}

	tok, err := p.tokenClient(fl).Exchange(loginCtx, pinoauth.ExchangeRequest{
		Code:         code,
		CodeVerifier: pkce.Verifier,
		RedirectURI:  redirectURI,
	})
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	return p.tokenToCredentials(loginCtx, tok, nil)
}

// tokenClient builds a pinoauth.Client from the static flow spec. The
// returned client is safe to use for both Exchange and Refresh on this
// provider (both share TokenURL / ClientID / ClientSecret / headers /
// body encoder).
func (p *genericAuthProvider) tokenClient(fl *OAuthFlowSpec) *pinoauth.Client {
	headers := http.Header{}
	for k, v := range fl.TokenHeaders {
		headers.Set(k, v)
	}
	c := &pinoauth.Client{
		TokenURL:     fl.TokenURL,
		ClientID:     fl.ClientID,
		ClientSecret: fl.ClientSecret,
		Headers:      headers,
	}
	if fl.TokenBodyJSON {
		c.BodyEncoder = pinoauth.JSONBodyEncoder
	}
	return c
}

// openCallbackChannel binds the local OAuth callback server (or arranges
// the manual-paste fallback when the spec disables/can't bind one). It
// returns a channel for the redirect result (nil when binding was
// skipped or failed but a manual fallback is configured), the redirect
// URI to embed in the authorize URL, and any fatal error.
//
// On a successful bind, lifecycle (Close) is wired into ctx via a
// goroutine; the caller does not need to track the *http.Server.
func (p *genericAuthProvider) openCallbackChannel(
	ctx context.Context,
	fl *OAuthFlowSpec,
	state string,
) (<-chan *pinoauth.CallbackResult, string, error) {
	if fl.DisableCallbackServer {
		// Caller forced the manual flow (e.g. custom non-loopback
		// redirect URI registered with the OAuth provider).
		if fl.ManualRedirectURI == "" {
			return nil, "", fmt.Errorf("callback server disabled but no manual_redirect_uri configured")
		}
		return nil, fl.ManualRedirectURI, nil
	}

	addr := fl.CallbackAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	path := fl.CallbackPath
	if path == "" {
		path = "/callback"
	}

	srv, ch, resolvedAddr, bindErr := pinoauth.StartCallbackServer(ctx, path, addr, state)
	if bindErr != nil {
		if fl.ManualRedirectURI == "" {
			return nil, "", fmt.Errorf("starting callback server: %w", bindErr)
		}
		// Bind failed but caller provided a manual fallback URI.
		return nil, fl.ManualRedirectURI, nil
	}

	// Close the server when ctx is done so a cancelled login doesn't
	// leak the listener. We can't defer here because the channel must
	// outlive this function.
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	_, port, _ := net.SplitHostPort(resolvedAddr)
	redirectURI := fmt.Sprintf("http://localhost:%s%s", port, path)
	return ch, redirectURI, nil
}

// RefreshToken refreshes credentials. Default behaviour calls
// pinoauth.Refresh with the same static config; if the spec opts in via
// HasCustomRefresh, the bridge dispatches the auth/refresh JSON-RPC hook
// instead — when that hook is set the extension owns the entire refresh
// (including any post-exchange enrichment), so post_exchange is NOT
// invoked again on top.
func (p *genericAuthProvider) RefreshToken(ctx context.Context, creds *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	fl := p.spec.Flow
	if fl == nil {
		return nil, fmt.Errorf("provider %q: missing flow spec", p.spec.ID)
	}

	if fl.HasCustomRefresh {
		return p.bridge.callExtRefresh(ctx, p.spec.ID, creds)
	}

	if creds == nil || creds.Refresh == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	tok, err := p.tokenClient(fl).Refresh(ctx, pinoauth.RefreshRequest{
		RefreshToken: creds.Refresh,
	})
	if err != nil {
		return nil, fmt.Errorf("token refresh: %w", err)
	}
	// Many providers (e.g. Google) omit the refresh_token on a refresh
	// response when nothing changed — preserve the existing one rather
	// than blanking it out and forcing a re-login on the next refresh.
	if tok.RefreshToken == "" {
		tok.RefreshToken = creds.Refresh
	}
	return p.tokenToCredentials(ctx, tok, creds)
}

// GetAPIKey delegates to the auth/api_key extension hook when present.
// Default returns the access token directly — covers every Bearer-style
// provider in fir today (Anthropic, Codex, Antigravity, Gemini-CLI, Poe).
// Extensions that need different behaviour (Copilot uses a separate
// short-lived token) keep the imperative extAuthProvider.
func (p *genericAuthProvider) GetAPIKey(creds *ai.OAuthCredentials) string {
	// Defer to the bridge's standard auth/api_key dispatch so an
	// extension can override the trivial default.
	if key, ok := p.bridge.callExtAPIKey(p.spec.ID, creds); ok {
		return key
	}
	if creds == nil {
		return ""
	}
	return creds.Access
}

// ListModels delegates to the auth/list_models extension hook. Returning
// nil here means "no live list, use the static registry" — the same
// semantic the imperative extAuthProvider has when the hook is absent.
func (p *genericAuthProvider) ListModels(ctx context.Context, creds *ai.OAuthCredentials) ([]string, error) {
	return p.bridge.callExtListModels(ctx, p.spec.ID, creds)
}

// ModifyModels delegates to the auth/modify_models extension hook.
func (p *genericAuthProvider) ModifyModels(models []*ai.Model, creds *ai.OAuthCredentials) []*ai.Model {
	return p.bridge.callExtModifyModels(p.spec.ID, creds, models)
}

// ModelDefaults delegates to the auth/model_defaults extension hook.
func (p *genericAuthProvider) ModelDefaults(modelID string, siblings []*ai.Model) *ai.Model {
	return p.bridge.callExtModelDefaults(p.spec.ID, modelID, siblings)
}

// tokenToCredentials maps a parsed pinoauth.Token to fir's stored
// Credentials shape, optionally routing through the auth/post_exchange
// extension hook when HasPostExchange is set. previous, when non-nil,
// is the credential record being refreshed — passed through to the
// hook so it can preserve provider-specific extras (project IDs, etc.).
func (p *genericAuthProvider) tokenToCredentials(ctx context.Context, tok *pinoauth.Token, previous *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	if p.spec.Flow.HasPostExchange {
		return p.callExtPostExchange(ctx, tok, previous)
	}
	return defaultCredsFromToken(tok), nil
}

// defaultCredsFromToken is the identity mapping for providers that don't
// need post-exchange enrichment. When the upstream token has no expiry
// (server omitted expires_in), Credentials.Expires is left at 0, which
// fir's auth storage interprets as "never expires" — appropriate only
// for genuinely non-expiring tokens. Providers whose tokens really do
// expire but lack expires_in must opt into HasPostExchange and synthesise
// a sensible expiry there.
func defaultCredsFromToken(tok *pinoauth.Token) *ai.OAuthCredentials {
	if tok == nil {
		return &ai.OAuthCredentials{}
	}
	c := &ai.OAuthCredentials{
		Access:  tok.AccessToken,
		Refresh: tok.RefreshToken,
	}
	if !tok.ExpiresAt.IsZero() {
		c.Expires = tok.ExpiresAt.UnixMilli()
	}
	return c
}

// awaitCode waits for the authorization code: either through the
// callback server (when bound) or from a manual paste prompt as a
// pure fallback when the callback server failed to start.
func awaitCode(
	ctx context.Context,
	callbackCh <-chan *pinoauth.CallbackResult,
	callbacks pinoauth.LoginCallbacks,
	verifier string,
) (string, string, error) {
	if callbackCh != nil {
		// Callback server bound — race it against optional manual paste.
		return pinoauth.AwaitAuthCode(ctx, callbackCh, callbacks.OnManualCodeInput, callbacks.OnDismissManualInput)
	}
	// Pure manual fallback (callback server failed to bind, or the
	// spec disabled it entirely).
	if callbacks.OnManualCodeInput == nil {
		return "", "", fmt.Errorf("callback server unavailable and no manual input handler")
	}
	input, err := callbacks.OnManualCodeInput()
	if err != nil {
		return "", "", err
	}
	code, state := pinoauth.ParseAuthorizationInput(input)
	if state == "" {
		// SECURITY: when the user pastes a bare authorization code
		// (not a full redirect URL with `&state=...`), there is no
		// state value to verify against the CSRF token (verifier).
		// We accept this with the verifier substituted in so the
		// downstream `state == verifier` check passes — effectively
		// bypassing CSRF protection on the manual-paste-bare-code
		// path. This is the same trade-off the imperative extensions
		// made before this refactor: a hand-pasted code can't be
		// verified, but refusing it would block users on locked-down
		// machines where the local callback server can't bind.
		state = verifier
	}
	return code, state, nil
}

// buildAuthorizeURL composes the authorization endpoint URL with the
// standard parameters plus any spec-supplied extras. State is always
// the PKCE verifier — fir's convention shared across every authcode
// extension today.
func buildAuthorizeURL(fl *OAuthFlowSpec, pkce *pinoauth.PKCEChallenge, redirectURI string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", fl.ClientID)
	q.Set("redirect_uri", redirectURI)
	if fl.Scope != "" {
		q.Set("scope", fl.Scope)
	}
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", pkce.Verifier)
	for k, v := range fl.AuthParamsExtra {
		q.Set(k, v)
	}
	sep := "?"
	if u, err := url.Parse(fl.AuthorizeURL); err == nil && u.RawQuery != "" {
		// Preserve any pre-existing query string on the spec URL.
		sep = "&"
	}
	return fl.AuthorizeURL + sep + q.Encode()
}

// buildShortURL composes a pre-shortened authorize URL by appending only
// the per-session params (redirect_uri, code_challenge, state) to the
// configured ShortURLBase. The URL shortener is expected to merge these
// with its stored target (the static portion of the authorize URL).
// Returns "" when no ShortURLBase is configured.
func buildShortURL(fl *OAuthFlowSpec, pkce *pinoauth.PKCEChallenge, redirectURI string) string {
	if fl.ShortURLBase == "" {
		return ""
	}
	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("state", pkce.Verifier)
	return fl.ShortURLBase + "?" + q.Encode()
}

// --- Bridge-side hook dispatchers (only the ones the generic provider needs) ---

// callExtPostExchange invokes the auth/post_exchange JSON-RPC hook with
// the parsed token shape and returns the resulting credentials. previous,
// when non-nil, is the credential record being refreshed (allowing the
// hook to preserve extras like a Google project ID across refresh).
func (p *genericAuthProvider) callExtPostExchange(ctx context.Context, tok *pinoauth.Token, previous *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	params := map[string]any{
		"provider_id": p.spec.ID,
		"token":       tokenToWire(tok),
	}
	if previous != nil {
		params["previous_credentials"] = previous
	}
	raw, err := p.bridge.CallHook(ctx, "auth/post_exchange", params, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("auth/post_exchange: %w", err)
	}
	var result struct {
		Credentials *ai.OAuthCredentials `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("auth/post_exchange: invalid response: %w", err)
	}
	if result.Credentials == nil {
		return nil, fmt.Errorf("auth/post_exchange: no credentials returned")
	}
	return result.Credentials, nil
}

// tokenToWire serialises a *pinoauth.Token to the JSON shape extensions
// receive in auth/post_exchange. Mirrors RFC 6749 §5.1 (access_token,
// refresh_token, token_type, scope, expires_at as epoch ms) plus the
// raw provider-specific fields under "raw".
//
// Naming: this is the "raw token" shape — `access_token` / `expires_at`,
// matching the OAuth wire spec. The post_exchange hook returns a
// "stored credentials" shape — `access` / `expires` — that fir persists
// in auth.json. The two naming conventions are intentional and reflect
// the layer transition; ext authors should be careful not to conflate.
func tokenToWire(tok *pinoauth.Token) map[string]any {
	if tok == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"access_token":  tok.AccessToken,
		"refresh_token": tok.RefreshToken,
		"token_type":    tok.TokenType,
		"scope":         tok.Scope,
		"raw":           tok.Raw,
	}
	if !tok.ExpiresAt.IsZero() {
		out["expires_at"] = tok.ExpiresAt.UnixMilli()
	}
	return out
}

// --- Bridge hook helpers shared with extAuthProvider ---
//
// These wrap the same auth/* JSON-RPC methods the imperative path uses
// so genericAuthProvider can present a unified surface without
// duplicating every CallHook dance. Keeping them on *Bridge so future
// callers (CLI, ACP) can reuse them.

// callExtRefresh invokes the auth/refresh JSON-RPC hook on the
// extension that owns providerID. Used by both extAuthProvider (always)
// and genericAuthProvider (only when the spec sets HasCustomRefresh).
func (b *Bridge) callExtRefresh(ctx context.Context, providerID string, creds *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	params := map[string]any{
		"provider_id": providerID,
		"credentials": creds,
	}
	raw, err := b.CallHook(ctx, "auth/refresh", params, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("auth/refresh: %w", err)
	}
	var result struct {
		Credentials *ai.OAuthCredentials `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("auth/refresh: invalid response: %w", err)
	}
	if result.Credentials == nil {
		return nil, fmt.Errorf("auth/refresh: no credentials returned")
	}
	return result.Credentials, nil
}

func (b *Bridge) callExtAPIKey(providerID string, creds *ai.OAuthCredentials) (string, bool) {
	params := map[string]any{
		"provider_id": providerID,
		"credentials": creds,
	}
	raw, err := b.CallHook(context.Background(), "auth/api_key", params, 10*time.Second)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	var result struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", false
	}
	return result.APIKey, true
}

func (b *Bridge) callExtListModels(ctx context.Context, providerID string, creds *ai.OAuthCredentials) ([]string, error) {
	params := map[string]any{
		"provider_id": providerID,
		"credentials": creds,
	}
	raw, err := b.CallHook(ctx, "auth/list_models", params, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("auth/list_models: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var result struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("auth/list_models: invalid response: %w", err)
	}
	return result.Models, nil
}

func (b *Bridge) callExtModifyModels(providerID string, creds *ai.OAuthCredentials, models []*ai.Model) []*ai.Model {
	params := map[string]any{
		"provider_id": providerID,
		"credentials": creds,
		"models":      models,
	}
	raw, err := b.CallHook(context.Background(), "auth/modify_models", params, 10*time.Second)
	if err != nil {
		return nil
	}
	var result struct {
		Models []*ai.Model `json:"models"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return result.Models
}

func (b *Bridge) callExtModelDefaults(providerID, modelID string, siblings []*ai.Model) *ai.Model {
	siblingIDs := make([]string, len(siblings))
	for i, s := range siblings {
		siblingIDs[i] = s.ID
	}
	params := map[string]any{
		"provider_id": providerID,
		"model_id":    modelID,
		"sibling_ids": siblingIDs,
	}
	raw, err := b.CallHook(context.Background(), "auth/model_defaults", params, 5*time.Second)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var result struct {
		Model *ai.Model `json:"model"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return result.Model
}
