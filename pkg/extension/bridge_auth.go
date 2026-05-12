package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/pinoauth"
)

// extAuthProvider adapts an extension-provided auth provider to the
// ai.OAuthProvider interface. It sends JSON-RPC requests to the extension
// process and handles bidirectional communication during login.
type extAuthProvider struct {
	spec   AuthProviderSpec
	bridge *Bridge

	// callbackMu protects the active callback server state.
	callbackMu  sync.Mutex
	callbackSrv *http.Server
	callbackCh  <-chan *pinoauth.CallbackResult
}

func (p *extAuthProvider) ID() string               { return p.spec.ID }
func (p *extAuthProvider) Name() string             { return p.spec.Name }
func (p *extAuthProvider) UsesCallbackServer() bool { return p.spec.UsesCallbackServer }

func (p *extAuthProvider) Login(ctx context.Context, callbacks pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	// Store callbacks + ctx so the bridge can dispatch UI requests and
	// callback-server hooks from the extension.
	p.bridge.setAuthCallbacks(ctx, &callbacks)
	defer p.bridge.setAuthCallbacks(nil, nil)

	params := map[string]any{
		"provider_id": p.spec.ID,
	}
	// Use a long timeout for login (5 minutes) since it's interactive.
	raw, err := p.bridge.CallHook(ctx, "auth/login", params, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("auth/login: %w", err)
	}

	var result struct {
		Credentials *ai.OAuthCredentials `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("auth/login: invalid response: %w", err)
	}
	if result.Credentials == nil {
		return nil, fmt.Errorf("auth/login: no credentials returned")
	}
	return result.Credentials, nil
}

func (p *extAuthProvider) ListModels(ctx context.Context, creds *ai.OAuthCredentials) ([]string, error) {
	return p.bridge.callExtListModels(ctx, p.spec.ID, creds)
}

func (p *extAuthProvider) RefreshToken(ctx context.Context, creds *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return p.bridge.callExtRefresh(ctx, p.spec.ID, creds)
}

func (p *extAuthProvider) GetAPIKey(creds *ai.OAuthCredentials) string {
	if key, ok := p.bridge.callExtAPIKey(p.spec.ID, creds); ok {
		return key
	}
	if creds == nil {
		return ""
	}
	return creds.Access
}

func (p *extAuthProvider) ModifyModels(models []*ai.Model, creds *ai.OAuthCredentials) []*ai.Model {
	return p.bridge.callExtModifyModels(p.spec.ID, creds, models)
}

// ModelDefaults satisfies ai.OAuthProvider. Extensions can implement the
// "auth/model_defaults" JSON-RPC hook to provide metadata for live-listed
// model IDs not in the built-in registry. Returning nil (or omitting the
// hook) defers to the generic sibling-clone fallback.
//
// Only sibling IDs are sent over the wire — extensions that need full sibling
// metadata can fetch it via a follow-up `models/get` call (or replicate the
// info themselves). Keeps the RPC payload small even for providers with
// hundreds of registered models.
func (p *extAuthProvider) ModelDefaults(modelID string, siblings []*ai.Model) *ai.Model {
	return p.bridge.callExtModelDefaults(p.spec.ID, modelID, siblings)
}

// StartCallbackServer starts a local OAuth callback server for the extension.
func (p *extAuthProvider) StartCallbackServer(ctx context.Context, addr, path, state string) (string, string, error) {
	p.callbackMu.Lock()
	defer p.callbackMu.Unlock()

	if p.callbackSrv != nil {
		return "", "", fmt.Errorf("callback server already running")
	}

	srv, ch, resolvedAddr, err := pinoauth.StartCallbackServer(ctx, path, addr, state)
	if err != nil {
		return "", "", err
	}
	p.callbackSrv = srv
	p.callbackCh = ch

	// Use the resolved address (which has the actual port if 0 was requested).
	// The redirect URI uses "localhost" instead of the IP for OAuth compatibility.
	_, port, _ := net.SplitHostPort(resolvedAddr)
	redirectURI := fmt.Sprintf("http://localhost:%s%s", port, path)
	return resolvedAddr, redirectURI, nil
}

// AwaitCallback waits for the callback server to receive an auth code.
func (p *extAuthProvider) AwaitCallback(ctx context.Context) (string, string, error) {
	p.callbackMu.Lock()
	ch := p.callbackCh
	p.callbackMu.Unlock()

	if ch == nil {
		return "", "", fmt.Errorf("no callback server running")
	}

	select {
	case result, ok := <-ch:
		if !ok || result == nil {
			return "", "", fmt.Errorf("callback server closed without result")
		}
		return result.Code, result.State, nil
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// callbackChan returns the active callback channel, or nil if no callback
// server is running.
func (p *extAuthProvider) callbackChan() <-chan *pinoauth.CallbackResult {
	p.callbackMu.Lock()
	defer p.callbackMu.Unlock()
	return p.callbackCh
}

// StopCallbackServer stops the callback server.
func (p *extAuthProvider) StopCallbackServer() {
	p.callbackMu.Lock()
	defer p.callbackMu.Unlock()
	if p.callbackSrv != nil {
		p.callbackSrv.Close()
		p.callbackSrv = nil
		p.callbackCh = nil
	}
}

// RegisterAuthProviders registers all auth providers from the extension with
// the global oauth registry. Each spec is dispatched to either the
// declarative genericAuthProvider (when spec.Flow is non-nil) or the
// imperative extAuthProvider (when the extension drives the whole login
// itself via JSON-RPC).
func (b *Bridge) RegisterAuthProviders() {
	b.authProvidersMu.Lock()
	defer b.authProvidersMu.Unlock()
	for _, spec := range b.caps.AuthProviders {
		if spec.Flow != nil {
			provider := &genericAuthProvider{spec: spec, bridge: b}
			b.genericAuthProviders = append(b.genericAuthProviders, provider)
			ai.RegisterOAuthProvider(provider)
			continue
		}
		provider := &extAuthProvider{spec: spec, bridge: b}
		b.authProviders = append(b.authProviders, provider)
		ai.RegisterOAuthProvider(provider)
	}
}

// UnregisterAuthProviders removes all auth providers registered by this extension.
// Also stops any running callback servers.
func (b *Bridge) UnregisterAuthProviders() {
	b.authProvidersMu.Lock()
	defer b.authProvidersMu.Unlock()
	for _, p := range b.authProviders {
		p.StopCallbackServer()
	}
	for _, spec := range b.caps.AuthProviders {
		ai.UnregisterOAuthProvider(spec.ID)
	}
	b.authProviders = nil
	b.genericAuthProviders = nil
}

// UnregisterAuthProvider removes a single auth provider by ID from this
// bridge's registrations and the global oauth registry.
func (b *Bridge) UnregisterAuthProvider(id string) {
	b.authProvidersMu.Lock()
	defer b.authProvidersMu.Unlock()
	ai.UnregisterOAuthProvider(id)
	kept := b.authProviders[:0]
	for _, p := range b.authProviders {
		if p.spec.ID == id {
			p.StopCallbackServer()
			continue
		}
		kept = append(kept, p)
	}
	b.authProviders = kept
	keptGeneric := b.genericAuthProviders[:0]
	for _, p := range b.genericAuthProviders {
		if p.spec.ID == id {
			continue
		}
		keptGeneric = append(keptGeneric, p)
	}
	b.genericAuthProviders = keptGeneric
}

// ReregisterAuthProvider re-registers a specific auth provider owned by this
// bridge into the global oauth registry. Used after conflict resolution to
// ensure the winning bridge's provider is the active registration.
func (b *Bridge) ReregisterAuthProvider(id string) {
	b.authProvidersMu.RLock()
	defer b.authProvidersMu.RUnlock()
	for _, p := range b.authProviders {
		if p.spec.ID == id {
			ai.RegisterOAuthProvider(p)
			return
		}
	}
	for _, p := range b.genericAuthProviders {
		if p.spec.ID == id {
			ai.RegisterOAuthProvider(p)
			return
		}
	}
}

// setAuthCallbacks stores/clears the login callbacks and ctx for UI dispatch.
func (b *Bridge) setAuthCallbacks(ctx context.Context, cb *pinoauth.LoginCallbacks) {
	b.authCallbacksMu.Lock()
	defer b.authCallbacksMu.Unlock()
	b.authCallbacks = cb
	b.authCtx = ctx
}

// getAuthCallbacks returns the current login callbacks, or nil.
func (b *Bridge) getAuthCallbacks() *pinoauth.LoginCallbacks {
	b.authCallbacksMu.RLock()
	defer b.authCallbacksMu.RUnlock()
	return b.authCallbacks
}

// getAuthCtx returns the active login ctx, or context.Background() if none.
func (b *Bridge) getAuthCtx() context.Context {
	b.authCallbacksMu.RLock()
	defer b.authCallbacksMu.RUnlock()
	if b.authCtx != nil {
		return b.authCtx
	}
	return context.Background()
}

// handleAuthHelperRPC handles auth/* helper RPCs from the extension.
// Returns (result, error, handled). If handled is false, the method was not an auth RPC.
func (b *Bridge) handleAuthHelperRPC(method string, params *json.RawMessage) (any, *Error, bool) {
	switch method {
	case "auth/generate_pkce":
		pkce := pinoauth.GeneratePKCE()
		return map[string]any{
			"verifier":  pkce.Verifier,
			"challenge": pkce.Challenge,
		}, nil, true

	case "auth/start_callback_server":
		var p struct {
			Addr  string `json:"addr"`
			Path  string `json:"path"`
			State string `json:"state"`
		}
		if params != nil {
			if err := json.Unmarshal(*params, &p); err != nil {
				return nil, &Error{Code: -32602, Message: "invalid params: " + err.Error()}, true
			}
		}
		if p.Addr == "" {
			p.Addr = "127.0.0.1:0"
		}
		if p.Path == "" {
			p.Path = "/callback"
		}

		// Find the extAuthProvider for the active login.
		provider := b.findActiveAuthProvider()
		if provider == nil {
			return nil, &Error{Code: -32000, Message: "no active auth login"}, true
		}

		ctx := b.getAuthCtx()

		addr, redirectURI, err := provider.StartCallbackServer(ctx, p.Addr, p.Path, p.State)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}, true
		}
		return map[string]any{
			"addr":         addr,
			"redirect_uri": redirectURI,
		}, nil, true

	case "auth/await_callback":
		provider := b.findActiveAuthProvider()
		if provider == nil {
			return nil, &Error{Code: -32000, Message: "no active auth login"}, true
		}

		ch := provider.callbackChan()
		if ch == nil {
			return nil, &Error{Code: -32000, Message: "no callback server running"}, true
		}

		ctx := b.getAuthCtx()
		cb := b.getAuthCallbacks()

		var manualInput func() (string, error)
		var onDismiss func()
		if cb != nil {
			manualInput = cb.OnManualCodeInput
			onDismiss = cb.OnDismissManualInput
		}

		// Race the callback server against a manual paste prompt.
		// If the browser can't reach localhost the user can paste the
		// redirect URL instead of waiting forever.
		code, state, err := pinoauth.AwaitAuthCode(ctx, ch, manualInput, onDismiss)
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}, true
		}
		return map[string]any{
			"code":  code,
			"state": state,
		}, nil, true

	case "auth/stop_callback_server":
		provider := b.findActiveAuthProvider()
		if provider != nil {
			provider.StopCallbackServer()
		}
		return map[string]any{"ok": true}, nil, true

	case "auth/open_url":
		var p struct {
			URL          string `json:"url"`
			ShortURL     string `json:"short_url"`
			Instructions string `json:"instructions"`
		}
		if params != nil {
			if err := json.Unmarshal(*params, &p); err != nil {
				return nil, &Error{Code: -32602, Message: "invalid params: " + err.Error()}, true
			}
		}
		firlog.Debug("ext auth/open_url", "url", p.URL, "short_url", p.ShortURL)
		if cb := b.getAuthCallbacks(); cb != nil && cb.OnAuth != nil {
			cb.OnAuth(pinoauth.AuthInfo{URL: p.URL, ShortURL: p.ShortURL, Instructions: p.Instructions})
		}
		return map[string]any{"ok": true}, nil, true

	case "auth/progress":
		var p struct {
			Message string `json:"message"`
		}
		if params != nil {
			if err := json.Unmarshal(*params, &p); err != nil {
				return nil, &Error{Code: -32602, Message: "invalid params: " + err.Error()}, true
			}
		}
		if cb := b.getAuthCallbacks(); cb != nil && cb.OnProgress != nil {
			cb.OnProgress(p.Message)
		}
		return map[string]any{"ok": true}, nil, true

	case "auth/prompt":
		var p struct {
			Message     string `json:"message"`
			Placeholder string `json:"placeholder"`
			AllowEmpty  bool   `json:"allow_empty"`
		}
		if params != nil {
			if err := json.Unmarshal(*params, &p); err != nil {
				return nil, &Error{Code: -32602, Message: "invalid params: " + err.Error()}, true
			}
		}
		cb := b.getAuthCallbacks()
		if cb == nil || cb.OnPrompt == nil {
			return nil, &Error{Code: -32000, Message: "no prompt handler available"}, true
		}
		value, err := cb.OnPrompt(pinoauth.Prompt{
			Message:     p.Message,
			Placeholder: p.Placeholder,
			AllowEmpty:  p.AllowEmpty,
		})
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}, true
		}
		return map[string]any{"value": value}, nil, true

	default:
		return nil, nil, false
	}
}

// findActiveAuthProvider returns the first extAuthProvider for this bridge.
func (b *Bridge) findActiveAuthProvider() *extAuthProvider {
	b.authProvidersMu.RLock()
	defer b.authProvidersMu.RUnlock()
	if len(b.authProviders) > 0 {
		return b.authProviders[0]
	}
	return nil
}
