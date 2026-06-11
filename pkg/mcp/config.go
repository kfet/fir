// Package mcp provides integration with external MCP (Model Context Protocol)
// servers, converting their tools into agent.AgentTool values.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
