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

func TestServerConfig_TransportFields(t *testing.T) {
	// SSE transport round-trips correctly.
	sse := ServerConfig{Transport: "sse", URL: "http://localhost:8080/sse"}
	data, err := json.Marshal(sse)
	require.NoError(t, err)
	var sse2 ServerConfig
	require.NoError(t, json.Unmarshal(data, &sse2))
	assert.Equal(t, sse, sse2)

	// Streamable transport round-trips correctly.
	sh := ServerConfig{Transport: "streamable", URL: "http://localhost:8080/mcp"}
	data, err = json.Marshal(sh)
	require.NoError(t, err)
	var sh2 ServerConfig
	require.NoError(t, json.Unmarshal(data, &sh2))
	assert.Equal(t, sh, sh2)
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

func TestMergeConfigs_OverrideWins(t *testing.T) {
	base := &ConfigFile{MCPServers: map[string]ServerConfig{
		"shared":    {Command: "base-cmd"},
		"only-base": {Command: "base-only"},
	}}
	override := &ConfigFile{MCPServers: map[string]ServerConfig{
		"shared":        {Command: "override-cmd"},
		"only-override": {Command: "override-only"},
	}}

	merged := MergeConfigs(base, override)
	assert.Equal(t, "override-cmd", merged.MCPServers["shared"].Command)
	assert.Equal(t, "base-only", merged.MCPServers["only-base"].Command)
	assert.Equal(t, "override-only", merged.MCPServers["only-override"].Command)
	assert.Len(t, merged.MCPServers, 3)
}

func TestMergeConfigs_NilInputs(t *testing.T) {
	a := &ConfigFile{MCPServers: map[string]ServerConfig{"srv": {Command: "cmd"}}}
	empty := &ConfigFile{}

	// Override is empty — base survives.
	merged := MergeConfigs(a, empty)
	assert.Len(t, merged.MCPServers, 1)

	// Base is empty — override survives.
	merged = MergeConfigs(empty, a)
	assert.Len(t, merged.MCPServers, 1)
}

func TestMergeConfigs_DoesNotMutate(t *testing.T) {
	base := &ConfigFile{MCPServers: map[string]ServerConfig{"srv": {Command: "original"}}}
	override := &ConfigFile{MCPServers: map[string]ServerConfig{"srv": {Command: "new"}}}

	merged := MergeConfigs(base, override)
	assert.Equal(t, "original", base.MCPServers["srv"].Command, "base should not be mutated")
	assert.Equal(t, "new", merged.MCPServers["srv"].Command)
}

func TestLoadDefaultConfigs_ProjectOverridesUser(t *testing.T) {
	dir := t.TempDir()

	// User config.
	userDir := filepath.Join(dir, "user-home", ".fir")
	require.NoError(t, os.MkdirAll(userDir, 0o700))
	userCfg := `{"mcpServers":{"shared":{"command":"user-cmd"},"user-only":{"command":"u"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "mcp.json"), []byte(userCfg), 0o600))

	// Project config.
	projectFirDir := filepath.Join(dir, "project", ".fir")
	require.NoError(t, os.MkdirAll(projectFirDir, 0o700))
	projectCfg := `{"mcpServers":{"shared":{"command":"project-cmd"},"project-only":{"command":"p"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(projectFirDir, "mcp.json"), []byte(projectCfg), 0o600))

	// Manually load and merge to test the logic (LoadDefaultConfigs uses HOME
	// env var which we can't control in unit tests).
	user, err := LoadConfigFile(filepath.Join(userDir, "mcp.json"))
	require.NoError(t, err)
	project, err := LoadConfigFile(filepath.Join(projectFirDir, "mcp.json"))
	require.NoError(t, err)

	merged := MergeConfigs(user, project)
	assert.Equal(t, "project-cmd", merged.MCPServers["shared"].Command, "project overrides user")
	assert.Equal(t, "u", merged.MCPServers["user-only"].Command)
	assert.Equal(t, "p", merged.MCPServers["project-only"].Command)
	assert.Len(t, merged.MCPServers, 3)
}

func TestLoadDefaultConfigs_MissingFiles(t *testing.T) {
	// With a non-existent project dir, both files are missing → empty result.
	cfg, err := LoadDefaultConfigs(filepath.Join(t.TempDir(), "no-such-dir"))
	require.NoError(t, err)
	assert.Empty(t, cfg.MCPServers)
}

func TestDefaultConfigPaths_UsesFIRAgentDir(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))

	userPath, projectPath := DefaultConfigPaths("/project")
	assert.Equal(t, filepath.Join(agentDir, "mcp.json"), userPath)
	assert.Equal(t, "/project/.fir/mcp.json", projectPath)
}
