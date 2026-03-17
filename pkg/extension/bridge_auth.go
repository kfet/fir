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
	"github.com/kfet/fir/pkg/ai/oauth"
)

// extAuthProvider adapts an extension-provided auth provider to the
// oauth.Provider interface. It sends JSON-RPC requests to the extension
// process and handles bidirectional communication during login.
type extAuthProvider struct {
	spec   AuthProviderSpec
	bridge *Bridge

	// callbackMu protects the active callback server state.
	callbackMu  sync.Mutex
	callbackSrv *http.Server
	callbackCh  <-chan *oauth.CallbackResult
}

func (p *extAuthProvider) ID() string               { return p.spec.ID }
func (p *extAuthProvider) Name() string              { return p.spec.Name }
func (p *extAuthProvider) UsesCallbackServer() bool  { return p.spec.UsesCallbackServer }

func (p *extAuthProvider) Login(callbacks oauth.LoginCallbacks) (*oauth.Credentials, error) {
	// Store callbacks so the bridge can dispatch UI requests from the extension.
	p.bridge.setAuthCallbacks(&callbacks)
	defer p.bridge.setAuthCallbacks(nil)

	params := map[string]any{
		"provider_id": p.spec.ID,
	}
	// Use a long timeout for login (5 minutes) since it's interactive.
	raw, err := p.bridge.CallHook("auth/login", params, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("auth/login: %w", err)
	}

	var result struct {
		Credentials *oauth.Credentials `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("auth/login: invalid response: %w", err)
	}
	if result.Credentials == nil {
		return nil, fmt.Errorf("auth/login: no credentials returned")
	}
	return result.Credentials, nil
}

func (p *extAuthProvider) RefreshToken(creds *oauth.Credentials) (*oauth.Credentials, error) {
	params := map[string]any{
		"provider_id": p.spec.ID,
		"credentials": creds,
	}
	raw, err := p.bridge.CallHook("auth/refresh", params, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("auth/refresh: %w", err)
	}

	var result struct {
		Credentials *oauth.Credentials `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("auth/refresh: invalid response: %w", err)
	}
	if result.Credentials == nil {
		return nil, fmt.Errorf("auth/refresh: no credentials returned")
	}
	return result.Credentials, nil
}

func (p *extAuthProvider) GetAPIKey(creds *oauth.Credentials) string {
	params := map[string]any{
		"provider_id": p.spec.ID,
		"credentials": creds,
	}
	raw, err := p.bridge.CallHook("auth/api_key", params, 10*time.Second)
	if err != nil {
		// Fallback: return access token directly.
		return creds.Access
	}

	var result struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return creds.Access
	}
	return result.APIKey
}

func (p *extAuthProvider) ModifyModels(models []*ai.Model, creds *oauth.Credentials) []*ai.Model {
	params := map[string]any{
		"provider_id": p.spec.ID,
		"credentials": creds,
		"models":      models,
	}
	raw, err := p.bridge.CallHook("auth/modify_models", params, 10*time.Second)
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

// StartCallbackServer starts a local OAuth callback server for the extension.
func (p *extAuthProvider) StartCallbackServer(ctx context.Context, addr, path string) (string, string, error) {
	p.callbackMu.Lock()
	defer p.callbackMu.Unlock()

	if p.callbackSrv != nil {
		return "", "", fmt.Errorf("callback server already running")
	}

	srv, ch, resolvedAddr, err := oauth.StartOAuthCallbackServer(ctx, path, addr)
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
// the global oauth registry.
func (b *Bridge) RegisterAuthProviders() {
	b.authProvidersMu.Lock()
	defer b.authProvidersMu.Unlock()
	for _, spec := range b.caps.AuthProviders {
		provider := &extAuthProvider{
			spec:   spec,
			bridge: b,
		}
		b.authProviders = append(b.authProviders, provider)
		oauth.RegisterProvider(provider)
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
		oauth.UnregisterProvider(spec.ID)
	}
	b.authProviders = nil
}

// setAuthCallbacks stores/clears the login callbacks for UI dispatch.
func (b *Bridge) setAuthCallbacks(cb *oauth.LoginCallbacks) {
	b.authCallbacksMu.Lock()
	defer b.authCallbacksMu.Unlock()
	b.authCallbacks = cb
}

// getAuthCallbacks returns the current login callbacks, or nil.
func (b *Bridge) getAuthCallbacks() *oauth.LoginCallbacks {
	b.authCallbacksMu.RLock()
	defer b.authCallbacksMu.RUnlock()
	return b.authCallbacks
}

// handleAuthHelperRPC handles auth/* helper RPCs from the extension.
// Returns (result, error, handled). If handled is false, the method was not an auth RPC.
func (b *Bridge) handleAuthHelperRPC(method string, params *json.RawMessage) (any, *Error, bool) {
	switch method {
	case "auth/generate_pkce":
		pkce, err := oauth.GeneratePKCE()
		if err != nil {
			return nil, &Error{Code: -32000, Message: err.Error()}, true
		}
		return map[string]any{
			"verifier":  pkce.Verifier,
			"challenge": pkce.Challenge,
		}, nil, true

	case "auth/start_callback_server":
		var p struct {
			Addr string `json:"addr"`
			Path string `json:"path"`
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

		ctx := context.Background()
		if cb := b.getAuthCallbacks(); cb != nil && cb.Ctx != nil {
			ctx = cb.Ctx
		}

		addr, redirectURI, err := provider.StartCallbackServer(ctx, p.Addr, p.Path)
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

		ctx := context.Background()
		if cb := b.getAuthCallbacks(); cb != nil && cb.Ctx != nil {
			ctx = cb.Ctx
		}

		code, state, err := provider.AwaitCallback(ctx)
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
			Instructions string `json:"instructions"`
		}
		if params != nil {
			if err := json.Unmarshal(*params, &p); err != nil {
				return nil, &Error{Code: -32602, Message: "invalid params: " + err.Error()}, true
			}
		}
		if cb := b.getAuthCallbacks(); cb != nil && cb.OnAuth != nil {
			cb.OnAuth(oauth.AuthInfo{URL: p.URL, Instructions: p.Instructions})
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
		value, err := cb.OnPrompt(oauth.Prompt{
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

// findActiveAuthProvider returns the extAuthProvider with the given ID,
// or the first one if id is empty.
func (b *Bridge) findActiveAuthProvider() *extAuthProvider {
	b.authProvidersMu.RLock()
	defer b.authProvidersMu.RUnlock()
	if len(b.authProviders) > 0 {
		return b.authProviders[0]
	}
	return nil
}

// findAuthProviderByID returns the extAuthProvider with the given ID, or nil.
func (b *Bridge) findAuthProviderByID(id string) *extAuthProvider {
	b.authProvidersMu.RLock()
	defer b.authProvidersMu.RUnlock()
	for _, p := range b.authProviders {
		if p.spec.ID == id {
			return p
		}
	}
	return nil
}
