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
	// Cwd is the working directory for the "stdio" subprocess. When empty
	// the subprocess inherits fir's working directory. Servers loaded from an
	// Agent Plugin default to the plugin root.
	Cwd string `json:"cwd,omitempty"`
	// Headers are fixed HTTP headers sent with every request to the "sse" or
	// "streamable" endpoint's origin. Headers fir generates itself (MCP,
	// authorization) take precedence over a configured header of the same
	// name, and configured headers are never sent to a different origin.
	Headers map[string]string `json:"headers,omitempty"`
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
	// Disabled keeps the server configured but never connects to it. fir
	// still reports it (state "disabled") so the agent knows it exists.
	Disabled bool `json:"disabled,omitempty"`
	// Files opts this server into the experimental, draft MCP files
	// extension (io.modelcontextprotocol/files): file uploads and downloads
	// that never pass through the model. Absent (the default) means off.
	// See FilesConfig.
	Files *FilesConfig `json:"files,omitempty"`
}

// FilesConfig configures the experimental MCP files extension for one
// server. The extension is used only when Enabled is true, the server
// advertises io.modelcontextprotocol/files and the session speaks protocol
// revision 2026-07-28 or later.
//
// When on, a tool argument the server marks with x-mcp-file is shown to the
// model as {"path": "<local file>"}. fir reads that file, sends it inline or
// uploads it to the target the server names, and retries the call; the model
// never sees the bytes or the upload URL. Files a tool returns as download
// targets are fetched into a private per-session directory and their local
// paths are appended to the tool result.
type FilesConfig struct {
	// Enabled turns the extension on for this server.
	Enabled bool `json:"enabled"`
	// AutoApprove sends local files to this server without asking. Without
	// it, every file is approved through the front end's permission prompt
	// (ACP session/request_permission); a session with no prompt — headless
	// runs and the terminal UI today — refuses to send files.
	AutoApprove bool `json:"autoApprove,omitempty"`
	// AllowedOrigins, when non-empty, restricts transfers to targets whose
	// origin (scheme://host:port) is listed, besides paths under the
	// server's own address. It is also the only way to reach a target on a
	// private or loopback address, or over plain http (loopback only).
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`
	// MaxDownloadBytes caps each downloaded file. 0 means 100 MiB.
	MaxDownloadBytes int64 `json:"maxDownloadBytes,omitempty"`
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
	//
	// A public https URI is also accepted when CallbackListen is set: a
	// reverse proxy (e.g. the agentbox gateway) serves that URI and forwards
	// the request, path unchanged, to the loopback listener. When RedirectURI
	// is empty and FIR_MCP_OAUTH_REDIRECT_BASE is set, fir derives
	// "<base>/<port>/callback" for a free loopback port it listens on —
	// only with dynamic registration, i.e. when ClientID is empty too.
	RedirectURI string `json:"redirect_uri,omitempty"`
	// CallbackListen is the loopback host:port the callback server binds when
	// RedirectURI is a public https URI (e.g. "127.0.0.1:47123"). The callback
	// route is RedirectURI's path, so the proxy must forward it unchanged.
	// Required with an https RedirectURI and rejected otherwise.
	CallbackListen string `json:"callback_listen,omitempty"`
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
