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

func TestLoadConfigDir_SortedMerge(t *testing.T) {
	dir := t.TempDir()
	// Write files out of lexical order to verify sorting.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"mcpServers":{"srv":{"command":"b-cmd"}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"mcpServers":{"srv":{"command":"a-cmd"}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.json"), []byte(`{"mcpServers":{"srv":{"command":"c-cmd"}}}`), 0o600))

	cfg, collisions, sources, err := loadConfigDir(dir)
	require.NoError(t, err)
	// c.json (last lexically) should win.
	assert.Equal(t, "c-cmd", cfg.MCPServers["srv"].Command)
	assert.Equal(t, filepath.Join(dir, "c.json"), sources["srv"])
	// One collision entry for "srv" since all three define it.
	require.Len(t, collisions, 1)
	assert.Equal(t, "srv", collisions[0].Server)
	assert.Equal(t, filepath.Join(dir, "c.json"), collisions[0].WonFile)
	assert.Contains(t, collisions[0].ShadowedFiles, filepath.Join(dir, "a.json"))
	assert.Contains(t, collisions[0].ShadowedFiles, filepath.Join(dir, "b.json"))
}

func TestLoadConfigDir_MissingDir(t *testing.T) {
	cfg, collisions, sources, err := loadConfigDir(filepath.Join(t.TempDir(), "no-such-dir"))
	require.NoError(t, err)
	assert.Empty(t, cfg.MCPServers)
	assert.Empty(t, collisions)
	assert.Empty(t, sources)
}

func TestLoadConfigDir_NoCollisions(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"mcpServers":{"srv1":{"command":"a"}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"mcpServers":{"srv2":{"command":"b"}}}`), 0o600))

	cfg, collisions, sources, err := loadConfigDir(dir)
	require.NoError(t, err)
	assert.Len(t, cfg.MCPServers, 2)
	assert.Empty(t, collisions)
	assert.Equal(t, filepath.Join(dir, "a.json"), sources["srv1"])
	assert.Equal(t, filepath.Join(dir, "b.json"), sources["srv2"])
}

func TestLoadConfigDir_IgnoresSubdirs(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(subdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "ignored.json"), []byte(`{"mcpServers":{"srv":{"command":"ignored"}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"mcpServers":{"srv":{"command":"a"}}}`), 0o600))

	cfg, _, _, err := loadConfigDir(dir)
	require.NoError(t, err)
	assert.Equal(t, "a", cfg.MCPServers["srv"].Command)
}

func TestLoadConfigDir_IgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte(`not json`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"mcpServers":{"srv":{"command":"b"}}}`), 0o600))

	cfg, collisions, _, err := loadConfigDir(dir)
	require.NoError(t, err)
	assert.Len(t, cfg.MCPServers, 1)
	assert.Empty(t, collisions)
}

func TestLoadConfigDir_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`not valid json`), 0o600))

	_, _, _, err := loadConfigDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestLoadDefaultConfigsReport_MergesUserDDropins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)

	// User base config.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"base":{"command":"base-cmd"}}}`), 0o600))

	// User drop-ins directory.
	mcpDDir := filepath.Join(dir, "mcp.d")
	require.NoError(t, os.Mkdir(mcpDDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mcpDDir, "dropin.json"), []byte(`{"mcpServers":{"dropin":{"command":"dropin-cmd"}}}`), 0o600))

	cfg, collisions, err := LoadDefaultConfigsReport("")
	require.NoError(t, err)
	assert.Equal(t, "base-cmd", cfg.MCPServers["base"].Command)
	assert.Equal(t, "dropin-cmd", cfg.MCPServers["dropin"].Command)
	assert.Empty(t, collisions)
}

func TestLoadDefaultConfigsReport_DropinShadowsBase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)

	// User base config.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"srv":{"command":"base-cmd"}}}`), 0o600))

	// User drop-in that shadows the base.
	mcpDDir := filepath.Join(dir, "mcp.d")
	require.NoError(t, os.Mkdir(mcpDDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mcpDDir, "shadow.json"), []byte(`{"mcpServers":{"srv":{"command":"dropin-cmd"}}}`), 0o600))

	cfg, collisions, err := LoadDefaultConfigsReport("")
	require.NoError(t, err)
	assert.Equal(t, "dropin-cmd", cfg.MCPServers["srv"].Command)
	require.Len(t, collisions, 1)
	assert.Equal(t, "srv", collisions[0].Server)
	assert.Equal(t, filepath.Join(mcpDDir, "shadow.json"), collisions[0].WonFile)
	assert.Contains(t, collisions[0].ShadowedFiles, filepath.Join(dir, "mcp.json"))
}

func TestLoadDefaultConfigsReport_BaseAndMultipleDropins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)

	// User base config.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"srv":{"command":"base-cmd"}}}`), 0o600))

	// Multiple drop-ins that shadow base and each other.
	mcpDDir := filepath.Join(dir, "mcp.d")
	require.NoError(t, os.Mkdir(mcpDDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mcpDDir, "a.json"), []byte(`{"mcpServers":{"srv":{"command":"a-cmd"}}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mcpDDir, "b.json"), []byte(`{"mcpServers":{"srv":{"command":"b-cmd"}}}`), 0o600))

	cfg, collisions, err := LoadDefaultConfigsReport("")
	require.NoError(t, err)
	assert.Equal(t, "b-cmd", cfg.MCPServers["srv"].Command)
	// Should have exactly ONE collision entry, not two (no duplicates).
	require.Len(t, collisions, 1, "expected single collision entry, not duplicates")
	assert.Equal(t, "srv", collisions[0].Server)
	assert.Equal(t, filepath.Join(mcpDDir, "b.json"), collisions[0].WonFile)
	// Should shadow both base and first drop-in.
	assert.Contains(t, collisions[0].ShadowedFiles, filepath.Join(dir, "mcp.json"))
	assert.Contains(t, collisions[0].ShadowedFiles, filepath.Join(mcpDDir, "a.json"))
}

func TestLoadDefaultConfigsReport_ProjectOverridesAll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)

	// User base config.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"srv":{"command":"user-cmd"}}}`), 0o600))

	// Project config.
	projectDir := filepath.Join(dir, "project")
	projectFirDir := filepath.Join(projectDir, ".fir")
	require.NoError(t, os.MkdirAll(projectFirDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectFirDir, "mcp.json"), []byte(`{"mcpServers":{"srv":{"command":"project-cmd"}}}`), 0o600))

	cfg, _, err := LoadDefaultConfigsReport(projectDir)
	require.NoError(t, err)
	// Project overrides user, no collision reported (expected behavior).
	assert.Equal(t, "project-cmd", cfg.MCPServers["srv"].Command)
}

func TestLoadDefaultConfigs_StillWorks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{"mcpServers":{"srv":{"command":"user-cmd"}}}`), 0o600))

	// Verify LoadDefaultConfigs still returns merged config without collisions.
	cfg, err := LoadDefaultConfigs("")
	require.NoError(t, err)
	assert.Equal(t, "user-cmd", cfg.MCPServers["srv"].Command)
}
