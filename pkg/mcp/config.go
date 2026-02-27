// Package mcp provides integration with external MCP (Model Context Protocol)
// servers, converting their tools into agent.AgentTool values.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
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

// DefaultConfigPaths returns the canonical config file paths that LoadDefaultConfigs reads:
//   - userPath: ~/.fir/mcp.json  (lower precedence)
//   - projectPath: <projectDir>/.fir/mcp.json  (higher precedence)
func DefaultConfigPaths(projectDir string) (userPath, projectPath string) {
	home, _ := os.UserHomeDir()
	if home != "" {
		userPath = home + "/.fir/mcp.json"
	}
	if projectDir != "" {
		projectPath = projectDir + "/.fir/mcp.json"
	}
	return
}

// LoadDefaultConfigs loads the user-level (~/.fir/mcp.json) and project-level
// (<projectDir>/.fir/mcp.json) config files, merging them so that the project
// config takes precedence over the user config.
//
// Missing files are silently skipped. Returns an empty ConfigFile when neither
// file exists.
func LoadDefaultConfigs(projectDir string) (*ConfigFile, error) {
	userPath, projectPath := DefaultConfigPaths(projectDir)

	var user, project *ConfigFile
	var err error

	if userPath != "" {
		if user, err = LoadConfigFile(userPath); err != nil {
			return nil, fmt.Errorf("user MCP config: %w", err)
		}
	} else {
		user = &ConfigFile{}
	}

	if projectPath != "" {
		if project, err = LoadConfigFile(projectPath); err != nil {
			return nil, fmt.Errorf("project MCP config: %w", err)
		}
	} else {
		project = &ConfigFile{}
	}

	return MergeConfigs(user, project), nil
}
