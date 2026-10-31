package agentplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/mcp"
)

// Reserved subprocess environment variables (§9.1).
const (
	EnvPluginRoot = "PLUGIN_ROOT"
	EnvPluginData = "PLUGIN_DATA"
)

// MCPServers loads the plugin's mcp.json per §7.2 and maps each valid entry
// to fir's native mcp.ServerConfig. dataDir is the client-managed
// PLUGIN_DATA directory for this plugin; it is created (before any launch)
// only when a valid-so-far stdio entry needs it.
//
// An invalid mcp.json yields no servers; an invalid entry is skipped. Both
// are reported.
func (p *Plugin) MCPServers(dataDir string) (map[string]mcp.ServerConfig, []Diagnostic) {
	path := filepath.Join(p.Root, MCPFile)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil // missing fixed location is not an error (§6.2)
	}
	disable := func(format string, a ...any) (map[string]mcp.ServerConfig, []Diagnostic) {
		return nil, []Diagnostic{{path, "MCP disabled for plugin " + p.Manifest.Name + ": " + fmt.Sprintf(format, a...)}}
	}
	real, ok, err := contained(p.Root, path)
	switch {
	case err != nil:
		return disable("%v", err)
	case !ok:
		return disable("mcp.json resolves outside the plugin root")
	}
	data, err := readRegular(real)
	if err != nil {
		return disable("mcp.json: %v", err)
	}
	obj, err := decodeObject(data)
	if err != nil {
		return disable("%v", err)
	}
	for k := range obj {
		if k != "$schema" && k != "mcpServers" {
			return disable("unknown top-level field %q", k)
		}
	}
	schema, ok := decodeString(obj["$schema"])
	if !ok {
		return disable(`required field "$schema" is missing or not a string`)
	}
	ver, ok := mcpSchemas[schema]
	if !ok {
		return disable("unsupported Agent Plugins version: $schema %q is not recognised (fir supports %s)", schema, MCPSchemaID)
	}
	if ver != p.Manifest.Version {
		return disable("mcp.json targets Agent Plugins %s but plugin.json targets %s", ver, p.Manifest.Version)
	}
	var servers map[string]json.RawMessage
	if raw, ok := obj["mcpServers"]; !ok || !isJSONObject(raw) || json.Unmarshal(raw, &servers) != nil {
		return disable(`required field "mcpServers" is missing or not an object`)
	}

	dataFn := lazyDataDir(dataDir)
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make(map[string]mcp.ServerConfig, len(servers))
	var diags []Diagnostic
	for _, name := range names {
		cfg, err := p.mapServer(servers[name], dataFn)
		if err != nil {
			diags = append(diags, Diagnostic{path, fmt.Sprintf("MCP server %q skipped: %v", name, err)})
			continue
		}
		out[name] = cfg
	}
	return out, diags
}

// lazyDataDir defers creating PLUGIN_DATA until a stdio entry needs it, so
// merely reading config for remote-only plugins writes nothing to disk.
func lazyDataDir(dir string) func() (string, error) {
	var real string
	var err error
	done := false
	return func() (string, error) {
		if !done {
			real, err = ensureDataDir(dir)
			done = true
		}
		return real, err
	}
}

// ensureDataDir creates the PLUGIN_DATA directory and returns its resolved
// path. Creation happens before any subprocess is launched (§9.1).
func ensureDataDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("no PLUGIN_DATA directory configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create PLUGIN_DATA %s: %w", dir, err)
	}
	return realRoot(dir)
}

var variantFields = map[string]map[string]bool{
	"stdio":           {"type": true, "command": true, "args": true, "env": true, "cwd": true},
	"streamable-http": {"type": true, "url": true, "headers": true},
	"sse":             {"type": true, "url": true, "headers": true},
}

func (p *Plugin) mapServer(raw json.RawMessage, data func() (string, error)) (mcp.ServerConfig, error) {
	var cfg mcp.ServerConfig
	var obj map[string]json.RawMessage
	if !isJSONObject(raw) || json.Unmarshal(raw, &obj) != nil {
		return cfg, errors.New("entry must be an object")
	}
	typ, ok := decodeString(obj["type"])
	if !ok {
		return cfg, errors.New(`required field "type" is missing or not a string`)
	}
	allowed, ok := variantFields[typ]
	if !ok {
		return cfg, fmt.Errorf("unsupported transport type %q (supported: stdio, streamable-http, sse)", typ)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return cfg, fmt.Errorf("field %q is not allowed for type %q", k, typ)
		}
	}
	if typ == "stdio" {
		return p.mapStdio(obj, data)
	}
	return mapRemote(typ, obj)
}

func (p *Plugin) mapStdio(obj map[string]json.RawMessage, data func() (string, error)) (mcp.ServerConfig, error) {
	cfg := mcp.ServerConfig{Transport: "stdio"}
	command, ok := decodeString(obj["command"])
	if !ok || command == "" {
		return cfg, errors.New(`required field "command" is missing or empty`)
	}
	var args []string
	if raw, ok := obj["args"]; ok {
		if args, ok = decodeStringArray(raw); !ok {
			return cfg, errors.New(`field "args" must be an array of strings`)
		}
	}
	var env map[string]string
	if raw, ok := obj["env"]; ok {
		if env, ok = decodeStringMap(raw); !ok {
			return cfg, errors.New(`field "env" must be an object of strings`)
		}
		for k := range env {
			if sameEnvName(k, EnvPluginRoot) || sameEnvName(k, EnvPluginData) {
				return cfg, fmt.Errorf(`field "env" must not set reserved variable %q`, k)
			}
		}
	}
	cwdRaw, hasCwd := "", false
	if raw, ok := obj["cwd"]; ok {
		if cwdRaw, ok = decodeString(raw); !ok {
			return cfg, errors.New(`field "cwd" must be a string`)
		}
		hasCwd = true
	}
	// Resolve the command before creating PLUGIN_DATA so an invalid entry
	// leaves no trace.
	resolved, err := p.resolveCommand(command)
	if err != nil {
		return cfg, err
	}
	cfg.Command = resolved
	dataReal, err := data()
	if err != nil {
		return cfg, err
	}

	expand := strings.NewReplacer("${PLUGIN_ROOT}", p.Root, "${PLUGIN_DATA}", dataReal).Replace
	for _, a := range args {
		cfg.Args = append(cfg.Args, expand(a))
	}
	cfg.Env = make(map[string]string, len(env)+2)
	for k, v := range env {
		cfg.Env[k] = expand(v)
	}
	// Set after the configured env so they always win (§9.1).
	cfg.Env[EnvPluginRoot] = p.Root
	cfg.Env[EnvPluginData] = dataReal

	cfg.Cwd = p.Root
	if hasCwd {
		if cfg.Cwd, err = p.resolveCwd(cwdRaw, expand, dataReal); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// sameEnvName compares environment variable names with platform semantics.
func sameEnvName(a, b string) bool {
	return a == b || (envNamesFold && strings.EqualFold(a, b))
}

// envNamesFold is true where environment names are case-insensitive.
var envNamesFold = runtime.GOOS == "windows"

// resolveCommand applies §7.2.1: one token, either a bare executable name
// (left for the platform PATH search at launch, using fir's own PATH) or a
// "./" path resolved against and contained in the plugin root. No
// placeholder expansion is performed.
func (p *Plugin) resolveCommand(command string) (string, error) {
	if strings.HasPrefix(command, "./") {
		path := filepath.Join(p.Root, filepath.FromSlash(command[2:]))
		real, ok, err := contained(p.Root, path)
		if err != nil {
			return "", fmt.Errorf("command %q: %v", command, err)
		}
		if !ok {
			return "", fmt.Errorf("command %q resolves outside the plugin root", command)
		}
		if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("command %q is not a regular file", command)
		}
		return real, nil
	}
	if strings.ContainsAny(command, `/\`) || command == "." || command == ".." {
		return "", fmt.Errorf("command %q must be a bare executable name or a plugin-relative path starting with ./", command)
	}
	if strings.ContainsAny(command, " \t\r\n") {
		return "", fmt.Errorf("command %q must be a single executable token, not a shell command line (put arguments in args)", command)
	}
	return command, nil
}

// resolveCwd applies the cwd forms of §7.2.1 and their containment rules.
func (p *Plugin) resolveCwd(raw string, expand func(string) string, dataReal string) (string, error) {
	var base, path string
	switch {
	case strings.HasPrefix(raw, "./"):
		base, path = p.Root, filepath.Join(p.Root, filepath.FromSlash(raw[2:]))
	case raw == "${PLUGIN_ROOT}" || strings.HasPrefix(raw, "${PLUGIN_ROOT}/"):
		base, path = p.Root, filepath.FromSlash(expand(raw))
	case raw == "${PLUGIN_DATA}" || strings.HasPrefix(raw, "${PLUGIN_DATA}/"):
		base, path = dataReal, filepath.FromSlash(expand(raw))
	default:
		return "", fmt.Errorf(`cwd %q must start with "./", "${PLUGIN_ROOT}" or "${PLUGIN_DATA}"`, raw)
	}
	path = filepath.Clean(path)
	if !within(base, path) {
		return "", fmt.Errorf("cwd %q escapes its root", raw)
	}
	if base == dataReal {
		// PLUGIN_DATA is client-managed, so fir creates subdirectories in it.
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", fmt.Errorf("cwd %q: %v", raw, err)
		}
	}
	real, ok, err := contained(base, path)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %v", raw, err)
	}
	if !ok {
		return "", fmt.Errorf("cwd %q resolves outside its root", raw)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", raw)
	}
	return real, nil
}

// mapRemote maps a streamable-http or sse entry. URL and headers are used
// verbatim: no placeholder or environment expansion (§7.2.1).
func mapRemote(typ string, obj map[string]json.RawMessage) (mcp.ServerConfig, error) {
	cfg := mcp.ServerConfig{Transport: "streamable"}
	if typ == "sse" {
		cfg.Transport = "sse"
	}
	raw, ok := decodeString(obj["url"])
	if !ok || raw == "" {
		return cfg, errors.New(`required field "url" is missing or empty`)
	}
	if err := ValidateURL(raw); err != nil {
		return cfg, err
	}
	cfg.URL = raw
	if rh, ok := obj["headers"]; ok {
		h, ok := decodeStringMap(rh)
		if !ok {
			return cfg, errors.New(`field "headers" must be an object of strings`)
		}
		if err := ValidateHeaders(h); err != nil {
			return cfg, err
		}
		cfg.Headers = h
	}
	return cfg, nil
}

// ValidateURL enforces the remote endpoint rules: an absolute http(s) URL
// with a host, no user information, no fragment, and https unless the host
// is exactly "localhost" or a loopback IP literal.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q: %v", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("url %q must be an absolute http or https URL", raw)
	}
	if u.User != nil {
		return fmt.Errorf("url %q must not contain user information", raw)
	}
	if strings.Contains(raw, "#") {
		return fmt.Errorf("url %q must not contain a fragment", raw)
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return fmt.Errorf("url %q must use https (http is allowed only for localhost or a loopback IP)", raw)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateHeaders checks that each header is a valid HTTP field and that no
// name appears twice under different casing.
func ValidateHeaders(h map[string]string) error {
	seen := make(map[string]string, len(h))
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !validHeaderName(k) {
			return fmt.Errorf("header name %q is not a valid HTTP field name", k)
		}
		if !validHeaderValue(h[k]) {
			return fmt.Errorf("header %q has an invalid value", k)
		}
		lk := strings.ToLower(k)
		if prev, dup := seen[lk]; dup {
			return fmt.Errorf("header %q duplicates %q (header names are case-insensitive)", k, prev)
		}
		seen[lk] = k
	}
	return nil
}

// validHeaderName reports whether s is an RFC 9110 token.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue reports whether s is a valid RFC 9110 field value: no
// control characters other than horizontal tab, and no leading or trailing
// whitespace.
func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return strings.TrimLeft(s, " \t") == s && strings.TrimRight(s, " \t") == s
}
