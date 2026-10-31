package agentplugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Edge cases beyond the checklist: unusual filesystem states and
// malformed values, each confined to its own failure boundary.

func TestIsPlugin(t *testing.T) {
	assert.True(t, IsPlugin(writePlugin(t, map[string]string{"plugin.json": minimalManifest})))
	assert.False(t, IsPlugin(t.TempDir()))
}

func TestLoad_FilesystemStates(t *testing.T) {
	_, diags := Load(filepath.Join(t.TempDir(), "missing"))
	assert.Contains(t, diagText(diags), "cannot resolve plugin root")

	dir := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "plugin.json")))
	p, diags := Load(dir)
	assert.Nil(t, p)
	assert.Contains(t, diagText(diags), "plugin rejected")

	p, diags = Load(writePlugin(t, map[string]string{"plugin.json/": ""}))
	assert.Nil(t, p)
	assert.Contains(t, diagText(diags), "not a regular file")
}

func TestManifest_MalformedValues(t *testing.T) {
	for _, doc := range []string{
		`{"$schema":"` + PluginSchemaID + `","name":"p","keywords":"x"}`,
		`{"$schema":"` + PluginSchemaID + `","name":"p","keywords":[1}`,
	} {
		_, _, err := ParseManifest([]byte(doc))
		assert.Error(t, err, doc)
	}
}

func TestSkills_FilesystemStates(t *testing.T) {
	dir := writePlugin(t, map[string]string{
		"plugin.json":             minimalManifest,
		"skills/.hidden/SKILL.md": skillCfg(".hidden"),
		"skills/dir/SKILL.md/":    "",
		"skills/a/SKILL.md":       skillCfg("a"),
		"skills/file":             "x",
		"skills/dangling/":        "",
	})
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "skills", "dangling", "SKILL.md")))
	skills, diags := mustLoad(t, dir).Skills()
	assert.Len(t, skills, 1)
	txt := diagText(diags)
	assert.Contains(t, txt, "SKILL.md is not a regular file")
	assert.Contains(t, txt, "dangling")
	assert.NotContains(t, txt, ".hidden")

	dir = writePlugin(t, map[string]string{"plugin.json": minimalManifest})
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "skills")))
	_, diags = mustLoad(t, dir).Skills()
	assert.Contains(t, diagText(diags), "skills ignored")
}

func TestMCP_FilesystemStatesAndDataDir(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest})
	require.NoError(t, os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "mcp.json")))
	_, diags := mustLoad(t, dir).MCPServers(t.TempDir())
	assert.Contains(t, diagText(diags), "MCP disabled")

	dir = writePlugin(t, map[string]string{"plugin.json": minimalManifest, "file": "x", "mcp.json": mcpDoc(`{
		"stdio":{"type":"stdio","command":"node"},
		"remote":{"type":"streamable-http","url":"https://example.com/mcp"}}`)})
	p := mustLoad(t, dir)
	servers, diags := p.MCPServers("")
	assert.Equal(t, []string{"remote"}, keys(servers), "remote servers need no PLUGIN_DATA")
	assert.Contains(t, diagText(diags), "no PLUGIN_DATA directory configured")

	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, diags = p.MCPServers(filepath.Join(blocker, "data"))
	assert.Contains(t, diagText(diags), "create PLUGIN_DATA")
}

func TestMCP_CwdAndRemoteErrors(t *testing.T) {
	data := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(data, "f"), []byte("x"), 0o644))
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "file": "x", "mcp.json": mcpDoc(`{
		"cwd-file":{"type":"stdio","command":"node","cwd":"./file"},
		"cwd-data-file":{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}/f/sub"},
		"bad-url":{"type":"sse","url":"http://example.com/sse"},
		"bad-header":{"type":"sse","url":"https://example.com/sse","headers":{"A":"1","a":"2"}}}`)})
	servers, diags := mustLoad(t, dir).MCPServers(data)
	assert.Empty(t, servers)
	txt := diagText(diags)
	assert.Contains(t, txt, `cwd "./file" is not a directory`)
	assert.Contains(t, txt, `cwd "${PLUGIN_DATA}/f/sub"`)
	assert.Contains(t, txt, "must use https")
	assert.Contains(t, txt, "case-insensitive")
	assert.Error(t, ValidateURL("https://example.com/%zz"))
}

func TestMCP_NonObjectMaps(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{
		"env":{"type":"stdio","command":"node","env":["A=1"]},
		"headers":{"type":"sse","url":"https://example.com/sse","headers":"A: 1"}}`)})
	servers, diags := mustLoad(t, dir).MCPServers(t.TempDir())
	assert.Empty(t, servers)
	assert.Contains(t, diagText(diags), `"env" must be an object of strings`)
	assert.Contains(t, diagText(diags), `"headers" must be an object of strings`)
}

func TestSameEnvName(t *testing.T) {
	assert.True(t, sameEnvName("PLUGIN_ROOT", "PLUGIN_ROOT"))
	old := envNamesFold
	t.Cleanup(func() { envNamesFold = old })
	envNamesFold = true
	assert.True(t, sameEnvName("plugin_root", "PLUGIN_ROOT"))
	envNamesFold = false
	assert.False(t, sameEnvName("plugin_root", "PLUGIN_ROOT"))
}

func TestWithin(t *testing.T) {
	assert.True(t, within("/a", "/a"))
	assert.True(t, within("/a", "/a/b"))
	assert.False(t, within("/a", "/ab"))
	assert.True(t, within("/", "/x"))
}
