// Package mcp provides integration with external MCP (Model Context Protocol)
// servers, converting their tools into agent.AgentTool values.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// PluginLayerFunc returns the MCP servers contributed by installed Agent
// Plugins, the plugin mcp.json each one came from, and a joined error of
// non-fatal diagnostics (skipped entries, rejected plugins). agentDir is the
// global fir config directory; projectDir is the project working directory.
type PluginLayerFunc func(agentDir, projectDir string) (servers map[string]ServerConfig, sources map[string]string, err error)

var pluginLayer atomic.Pointer[PluginLayerFunc]

// PluginLayerRegistered reports whether a plugin layer provider is installed.
func PluginLayerRegistered() bool { return pluginLayer.Load() != nil }

// RegisterPluginLayer installs the provider for the plugin MCP layer, the
// lowest-precedence layer read by LoadDefaultConfigsReport. The package
// manager registers it; mcp itself knows nothing about packages.
func RegisterPluginLayer(f PluginLayerFunc) {
	if f == nil {
		pluginLayer.Store(nil)
		return
	}
	pluginLayer.Store(&f)
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
	if a.RedirectURI != "" {
		// Same reasoning as authorization_servers: the loopback callback
		// server only ever runs inside the OAuth chain, so pinning its URI in
		// bearer or none mode is dead config.
		if mode == AuthModeBearer || mode == AuthModeNone {
			return fmt.Errorf("auth.redirect_uri is not used in mode %q; remove it or use mode %q", mode, AuthModeOAuth)
		}
		if _, _, err := validateRedirectURI(a.RedirectURI, a.CallbackListen); err != nil {
			return err
		}
	} else if a.CallbackListen != "" {
		return fmt.Errorf("auth.callback_listen requires an https auth.redirect_uri for the proxy to serve")
	}
	if a.AllowPrivateNetwork && (mode == AuthModeBearer || mode == AuthModeNone) {
		// The flag only widens where the OAuth discovery and token legs may
		// connect, and neither runs in these modes. Rejecting it loudly stops
		// a user believing it is what lets their static token reach an
		// internal host — nothing on the data path consults it.
		return fmt.Errorf("auth.allow_private_network is not used in mode %q; remove it or use mode %q", mode, AuthModeOAuth)
	}
	return nil
}

// validateRedirectURI checks a pinned redirect URI and the optional
// callback_listen address, returning the parsed URI and the address the
// callback server binds. Two shapes are accepted:
//
//   - http on a loopback host (RFC 8252 §7.3), with no callback_listen: fir
//     binds the URI's own host and port. The port must be explicit and
//     non-zero — pinning a URI whose port fir would choose at random defeats
//     the purpose.
//   - https on a public host, with callback_listen naming a loopback
//     host:port: a reverse proxy serves the public URI and forwards the
//     request, path unchanged, to that listener.
//
// The path must be non-empty because it is registered as the callback route.
// Query and fragment components are rejected: the authorization server
// compares the URI literally, and the callback route cannot carry them.
// Errors name the offending auth.* field.
func validateRedirectURI(raw, listen string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("auth.redirect_uri: parse %q: %w", raw, err)
	}
	if u.Path == "" {
		return nil, "", fmt.Errorf("auth.redirect_uri: %q must include a path (for example %q)", raw, raw+callbackRoute)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, "", fmt.Errorf("auth.redirect_uri: %q must not carry a query or fragment", raw)
	}
	if u.User != nil {
		return nil, "", fmt.Errorf("auth.redirect_uri: %q must not carry userinfo", raw)
	}
	switch u.Scheme {
	case "http":
		if listen != "" {
			return nil, "", fmt.Errorf("auth.callback_listen is only used with an https auth.redirect_uri; "+
				"with %q fir binds the URI's own host and port", raw)
		}
		if !isLoopbackHost(u.Hostname()) {
			return nil, "", fmt.Errorf("auth.redirect_uri: %q must point at a loopback host (127.0.0.1, ::1 or localhost); "+
				"for a public host use https with auth.callback_listen", raw)
		}
		if !validPort(u.Port()) {
			return nil, "", fmt.Errorf("auth.redirect_uri: %q must specify an explicit port in 1-65535", raw)
		}
		return u, u.Host, nil
	case "https":
		if u.Hostname() == "" || isLoopbackHost(u.Hostname()) {
			return nil, "", fmt.Errorf("auth.redirect_uri: %q: an https redirect URI must name a public host "+
				"(the loopback callback server is plain HTTP)", raw)
		}
		if listen == "" {
			return nil, "", fmt.Errorf("auth.redirect_uri: %q is https, so auth.callback_listen must name the "+
				"loopback host:port the proxy forwards it to (for example \"127.0.0.1:47123\")", raw)
		}
		if err := validateCallbackListen(listen); err != nil {
			return nil, "", err
		}
		return u, listen, nil
	default:
		return nil, "", fmt.Errorf("auth.redirect_uri: %q must use http on a loopback host (RFC 8252 §7.3) "+
			"or https together with auth.callback_listen", raw)
	}
}

// validateCallbackListen checks a loopback host:port with an explicit port.
func validateCallbackListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("auth.callback_listen: %q must be host:port: %w", listen, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("auth.callback_listen: %q must bind a loopback host (127.0.0.1, ::1 or localhost)", listen)
	}
	if !validPort(port) {
		return fmt.Errorf("auth.callback_listen: %q must specify an explicit port in 1-65535", listen)
	}
	return nil
}

// validPort reports whether port is a decimal in 1-65535. Atoi("") is 0, so a
// missing port fails too.
func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
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
	var errs []error

	for _, name := range names {
		path := filepath.Join(dir, name)
		cfg, loadErr := LoadConfigFile(path)
		if loadErr != nil {
			// One broken drop-in must not hide the others.
			errs = append(errs, loadErr)
			continue
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
	return merged, collisions, serverSource, errors.Join(errs...)
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
//  0. <plugin>/mcp.json                (installed Agent Plugins)
//  1. ~/.config/fir/mcp.json           (user base)
//  2. ~/.config/fir/mcp.d/*.json       (user drop-ins, lexically sorted)
//  3. <projectDir>/.fir/mcp.json       (project base)
//
// Loading is best-effort: a file that cannot be read or parsed is skipped and
// the rest still load. The returned config is never nil; err (if non-nil)
// joins one error per skipped file and callers should surface it as a warning.
func LoadDefaultConfigsReport(projectDir string) (*ConfigFile, []Collision, error) {
	cfg, collisions, _, err := loadDefaultConfigs(projectDir)
	return cfg, collisions, err
}

// ConfigSources maps each server name from the default config files to the
// file that defined its winning entry (see LoadDefaultConfigsReport).
func ConfigSources(projectDir string) map[string]string {
	_, _, src, _ := loadDefaultConfigs(projectDir)
	return src
}

func loadDefaultConfigs(projectDir string) (*ConfigFile, []Collision, map[string]string, error) {
	configDir := defaultConfigDir()
	var errs []error
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

	// 0. Installed Agent Plugins (lowest precedence). A user or project
	// entry of the same name wins; that is reported, since the plugin
	// author's server silently disappearing would be confusing.
	pluginSources := map[string]string{}
	if f := pluginLayer.Load(); f != nil {
		servers, sources, err := (*f)(configDir, projectDir)
		if err != nil {
			errs = append(errs, err)
		}
		for name, cfg := range servers {
			merged.MCPServers[name] = cfg
			serverSource[name] = sources[name]
			pluginSources[name] = sources[name]
		}
	}
	shadowPlugin := func(name, file string) {
		if src, ok := pluginSources[name]; ok && serverSource[name] == src {
			addCollision(name, file, src)
			errs = append(errs, fmt.Errorf("MCP server %q from plugin %s is overridden by %s", name, src, file))
		}
	}

	// 1. Load user base config.
	if userPath != "" {
		userCfg, err := LoadConfigFile(userPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("user MCP config: %w", err))
			userCfg = &ConfigFile{}
		}
		for name, cfg := range userCfg.MCPServers {
			shadowPlugin(name, userPath)
			merged.MCPServers[name] = cfg
			serverSource[name] = userPath
		}
	}

	// 2. Load user drop-ins (mcp.d/*.json).
	mcpDDir := filepath.Join(configDir, "mcp.d")
	dirCfg, dirCollisions, dirSources, err := loadConfigDir(mcpDDir)
	if err != nil {
		errs = append(errs, fmt.Errorf("user MCP drop-ins: %w", err))
	}
	if dirCfg == nil {
		dirCfg = &ConfigFile{}
	}

	// Merge drop-ins into our tracking, detecting collisions with base config.
	for name, cfg := range dirCfg.MCPServers {
		if _, fromPlugin := pluginSources[name]; fromPlugin && serverSource[name] == pluginSources[name] {
			shadowPlugin(name, dirSources[name])
		} else if prevFile, exists := serverSource[name]; exists {
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
			errs = append(errs, fmt.Errorf("project MCP config: %w", err))
			projCfg = &ConfigFile{}
		}
		for name, cfg := range projCfg.MCPServers {
			shadowPlugin(name, projectPath)
			merged.MCPServers[name] = cfg
			serverSource[name] = projectPath
		}
	}

	// Convert collision map to slice.
	var collisions []Collision
	for _, c := range collisionMap {
		collisions = append(collisions, *c)
	}

	return merged, collisions, serverSource, errors.Join(errs...)
}
