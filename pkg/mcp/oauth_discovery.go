package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// OAuth discovery for MCP servers, per the MCP authorization specification:
//
//	401 + WWW-Authenticate  →  RFC 9728 protected-resource metadata
//	                        →  RFC 8414 authorization-server metadata
//	                        →  RFC 7591 dynamic client registration
//
// Every network primitive here comes from the SDK's oauthex package, which
// also enforces the security invariants (HTTPS-or-loopback, issuer match,
// resource match, no dangerous URL schemes, PKCE support).

// wellKnownPRM and wellKnownAS* are the registered well-known suffixes used by
// the discovery chain.
const (
	wellKnownPRM    = "/.well-known/oauth-protected-resource"
	wellKnownAS     = "/.well-known/oauth-authorization-server"
	wellKnownOIDC   = "/.well-known/openid-configuration"
	bearerScheme    = "bearer"
	paramResourceMD = "resource_metadata"
	paramScope      = "scope"
)

// challengeInfo is what a 401's WWW-Authenticate header tells us.
type challengeInfo struct {
	// ResourceMetadata is the resource_metadata parameter (RFC 9728 §5.1),
	// an absolute URL to the protected-resource metadata document. Empty when
	// the server omitted it (or sent no challenge at all).
	ResourceMetadata string
	// Scope is the scope parameter of the challenge, if any.
	Scope string
}

// parseChallenge extracts the Bearer challenge parameters fir cares about from
// the WWW-Authenticate header values of a 401 response. A malformed header is
// not fatal: discovery falls back to the well-known path derivation, so we log
// and return the zero value.
func parseChallenge(headers []string) challengeInfo {
	var info challengeInfo
	if len(headers) == 0 {
		return info
	}
	challenges, err := oauthex.ParseWWWAuthenticate(headers)
	if err != nil {
		firlog.Debug("mcp oauth: malformed WWW-Authenticate", "err", err)
		return info
	}
	for _, ch := range challenges {
		if ch.Scheme != bearerScheme {
			continue
		}
		if v := ch.Params[paramResourceMD]; v != "" && info.ResourceMetadata == "" {
			info.ResourceMetadata = v
		}
		if v := ch.Params[paramScope]; v != "" && info.Scope == "" {
			info.Scope = v
		}
	}
	return info
}

// prmCandidates returns the protected-resource metadata URLs to try for a
// resource, in order. RFC 9728 §3.1 inserts the well-known segment between the
// authority and the resource path; a path-less resource yields the bare
// well-known URL. The root-path form is retried last so a server that
// publishes metadata at the origin (a common deviation) still works.
func prmCandidates(resource string) ([]string, error) {
	u, err := url.Parse(resource)
	if err != nil {
		return nil, fmt.Errorf("parse resource url: %w", err)
	}
	origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		return []string{origin + wellKnownPRM}, nil
	}
	return []string{origin + wellKnownPRM + path, origin + wellKnownPRM}, nil
}

// asCandidates returns the authorization-server metadata URLs to try for an
// issuer, in MCP-spec order: RFC 8414 path insertion, OIDC path insertion,
// then OIDC path append (the OpenID Connect Discovery 1.0 form).
func asCandidates(issuer string) ([]string, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("parse issuer url: %w", err)
	}
	origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		return []string{origin + wellKnownAS, origin + wellKnownOIDC}, nil
	}
	return []string{
		origin + wellKnownAS + path,
		origin + wellKnownOIDC + path,
		origin + path + wellKnownOIDC,
	}, nil
}

// errNoMetadata reports that a discovery step found nothing usable. It is
// returned rather than a bare nil so callers can distinguish "server is not
// OAuth-protected" from a transport failure.
var errNoMetadata = errors.New("no OAuth metadata found")

// discoverResourceMetadata fetches the RFC 9728 protected-resource metadata for
// resource. It prefers the resource_metadata URL advertised in the 401
// challenge and otherwise derives well-known candidates from the resource URL.
//
// Returns (nil, nil) when the server publishes no metadata at all: that is not
// an error, it just means the caller must fall back to using the resource's own
// origin as the authorization-server issuer.
func discoverResourceMetadata(ctx context.Context, hc *http.Client, resource string, ci challengeInfo) (*oauthex.ProtectedResourceMetadata, error) {
	var candidates []string
	if ci.ResourceMetadata != "" {
		candidates = append(candidates, ci.ResourceMetadata)
	}
	derived, err := prmCandidates(resource)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, derived...)

	for _, mdURL := range candidates {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, mdURL, resource, hc)
		if err == nil && prm != nil {
			firlog.Debug("mcp oauth: protected-resource metadata", "url", mdURL,
				"authorization_servers", prm.AuthorizationServers)
			return prm, nil
		}
		firlog.Debug("mcp oauth: no protected-resource metadata", "url", mdURL, "err", err)
	}
	// RFC 9728 metadata is optional; the caller falls back to the origin.
	return nil, nil
}

// discoverAuthServer resolves authorization-server metadata for the first
// issuer in issuers that publishes a usable document. Issuers are tried in
// order, and each issuer's candidate URLs in MCP-spec order.
//
// oauthex.GetAuthServerMeta returns (nil, nil) for a 4xx — a URL that simply
// does not host metadata — so a nil result means "try the next candidate", not
// success.
func discoverAuthServer(ctx context.Context, hc *http.Client, issuers []string) (*oauthex.AuthServerMeta, error) {
	if len(issuers) == 0 {
		return nil, errNoMetadata
	}
	var firstErr error
	for _, issuer := range issuers {
		candidates, err := asCandidates(issuer)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, mdURL := range candidates {
			asm, err := oauthex.GetAuthServerMeta(ctx, mdURL, issuer, hc)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", mdURL, err)
				}
				firlog.Debug("mcp oauth: authorization-server metadata failed", "url", mdURL, "err", err)
				continue
			}
			if asm == nil {
				continue // 4xx — this URL does not host metadata.
			}
			firlog.Debug("mcp oauth: authorization-server metadata", "url", mdURL,
				"authorization_endpoint", asm.AuthorizationEndpoint,
				"token_endpoint", asm.TokenEndpoint,
				"registration_endpoint", asm.RegistrationEndpoint)
			return asm, nil
		}
	}
	if firstErr != nil {
		return nil, fmt.Errorf("authorization server discovery: %w", firstErr)
	}
	return nil, errNoMetadata
}

// issuerCandidates returns the authorization-server issuers to try for a
// resource, given its (possibly nil) protected-resource metadata. Falling back
// to the resource's own origin matches the MCP spec's guidance for servers that
// publish no RFC 9728 document.
func issuerCandidates(resource string, prm *oauthex.ProtectedResourceMetadata) []string {
	if prm != nil && len(prm.AuthorizationServers) > 0 {
		return prm.AuthorizationServers
	}
	u, err := url.Parse(resource)
	if err != nil {
		return nil
	}
	return []string{(&url.URL{Scheme: u.Scheme, Host: u.Host}).String()}
}

// registerClient performs RFC 7591 dynamic client registration for fir against
// the authorization server, requesting the loopback redirect URI the pending
// login will actually listen on.
//
// Registration is re-run on every interactive login rather than cached: the
// loopback port is ephemeral (RFC 8252 §7.3), so a client_id registered against
// a previous login's redirect URI would be rejected on the next one. A stored
// client_id is only reused for refresh, which does not involve redirect_uri.
func registerClient(ctx context.Context, hc *http.Client, asm *oauthex.AuthServerMeta, redirectURI, scope string) (*oauthex.ClientRegistrationResponse, error) {
	if asm.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("authorization server %s does not support dynamic client registration (RFC 7591); "+
			"set auth.client_id in your MCP server config", asm.Issuer)
	}
	meta := &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              "fir",
		ClientURI:               "https://github.com/kfet/fir",
		ApplicationType:         "native",
		Scope:                   scope,
	}
	resp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, meta, hc)
	if err != nil {
		return nil, fmt.Errorf("dynamic client registration: %w", err)
	}
	return resp, nil
}
