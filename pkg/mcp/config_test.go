package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerConfig_JSONRoundTrip(t *testing.T) {
	cfg := ServerConfig{
		Command: "./my-server",
		Args:    []string{"--port", "5432"},
		Env:     map[string]string{"DB_URL": "postgres://localhost/db"},
		Roots:   []string{"file:///home/user/project"},
	}

	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	var cfg2 ServerConfig
	require.NoError(t, json.Unmarshal(data, &cfg2))
	assert.Equal(t, cfg, cfg2)
}

func TestServerConfig_RootsOmitEmpty(t *testing.T) {
	// Roots is omitempty — absent when nil.
	cfg := ServerConfig{Command: "/usr/bin/my-mcp-server"}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.JSONEq(t, `{"command":"/usr/bin/my-mcp-server"}`, string(data))
}

func TestServerConfig_OmitEmpty(t *testing.T) {
	// Args and Env are omitempty — absent when nil.
	cfg := ServerConfig{Command: "/usr/bin/my-mcp-server"}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.JSONEq(t, `{"command":"/usr/bin/my-mcp-server"}`, string(data))
}

func TestServerConfig_Zero(t *testing.T) {
	var cfg ServerConfig
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.JSONEq(t, `{"command":""}`, string(data))
}

func TestLoadConfigFile_Valid(t *testing.T) {
	content := `{
		"mcpServers": {
			"filesystem": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
				"env": {"NODE_PATH": "/usr/local/lib/node_modules"},
				"roots": ["file:///home/user/project"]
			},
			"database": {
				"command": "./my-db-server",
				"args": ["--port", "5432"]
			}
		}
	}`
	path := filepath.Join(t.TempDir(), "mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := LoadConfigFile(path)
	require.NoError(t, err)
	require.Len(t, cfg.MCPServers, 2)

	fs := cfg.MCPServers["filesystem"]
	assert.Equal(t, "npx", fs.Command)
	assert.Equal(t, []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"}, fs.Args)
	assert.Equal(t, map[string]string{"NODE_PATH": "/usr/local/lib/node_modules"}, fs.Env)
	assert.Equal(t, []string{"file:///home/user/project"}, fs.Roots)

	db := cfg.MCPServers["database"]
	assert.Equal(t, "./my-db-server", db.Command)
	assert.Equal(t, []string{"--port", "5432"}, db.Args)
	assert.Nil(t, db.Env)
	assert.Nil(t, db.Roots)
}

func TestLoadConfigFile_NotExist(t *testing.T) {
	cfg, err := LoadConfigFile(filepath.Join(t.TempDir(), "nonexistent.json"))
	require.NoError(t, err)
	assert.Empty(t, cfg.MCPServers)
}

func TestLoadConfigFile_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(`not json`), 0o600))
	_, err := LoadConfigFile(path)
	assert.Error(t, err)
}
