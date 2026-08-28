package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Test harness: a fake OAuth authorization server and a fake OAuth-protected
// MCP server, both on loopback (which oauthex permits over plain HTTP).
//
// The pair is deliberately strict — it rejects a missing PKCE verifier, a
// missing RFC 8707 resource parameter, a mismatched redirect URI and a revoked
// refresh token — so a test that passes here exercises the real protocol
// rather than a permissive stub.

// asOptions tunes fake authorization-server behaviour per test.
type asOptions struct {
	// noDCR omits registration_endpoint from the metadata, forcing the
	// pre-registered-client_id path.
	noDCR bool
	// preRegistered, when non-empty, is the only client_id accepted at the
	// authorization and token endpoints.
	preRegistered string
	// accessTokenTTL is the expires_in value handed out. Zero means 3600.
	accessTokenTTL int
	// rotateRefresh issues a new refresh token on every refresh.
	rotateRefresh bool
}

// fakeAuthServer implements just enough of RFC 8414 / 7591 / 6749 / 7636 /
// 8707 for the discovery, registration, authorization and token flows.
type fakeAuthServer struct {
	t    *testing.T
	srv  *httptest.Server
	opts asOptions

	mu sync.Mutex
	// clients maps issued client_id → registered redirect URI.
	clients map[string]string
	// codes maps an issued authorization code → its PKCE challenge and
	// redirect URI.
	codes map[string]pendingCode
	// access maps a live access token → the refresh token that minted it.
	access map[string]string
	// refresh holds live refresh tokens.
	refresh map[string]bool

	// Counters, read by tests to assert on protocol behaviour.
	registrations int
	refreshes     int
	issued        int
	// lastResource records the resource parameter of the last token request.
	lastResource string
}

type pendingCode struct {
	challenge   string
	redirectURI string
	clientID    string
}

func newFakeAuthServer(t *testing.T, opts asOptions) *fakeAuthServer {
	t.Helper()
	as := &fakeAuthServer{
		t:       t,
		opts:    opts,
		clients: map[string]string{},
		codes:   map[string]pendingCode{},
		access:  map[string]string{},
		refresh: map[string]bool{},
	}
	if opts.preRegistered != "" {
		// A pre-registered client accepts any loopback redirect URI, as real
		// authorization servers do for native apps (RFC 8252 §7.3).
		as.clients[opts.preRegistered] = ""
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", as.handleMetadata)
	mux.HandleFunc("/register", as.handleRegister)
	mux.HandleFunc("/authorize", as.handleAuthorize)
	mux.HandleFunc("/token", as.handleToken)
	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (as *fakeAuthServer) URL() string { return as.srv.URL }

func (as *fakeAuthServer) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	meta := map[string]any{
		"issuer":                                as.srv.URL,
		"authorization_endpoint":                as.srv.URL + "/authorize",
		"token_endpoint":                        as.srv.URL + "/token",
		"jwks_uri":                              as.srv.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	}
	if !as.opts.noDCR {
		meta["registration_endpoint"] = as.srv.URL + "/register"
	}
	writeJSON(w, http.StatusOK, meta)
}

func (as *fakeAuthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		Scope                   string   `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	if len(body.RedirectURIs) != 1 || !strings.HasPrefix(body.RedirectURIs[0], "http://127.0.0.1:") {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":             "invalid_redirect_uri",
			"error_description": fmt.Sprintf("want one loopback redirect URI, got %v", body.RedirectURIs),
		})
		return
	}
	as.mu.Lock()
	as.registrations++
	clientID := fmt.Sprintf("dcr-client-%d", as.registrations)
	as.clients[clientID] = body.RedirectURIs[0]
	as.mu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"redirect_uris":              body.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                body.GrantTypes,
		"scope":                      body.Scope,
	})
}

func (as *fakeAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fail := func(msg string) {
		http.Error(w, msg, http.StatusBadRequest)
	}
	if q.Get("response_type") != "code" {
		fail("response_type must be code")
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		fail("PKCE S256 is required")
		return
	}
	if q.Get("resource") == "" {
		fail("RFC 8707 resource parameter is required")
		return
	}
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	as.mu.Lock()
	registered, known := as.clients[clientID]
	if known && registered != "" && registered != redirectURI {
		as.mu.Unlock()
		fail("redirect_uri does not match the registered value")
		return
	}
	if !known {
		as.mu.Unlock()
		fail("unknown client_id " + clientID)
		return
	}
	code := fmt.Sprintf("code-%d", len(as.codes)+1)
	as.codes[code] = pendingCode{challenge: q.Get("code_challenge"), redirectURI: redirectURI, clientID: clientID}
	as.mu.Unlock()

	dest, err := url.Parse(redirectURI)
	if err != nil {
		fail("bad redirect_uri")
		return
	}
	rq := dest.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	dest.RawQuery = rq.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func (as *fakeAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	as.mu.Lock()
	as.lastResource = r.Form.Get("resource")
	as.mu.Unlock()
	if r.Form.Get("resource") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_target", "error_description": "resource parameter is required",
		})
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		as.handleCodeGrant(w, r)
	case "refresh_token":
		as.handleRefreshGrant(w, r)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (as *fakeAuthServer) handleCodeGrant(w http.ResponseWriter, r *http.Request) {
	as.mu.Lock()
	pc, ok := as.codes[r.Form.Get("code")]
	delete(as.codes, r.Form.Get("code"))
	as.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if pkceChallengeOf(r.Form.Get("code_verifier")) != pc.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_grant", "error_description": "PKCE verification failed",
		})
		return
	}
	if r.Form.Get("redirect_uri") != pc.redirectURI {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_grant", "error_description": "redirect_uri mismatch",
		})
		return
	}
	as.issueTokens(w)
}

func (as *fakeAuthServer) handleRefreshGrant(w http.ResponseWriter, r *http.Request) {
	as.mu.Lock()
	as.refreshes++
	live := as.refresh[r.Form.Get("refresh_token")]
	if live && as.opts.rotateRefresh {
		delete(as.refresh, r.Form.Get("refresh_token"))
	}
	as.mu.Unlock()
	if !live {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_grant", "error_description": "refresh token revoked or unknown",
		})
		return
	}
	if as.opts.rotateRefresh {
		as.issueTokens(w)
		return
	}
	as.issueReusingRefresh(w, r.Form.Get("refresh_token"))
}

// issueTokens mints a fresh access/refresh pair.
func (as *fakeAuthServer) issueTokens(w http.ResponseWriter) {
	as.mu.Lock()
	as.issued++
	accessTok := fmt.Sprintf("access-%d", as.issued)
	refreshTok := fmt.Sprintf("refresh-%d", as.issued)
	as.access[accessTok] = refreshTok
	as.refresh[refreshTok] = true
	as.mu.Unlock()
	as.writeTokenResponse(w, accessTok, refreshTok)
}

// issueReusingRefresh mints a fresh access token but keeps the refresh token,
// modelling a non-rotating authorization server.
func (as *fakeAuthServer) issueReusingRefresh(w http.ResponseWriter, refreshTok string) {
	as.mu.Lock()
	as.issued++
	accessTok := fmt.Sprintf("access-%d", as.issued)
	as.access[accessTok] = refreshTok
	as.mu.Unlock()
	as.writeTokenResponse(w, accessTok, refreshTok)
}

func (as *fakeAuthServer) writeTokenResponse(w http.ResponseWriter, accessTok, refreshTok string) {
	ttl := as.opts.accessTokenTTL
	if ttl == 0 {
		ttl = 3600
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessTok,
		"token_type":    "Bearer",
		"refresh_token": refreshTok,
		"expires_in":    ttl,
	})
}

// revokeAll invalidates every issued access and refresh token, modelling a
// server-side revocation.
func (as *fakeAuthServer) revokeAll() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.access = map[string]string{}
	as.refresh = map[string]bool{}
}

// revokeAccessTokens invalidates access tokens but leaves refresh tokens live,
// modelling ordinary access-token expiry seen from the resource server.
func (as *fakeAuthServer) revokeAccessTokens() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.access = map[string]string{}
}

// refreshLive reports whether a refresh token is still accepted.
func (as *fakeAuthServer) refreshLive(tok string) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.refresh[tok]
}

// resourceParam returns the resource indicator of the last token request.
func (as *fakeAuthServer) resourceParam() string {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.lastResource
}

func (as *fakeAuthServer) tokenValid(tok string) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	_, ok := as.access[tok]
	return ok
}

func (as *fakeAuthServer) counters() (registrations, refreshes, issued int) {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.registrations, as.refreshes, as.issued
}

// mcpOptions tunes the fake protected MCP server.
type mcpOptions struct {
	// noChallenge omits the WWW-Authenticate header from 401 responses,
	// forcing discovery to fall back to well-known path derivation.
	noChallenge bool
	// noPRM omits the RFC 9728 protected-resource metadata document, forcing
	// the issuer to be derived from the resource origin.
	noPRM bool
	// staticToken, when non-empty, is the only token the server accepts (used
	// for the bearer-mode tests); OAuth tokens are rejected.
	staticToken string
	// scopes is advertised as scopes_supported in the PRM document.
	scopes []string
	// prmOnlyAtChallengePath serves the RFC 9728 document at a non-standard
	// path advertised solely via the WWW-Authenticate challenge. Well-known
	// path derivation then cannot find it, so a successful login proves the
	// challenge header was actually used.
	prmOnlyAtChallengePath bool
	// open serves the MCP endpoint without any authentication.
	open bool
	// forbid answers every request with 403 rather than 401, modelling an
	// authenticated-but-insufficiently-authorized caller.
	forbid bool
	// bogusAuthServers, when non-empty, is advertised as the PRM's
	// authorization_servers instead of the real fake AS, modelling a server
	// whose metadata names an issuer that does not work.
	bogusAuthServers []string
}

// fakeMCPServer is an MCP server behind a bearer-token gate, with RFC 9728
// metadata pointing at a fake authorization server.
type fakeMCPServer struct {
	srv  *httptest.Server
	as   *fakeAuthServer
	opts mcpOptions

	mu           sync.Mutex
	unauthorized int
	authorized   int
	// redirect, when non-empty, makes every request answer 302 to this URL.
	redirect string
	// prmPath is where the RFC 9728 document is served.
	prmPath string
}

// newFakeMCPServer starts an MCP server exposing a single "ping" tool behind
// the auth gate described by opts.
func newFakeMCPServer(t *testing.T, as *fakeAuthServer, opts mcpOptions) *fakeMCPServer {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "ping", Description: "returns pong"},
		func(ctx context.Context, req *sdk.CallToolRequest, args struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: "pong"}},
			}, nil, nil
		})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)

	fms := &fakeMCPServer{as: as, opts: opts}
	mux := http.NewServeMux()
	prmPath := "/.well-known/oauth-protected-resource/mcp"
	if opts.prmOnlyAtChallengePath {
		prmPath = "/deep/custom/resource-metadata"
	}
	fms.prmPath = prmPath
	mux.HandleFunc(prmPath, func(w http.ResponseWriter, _ *http.Request) {
		if opts.noPRM {
			http.NotFound(w, &http.Request{})
			return
		}
		body := map[string]any{"resource": fms.resource()}
		if len(opts.bogusAuthServers) > 0 {
			body["authorization_servers"] = opts.bogusAuthServers
		} else if as != nil {
			body["authorization_servers"] = []string{as.URL()}
		}
		if len(opts.scopes) > 0 {
			body["scopes_supported"] = opts.scopes
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.Handle("/mcp", fms.guard(handler))
	fms.srv = httptest.NewServer(mux)
	t.Cleanup(fms.srv.Close)
	return fms
}

// resource is the canonical MCP endpoint URL.
func (f *fakeMCPServer) resource() string { return f.srv.URL + "/mcp" }

// guard wraps the MCP handler with the bearer-token check.
func (f *fakeMCPServer) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		redirect := f.redirect
		f.mu.Unlock()
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusFound)
			return
		}
		if f.opts.forbid {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if f.opts.open {
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ok := false
		switch {
		case tok == "":
		case f.opts.staticToken != "":
			ok = tok == f.opts.staticToken
		case f.as != nil:
			ok = f.as.tokenValid(tok)
		}
		if !ok {
			f.mu.Lock()
			f.unauthorized++
			f.mu.Unlock()
			if !f.opts.noChallenge {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					`Bearer realm="mcp", resource_metadata=%q`, f.srv.URL+f.prmPath))
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.authorized++
		f.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// redirectTo makes every subsequent request answer 302 to dest.
func (f *fakeMCPServer) redirectTo(dest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redirect = dest
}

func (f *fakeMCPServer) counters() (unauthorized, authorized int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unauthorized, f.authorized
}

// config returns a streamable ServerConfig pointing at this server.
func (f *fakeMCPServer) config(a *AuthConfig) ServerConfig {
	return ServerConfig{Transport: "streamable", URL: f.resource(), Auth: a}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// pkceChallengeOf computes the S256 challenge for a verifier (RFC 7636 §4.2).
func pkceChallengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
