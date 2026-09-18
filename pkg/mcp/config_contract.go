package mcp

// This file is the user-facing MCP configuration contract, and nothing else.
//
// It is embedded verbatim into the binary (see contract_source.go) and served
// to the agent as a skill resource, so it is documentation as much as it is
// code: the doc comments below are what a user asking "how do I configure an
// MCP server?" ends up reading. Keep them written for that audience.
//
// Nothing may be added here that is not part of the contract — no helpers, no
// validation, no unexported identifiers. A test enforces that (see
// config_contract_test.go); put everything else in config.go.

// ConfigFile is the top-level structure of .fir/mcp.json.
type ConfigFile struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ServerConfig describes a single MCP server to connect to.
type ServerConfig struct {
	// Transport specifies the protocol used to connect to the server.
	// Valid values: "stdio" (default when empty), "sse", "streamable".
	Transport string `json:"transport,omitempty"`
	// URL is the endpoint for SSE or streamable transports.
	// Required when Transport is "sse" or "streamable".
	URL string `json:"url,omitempty"`
	// Command is the executable to launch for the "stdio" transport.
	Command string `json:"command"`
	// Args are the command-line arguments passed to Command.
	Args []string `json:"args,omitempty"`
	// Env holds extra environment variables for the launched subprocess.
	Env map[string]string `json:"env,omitempty"`
	// Roots is an optional list of file:// URIs that fir will advertise to the
	// MCP server as filesystem roots. When empty, the process working directory
	// is used as the single default root. Entries are sent verbatim, so they
	// must be file:// URIs and not bare paths.
	Roots []string `json:"roots,omitempty"`
	// Auth optionally overrides how fir authenticates to an HTTP-based MCP
	// server. It is absent by default: a bare {"url":…, "transport":"streamable"}
	// entry already performs the full MCP OAuth discovery + login dance when the
	// server answers 401. See AuthConfig.
	Auth *AuthConfig `json:"auth,omitempty"`
}

// AuthMode selects the authentication strategy for an HTTP MCP server.
type AuthMode string

const (
	// AuthModeAuto is the default (empty) mode: connect unauthenticated and,
	// on a 401, run the MCP authorization-spec discovery chain and OAuth login.
	AuthModeAuto AuthMode = ""
	// AuthModeOAuth forces the OAuth flow — fir authenticates before the first
	// request instead of waiting for a 401.
	AuthModeOAuth AuthMode = "oauth"
	// AuthModeBearer sends AuthConfig.Token as a static bearer token and never
	// attempts OAuth. Use for servers that just want a personal access token.
	AuthModeBearer AuthMode = "bearer"
	// AuthModeNone disables all authentication handling. A 401 is surfaced
	// verbatim as a connection error.
	AuthModeNone AuthMode = "none"
)

// AuthConfig holds optional per-server authentication overrides. Every field is
// an escape hatch; a spec-compliant server needs none of them.
type AuthConfig struct {
	// Mode selects the strategy. Empty means AuthModeAuto. When Token is set
	// and Mode is empty, bearer mode is inferred.
	Mode AuthMode `json:"mode,omitempty"`
	// Token is a static bearer token used when Mode resolves to
	// AuthModeBearer. A value of the form ${VAR} or $VAR is read from the
	// process environment so secrets need not be written to disk.
	Token string `json:"token,omitempty"`
	// ClientID is a pre-registered OAuth client identifier, for authorization
	// servers that do not support RFC 7591 dynamic client registration. When
	// empty (the default) fir registers itself dynamically.
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret accompanies ClientID for confidential clients. Native apps
	// (RFC 8252) normally have none; leave empty. Supports ${VAR} expansion.
	ClientSecret string `json:"client_secret,omitempty"`
	// Scopes overrides the scopes requested at the authorization endpoint.
	// When empty, fir uses the scope from the 401 challenge, then the
	// protected-resource metadata's scopes_supported, and otherwise omits the
	// scope parameter entirely.
	Scopes []string `json:"scopes,omitempty"`
	// AuthorizationServers forces the OAuth issuer(s) to use, for servers
	// whose RFC 9728 protected-resource metadata is absent, wrong or
	// unreachable. When non-empty it *replaces* the candidate list derived
	// from that metadata — it is not merged with it — and the entries are
	// tried in order, the first one publishing usable RFC 8414 / OpenID
	// Connect metadata winning. Each entry must be an https URL (http is
	// allowed only for loopback) with no query or fragment.
	//
	// Only meaningful when the resolved mode runs the OAuth chain, i.e.
	// AuthModeAuto or AuthModeOAuth.
	AuthorizationServers []string `json:"authorization_servers,omitempty"`
	// RedirectURI pins the OAuth loopback redirect URI, for authorization
	// servers that require an exactly pre-registered value and do not honour
	// the loopback port variance of RFC 8252 §7.3 (Okta, notably). When empty
	// (the default) fir binds an ephemeral loopback port and derives the
	// redirect URI from it.
	//
	// The value is used verbatim, so it must match what the authorization
	// server has registered character for character — including "localhost"
	// versus "127.0.0.1" and the exact path. fir binds the host and port from
	// this URI, so the port must be free when you log in. Deployments that
	// need this normally also issue a pre-registered ClientID; set both.
	RedirectURI string `json:"redirect_uri,omitempty"`
	// AllowPrivateNetwork permits the OAuth discovery and token requests to
	// reach a private or otherwise non-public address. By default the MCP SDK
	// installs a hardened transport that refuses to connect to private,
	// loopback, link-local or unique-local addresses, so an authorization
	// server hosted on an internal network is unreachable.
	//
	// This relaxes only *where* the OAuth legs may connect. It does not relax
	// any transport requirement: plain http to a non-loopback host is still
	// refused, so a bearer token is never sent in cleartext. Nor does it make
	// a private IP *literal* usable — the SDK rejects those at the URL level
	// with no opt-out, so "https://192.168.1.10/" stays blocked. The case this
	// enables is a private DNS name with a real certificate, e.g.
	// "https://mcp.corp.internal/".
	//
	// Only meaningful when the resolved mode runs the OAuth chain, i.e.
	// AuthModeAuto or AuthModeOAuth; it is rejected in bearer and none mode.
	AllowPrivateNetwork bool `json:"allow_private_network,omitempty"`
}
