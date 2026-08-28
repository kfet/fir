// Package mcp provides integration with external MCP (Model Context Protocol)
// servers, converting their tools into agent.AgentTool values.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ServerConfig describes a single MCP server to connect to.
type ServerConfig struct {
	// Transport specifies the protocol used to connect to the server.
	// Valid values: "stdio" (default when empty), "sse", "streamable".
	Transport string `json:"transport,omitempty"`
	// URL is the endpoint for SSE or streamable transports.
	// Required when Transport is "sse" or "streamable".
	URL     string            `json:"url,omitempty"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Roots is an optional list of file:// URIs that fir will advertise to the
	// MCP server as filesystem roots. When empty, the process working directory
	// is used as the single default root.
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
}

// ResolveMode returns the effective AuthMode for a config, applying the
// "Token set implies bearer" inference. A nil config resolves to AuthModeAuto.
func (a *AuthConfig) ResolveMode() AuthMode {
	if a == nil {
		return AuthModeAuto
	}
	if a.Mode == AuthModeAuto && a.Token != "" {
		return AuthModeBearer
	}
	return a.Mode
}

// Validate reports whether the config is internally consistent.
func (a *AuthConfig) Validate() error {
	if a == nil {
		return nil
	}
	mode := a.ResolveMode()
	switch mode {
	case AuthModeAuto, AuthModeOAuth, AuthModeNone:
	case AuthModeBearer:
		if expandEnvRef(a.Token) == "" {
			return fmt.Errorf("auth.token is required (and must resolve to a non-empty value) for mode %q", AuthModeBearer)
		}
	default:
		return fmt.Errorf("unsupported auth.mode %q; valid values: oauth, bearer, none", a.Mode)
	}
	if len(a.AuthorizationServers) > 0 {
		// Nothing in bearer or none mode ever runs the OAuth chain, so a
		// forced issuer list there is silently dead config. Say so instead.
		// Note this fires for {"token": …, "authorization_servers": […]},
		// which infers bearer mode — that ambiguity deserves a loud error.
		if mode == AuthModeBearer || mode == AuthModeNone {
			return fmt.Errorf("auth.authorization_servers is not used in mode %q; remove it or use mode %q", mode, AuthModeOAuth)
		}
		for _, issuer := range a.AuthorizationServers {
			if err := validateIssuerURL(issuer); err != nil {
				return fmt.Errorf("auth.authorization_servers: %w", err)
			}
		}
	}
	return nil
}

// validateIssuerURL checks one forced authorization-server issuer. The URL
// rules mirror requireSecureTransport (https, or http on loopback for local
// dev) plus RFC 8414 §2, which forbids query and fragment components in an
// issuer identifier — a query string would also silently break the well-known
// path derivation in asCandidates.
func validateIssuerURL(issuer string) error {
	if issuer == "" {
		return errors.New("entries must not be empty")
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("parse %q: %w", issuer, err)
	}
	if u.Scheme == "" || u.Hostname() == "" {
		return fmt.Errorf("%q must be an absolute URL with a host", issuer)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q must not carry a query or fragment (RFC 8414 §2)", issuer)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("%q must use https (http is allowed only for loopback)", issuer)
}

// BearerToken returns the resolved static bearer token, expanding a ${VAR} or
// $VAR reference against the process environment.
func (a *AuthConfig) BearerToken() string {
	if a == nil {
		return ""
	}
	return expandEnvRef(a.Token)
}

// expandEnvRef resolves a value that is entirely an environment-variable
// reference ("${VAR}" or "$VAR") against the process environment. Any other
// value — including one that merely contains a "$" — is returned unchanged, so
// a literal token with a dollar sign in it is never mangled.
func expandEnvRef(v string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return os.Getenv(v[2 : len(v)-1])
	}
	if strings.HasPrefix(v, "$") && len(v) > 1 && isEnvName(v[1:]) {
		return os.Getenv(v[1:])
	}
	return v
}

// isEnvName reports whether s is a plausible environment-variable name
// (letters, digits, underscore; not starting with a digit).
func isEnvName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return s != ""
}

// ConfigFile is the top-level structure of .fir/mcp.json.
type ConfigFile struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// LoadConfigFile reads and parses a .fir/mcp.json file.
// Returns an empty ConfigFile (not an error) when the file does not exist.
func LoadConfigFile(path string) (*ConfigFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ConfigFile{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg ConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// MergeConfigs merges two ConfigFiles. Entries in override take precedence
// over entries in base when the server name appears in both. Neither argument
// is modified; a new ConfigFile is returned.
func MergeConfigs(base, override *ConfigFile) *ConfigFile {
	merged := &ConfigFile{
		MCPServers: make(map[string]ServerConfig),
	}
	for k, v := range base.MCPServers {
		merged.MCPServers[k] = v
	}
	for k, v := range override.MCPServers {
		merged.MCPServers[k] = v
	}
	return merged
}

// Collision records when a server name is shadowed during config merging.
type Collision struct {
	Server        string   `json:"server"`
	WonFile       string   `json:"won_file"`
	ShadowedFiles []string `json:"shadowed_files"`
}

// loadConfigDir loads all *.json files from a directory, merging them in
// lexical filename order (later files override earlier ones). Returns the
// merged config, any collisions detected, a map from server name to the file
// that provides it, and an error. A missing directory returns an empty config
// with no error (mirrors LoadConfigFile behaviour).
func loadConfigDir(dir string) (*ConfigFile, []Collision, map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ConfigFile{}, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	// Collect and sort JSON filenames lexically.
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// Track which file provided each server name for collision detection.
	serverSource := make(map[string]string) // server name -> file path
	var collisions []Collision
	merged := &ConfigFile{MCPServers: make(map[string]ServerConfig)}

	for _, name := range names {
		path := filepath.Join(dir, name)
		cfg, loadErr := LoadConfigFile(path)
		if loadErr != nil {
			return nil, nil, nil, loadErr
		}
		for serverName, serverCfg := range cfg.MCPServers {
			if prevFile, exists := serverSource[serverName]; exists {
				// Find or create collision entry for this server.
				found := false
				for i := range collisions {
					if collisions[i].Server == serverName {
						collisions[i].ShadowedFiles = append(collisions[i].ShadowedFiles, prevFile)
						collisions[i].WonFile = path
						found = true
						break
					}
				}
				if !found {
					collisions = append(collisions, Collision{
						Server:        serverName,
						WonFile:       path,
						ShadowedFiles: []string{prevFile},
					})
				}
			}
			merged.MCPServers[serverName] = serverCfg
			serverSource[serverName] = path
		}
	}
	return merged, collisions, serverSource, nil
}

// DefaultConfigPaths returns the canonical config file paths that LoadDefaultConfigs reads:
//   - userPath: ~/.config/fir/mcp.json  (lower precedence)
//   - projectPath: <projectDir>/.fir/mcp.json  (higher precedence)
func DefaultConfigPaths(projectDir string) (userPath, projectPath string) {
	userPath = filepath.Join(defaultConfigDir(), "mcp.json")
	if projectDir != "" {
		projectPath = projectDir + "/.fir/mcp.json"
	}
	return
}

// defaultConfigDir returns the global fir config directory (~/.config/fir),
// respecting $FIR_AGENT_DIR first and then $XDG_CONFIG_HOME. Must stay in sync
// with session.DefaultAgentDir plus the FIR_AGENT_DIR override; duplicated here
// to avoid circular imports.
func defaultConfigDir() string {
	if dir := os.Getenv("FIR_AGENT_DIR"); dir != "" {
		return dir
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "fir")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fir")
}

// LoadDefaultConfigs loads the user-level (~/.config/fir/mcp.json) and project-level
// (<projectDir>/.fir/mcp.json) config files, merging them so that the project
// config takes precedence over the user config.
//
// Missing files are silently skipped. Returns an empty ConfigFile when neither
// file exists.
func LoadDefaultConfigs(projectDir string) (*ConfigFile, error) {
	cfg, _, err := LoadDefaultConfigsReport(projectDir)
	return cfg, err
}

// LoadDefaultConfigsReport is like LoadDefaultConfigs but also returns any
// collisions detected during merging. The collision list records when a server
// name from a later config file shadows one from an earlier file. Precedence
// (low → high):
//  1. ~/.config/fir/mcp.json           (user base)
//  2. ~/.config/fir/mcp.d/*.json       (user drop-ins, lexically sorted)
//  3. <projectDir>/.fir/mcp.json       (project base)
func LoadDefaultConfigsReport(projectDir string) (*ConfigFile, []Collision, error) {
	configDir := defaultConfigDir()
	userPath, projectPath := DefaultConfigPaths(projectDir)

	// Track sources for collision detection across merge steps.
	serverSource := make(map[string]string) // server name -> file path
	collisionMap := make(map[string]*Collision)
	merged := &ConfigFile{MCPServers: make(map[string]ServerConfig)}

	// Helper to add or extend a collision.
	addCollision := func(server, wonFile, shadowedFile string) {
		if c, exists := collisionMap[server]; exists {
			c.ShadowedFiles = append(c.ShadowedFiles, shadowedFile)
			c.WonFile = wonFile
		} else {
			collisionMap[server] = &Collision{
				Server:        server,
				WonFile:       wonFile,
				ShadowedFiles: []string{shadowedFile},
			}
		}
	}

	// 1. Load user base config.
	if userPath != "" {
		userCfg, err := LoadConfigFile(userPath)
		if err != nil {
			return nil, nil, fmt.Errorf("user MCP config: %w", err)
		}
		for name, cfg := range userCfg.MCPServers {
			merged.MCPServers[name] = cfg
			serverSource[name] = userPath
		}
	}

	// 2. Load user drop-ins (mcp.d/*.json).
	mcpDDir := filepath.Join(configDir, "mcp.d")
	dirCfg, dirCollisions, dirSources, err := loadConfigDir(mcpDDir)
	if err != nil {
		return nil, nil, fmt.Errorf("user MCP drop-ins: %w", err)
	}

	// Merge drop-ins into our tracking, detecting collisions with base config.
	for name, cfg := range dirCfg.MCPServers {
		if prevFile, exists := serverSource[name]; exists {
			// Drop-in shadows the base config.
			addCollision(name, dirSources[name], prevFile)
		}
		merged.MCPServers[name] = cfg
		serverSource[name] = dirSources[name]
	}

	// Merge dir-internal collisions into our collision map.
	for _, c := range dirCollisions {
		if existing, exists := collisionMap[c.Server]; exists {
			// Extend with additional shadowed files from the dir.
			existing.ShadowedFiles = append(existing.ShadowedFiles, c.ShadowedFiles...)
			existing.WonFile = c.WonFile
		} else {
			collisionMap[c.Server] = &Collision{
				Server:        c.Server,
				WonFile:       c.WonFile,
				ShadowedFiles: append([]string(nil), c.ShadowedFiles...),
			}
		}
	}

	// 3. Load project config (highest precedence).
	// Project shadowing user configs is expected, not reported as collision.
	if projectPath != "" {
		projCfg, err := LoadConfigFile(projectPath)
		if err != nil {
			return nil, nil, fmt.Errorf("project MCP config: %w", err)
		}
		for name, cfg := range projCfg.MCPServers {
			merged.MCPServers[name] = cfg
		}
	}

	// Convert collision map to slice.
	var collisions []Collision
	for _, c := range collisionMap {
		collisions = append(collisions, *c)
	}

	return merged, collisions, nil
}
