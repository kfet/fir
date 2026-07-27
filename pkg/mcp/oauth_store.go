package mcp

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
)

// Credential persistence for MCP OAuth tokens.
//
// Tokens live in the same auth.json as AI-provider credentials (flock-guarded,
// 0600, atomic rewrite) under a namespaced key, "mcp:<server>". Every listing
// helper in pkg/auth filters that prefix out so an MCP server never shows up as
// a provider account.
//
// A stored credential is *bound to the canonical server URL it was minted for*.
// .fir/mcp.json is project-local and therefore attacker-controllable: without
// the binding, a repo declaring {"github": {"url": "https://evil.example/mcp"}}
// would receive the bearer token fir holds for the real github MCP server.
// LoadCredential refuses to return a token whose recorded URL does not match
// the config's URL, which degrades to "no token" and forces a fresh login.

// Extra keys used inside auth.AuthCredential.Extra for MCP credentials.
const (
	extraResource     = "mcp_resource"
	extraClientID     = "client_id"
	extraClientSecret = "client_secret"
	extraTokenURL     = "token_endpoint"
	extraAuthURL      = "authorization_endpoint"
	extraIssuer       = "issuer"
	extraScope        = "scope"
)

// storageKey returns the auth.json key for an MCP server.
//
// The name is percent-escaped with url.PathEscape, which is injective — a
// server literally named "a%23b" cannot collide with one named "a#b". The
// escaping matters because auth's account machinery splits slot keys on "#",
// so a raw "#" in the name would make SplitSlot mis-parse the key.
func storageKey(serverName string) string {
	return auth.MCPKeyPrefix + url.PathEscape(serverName)
}

// oauthCredential is the in-memory view of a stored MCP OAuth credential.
type oauthCredential struct {
	// Resource is the canonical MCP server URL the token was minted for
	// (RFC 8707 resource indicator). Used to bind the token to its server.
	Resource string
	// Token carries the access/refresh material and expiry.
	Token *pinoauth.Token
	// ClientID / ClientSecret are the OAuth client credentials, either
	// configured or obtained via dynamic client registration.
	ClientID     string
	ClientSecret string
	// TokenURL / AuthURL / Issuer are the discovered authorization-server
	// endpoints. TokenURL is what a refresh needs; the others are recorded so
	// `fir mcp login` can report where a credential came from.
	TokenURL string
	AuthURL  string
	Issuer   string
	// Scope is the granted scope, replayed on refresh when non-empty.
	Scope string
}

// credentialStore persists MCP OAuth credentials into an auth.AuthStorage.
// A nil store is legal and behaves as a no-op cache-less store: tokens are
// held in memory only for the lifetime of the process. That keeps tests and
// embedded uses (no agent dir) working without special-casing at every site.
type credentialStore struct {
	storage *auth.AuthStorage
}

// Load returns the credential stored for serverName, or nil when absent,
// unusable, or bound to a different resource URL than wantResource.
func (c credentialStore) Load(serverName, wantResource string) *oauthCredential {
	if c.storage == nil {
		return nil
	}
	cred := c.storage.Get(storageKey(serverName))
	if cred == nil || cred.Type != auth.CredentialTypeOAuth {
		return nil
	}
	got := extraString(cred.Extra, extraResource)
	if got == "" || got != wantResource {
		// Either a pre-binding credential or a credential minted for a
		// different URL under the same server name. Both must not be used.
		return nil
	}
	tok := &pinoauth.Token{
		AccessToken:  cred.Access,
		TokenType:    "Bearer",
		RefreshToken: cred.Refresh,
		Scope:        extraString(cred.Extra, extraScope),
	}
	if cred.Expires > 0 {
		tok.ExpiresAt = time.UnixMilli(cred.Expires)
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil
	}
	return &oauthCredential{
		Resource:     got,
		Token:        tok,
		ClientID:     extraString(cred.Extra, extraClientID),
		ClientSecret: extraString(cred.Extra, extraClientSecret),
		TokenURL:     extraString(cred.Extra, extraTokenURL),
		AuthURL:      extraString(cred.Extra, extraAuthURL),
		Issuer:       extraString(cred.Extra, extraIssuer),
		Scope:        tok.Scope,
	}
}

// Save writes a credential for serverName. Refresh-token rotation makes this
// hot: every successful refresh must persist the new refresh token or the user
// is silently stranded at the next restart.
func (c credentialStore) Save(serverName string, oc *oauthCredential) error {
	if c.storage == nil {
		return nil
	}
	if oc == nil || oc.Token == nil {
		return fmt.Errorf("save MCP credential for %q: nothing to store", serverName)
	}
	cred := auth.AuthCredential{
		Type:    auth.CredentialTypeOAuth,
		Label:   serverName,
		Access:  oc.Token.AccessToken,
		Refresh: oc.Token.RefreshToken,
		Extra: map[string]any{
			extraResource: oc.Resource,
		},
	}
	if !oc.Token.ExpiresAt.IsZero() {
		cred.Expires = oc.Token.ExpiresAt.UnixMilli()
	}
	putExtra(cred.Extra, extraClientID, oc.ClientID)
	putExtra(cred.Extra, extraClientSecret, oc.ClientSecret)
	putExtra(cred.Extra, extraTokenURL, oc.TokenURL)
	putExtra(cred.Extra, extraAuthURL, oc.AuthURL)
	putExtra(cred.Extra, extraIssuer, oc.Issuer)
	putExtra(cred.Extra, extraScope, oc.Scope)
	return c.storage.Set(storageKey(serverName), cred)
}

// Delete removes any stored credential for serverName.
func (c credentialStore) Delete(serverName string) error {
	if c.storage == nil {
		return nil
	}
	if !c.storage.Has(storageKey(serverName)) {
		return nil
	}
	return c.storage.Remove(storageKey(serverName))
}

func extraString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func putExtra(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// canonicalResource normalises an MCP server URL into the RFC 8707 resource
// indicator sent on authorization and token requests: lower-cased scheme and
// host, default port dropped, query and fragment removed, and a bare root path
// collapsed to "". Servers compare this value against their own identifier, so
// the normalisation must be conservative — the path is otherwise preserved
// verbatim, including a trailing slash on a non-root path.
func canonicalResource(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse server url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("server url %q must be absolute", rawURL)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = stripDefaultPort(u.Scheme, strings.ToLower(u.Host))
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

// stripDefaultPort removes an explicit :80 / :443 that matches the scheme.
// net.SplitHostPort is used rather than a naive Cut so bracketed IPv6 hosts
// ("[::1]:443") are handled correctly.
func stripDefaultPort(scheme, host string) string {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		return host // no port present (or malformed) — leave it alone
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		if strings.Contains(h, ":") {
			return "[" + h + "]" // re-bracket a bare IPv6 literal
		}
		return h
	}
	return host
}
