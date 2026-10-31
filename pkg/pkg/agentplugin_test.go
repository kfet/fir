package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPluginSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
	}
}

func agentPluginDir(t *testing.T, name, server string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	writeFiles(t, dir, map[string]string{
		"plugin.json":            `{"$schema":"` + testPluginSchema + `","name":"` + name + `"}`,
		"skills/hello/SKILL.md":  "---\nname: hello\ndescription: say hello\n---\n",
		"skills/hello/deep/x.md": "# not a skill",
		"README.md":              "# not a skill in a plugin",
		"loose/SKILL.md":         "---\nname: loose\ndescription: not in skills/\n---\n",
		"extensions/ignored.py":  "# ---\n# name: ignored\n# ---\n",
		"fir.json":               `{"skills":["loose/SKILL.md"],"extensions":["extensions/*.py"]}`,
		"bin/srv":                "#!/bin/sh\n",
		"mcp.json":               `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"` + server + `":{"type":"stdio","command":"./bin/srv"}}}`,
	})
	return dir
}

func TestScanPackageResources_AgentPlugin(t *testing.T) {
	dir := agentPluginDir(t, "demo", "s")
	res, err := ScanPackageResources(dir)
	require.NoError(t, err)
	require.NotNil(t, res.Plugin)
	assert.Equal(t, "demo", res.Plugin.Manifest.Name)
	require.Len(t, res.Skills, 1, "only skills/*/SKILL.md; fir.json is ignored for plugins")
	assert.True(t, strings.HasSuffix(res.Skills[0], filepath.Join("skills", "hello", "SKILL.md")))
	assert.Empty(t, res.Extensions)
	assert.Empty(t, res.Themes)
}

func TestScanPackageResources_RejectedPluginContributesNothing(t *testing.T) {
	dir := agentPluginDir(t, "demo", "s")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"demo"}`), 0o644))
	res, err := ScanPackageResources(dir)
	require.NoError(t, err)
	assert.Nil(t, res.Plugin)
	assert.Empty(t, res.Skills)
	assert.Empty(t, res.Extensions)
	require.NotEmpty(t, res.Diagnostics)
	assert.Contains(t, res.Diagnostics[len(res.Diagnostics)-1].Message, "plugin rejected")
}

func TestScanPackageResources_NonPluginUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a/b/c/SKILL.md": "---\nname: c\ndescription: deep\n---\n",
		"top.md":         "---\nname: top\ndescription: root md\n---\n",
	})
	res, err := ScanPackageResources(dir)
	require.NoError(t, err)
	assert.Nil(t, res.Plugin)
	assert.Len(t, res.Skills, 2, "autoDiscover still finds any-depth SKILL.md and root *.md")
}

func TestPluginMCPServers_FromInstalledPlugins(t *testing.T) {
	agentDir := t.TempDir()
	cwd := t.TempDir()
	userPlugin := agentPluginDir(t, "alpha", "shared")
	projPlugin := agentPluginDir(t, "beta", "shared")
	writeFiles(t, projPlugin, map[string]string{"mcp.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"shared":{"type":"stdio","command":"./bin/srv"},"own":{"type":"streamable-http","url":"https://example.com/mcp"},"bad":{"type":"stdio","command":"../x"}}}`})
	plain := t.TempDir() // a non-plugin package contributes no MCP
	writeFiles(t, plain, map[string]string{"mcp.json": `{"mcpServers":{"plain":{"command":"x"}}}`})

	sm := &mockSettings{global: []any{userPlugin, plain}, project: []any{projPlugin}}
	m := New(agentDir, cwd, sm)

	servers, sources, err := m.pluginMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `MCP server "bad" skipped`)
	assert.Contains(t, err.Error(), `MCP server "shared" from`)
	assert.Contains(t, err.Error(), "already defined by")
	assert.Len(t, servers, 2)
	assert.Contains(t, servers, "own")
	assert.NotContains(t, servers, "plain")

	alphaRoot, _ := filepath.EvalSymlinks(userPlugin)
	assert.Equal(t, filepath.Join(alphaRoot, "mcp.json"), sources["shared"], "first installed plugin wins")
	assert.Equal(t, filepath.Join(alphaRoot, "bin", "srv"), servers["shared"].Command)
	assert.Equal(t, alphaRoot, servers["shared"].Cwd)

	// PLUGIN_DATA: per scope, outside the install path, created eagerly.
	userData := m.pluginDataDir("alpha", userPlugin, false)
	projData := m.pluginDataDir("beta", projPlugin, true)
	assert.True(t, strings.HasPrefix(userData, filepath.Join(agentDir, "plugin-data", "alpha-")))
	assert.True(t, strings.HasPrefix(projData, filepath.Join(cwd, ".fir", "plugin-data", "beta-")))
	assert.NotEqual(t, m.pluginDataDir("alpha", "other-source", false), userData, "same name, different source: separate data")
	for _, d := range []string{userData, projData} {
		fi, err := os.Stat(d)
		require.NoError(t, err, d)
		assert.True(t, fi.IsDir())
	}
	real, _ := filepath.EvalSymlinks(userData)
	assert.Equal(t, real, servers["shared"].Env["PLUGIN_DATA"])

	// Data survives a reload (update) of the plugin.
	require.NoError(t, os.WriteFile(filepath.Join(userData, "state"), []byte("keep"), 0o600))
	_, _, _ = m.pluginMCPServers()
	b, err := os.ReadFile(filepath.Join(userData, "state"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(b))
}

func TestInstall_AgentPlugin(t *testing.T) {
	agentDir := t.TempDir()
	sm := &mockSettings{}
	m := New(agentDir, t.TempDir(), sm)
	dir := agentPluginDir(t, "demo", "s")
	require.NoError(t, m.Install(dir, false))
	pkgs, err := m.List()
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	require.NotNil(t, pkgs[0].Resources.Plugin)
	assert.Len(t, pkgs[0].Resources.Skills, 1)
}

func TestPluginMCPServers_Exported(t *testing.T) {
	servers, sources, err := PluginMCPServers(t.TempDir(), t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, servers)
	assert.Empty(t, sources)
}

func TestPluginMCPServers_ListError(t *testing.T) {
	m := New(t.TempDir(), t.TempDir(), &mockSettings{global: []any{42}})
	_, _, err := m.pluginMCPServers()
	assert.ErrorContains(t, err, "agent plugins")
}

func TestInstall_AgentPluginWithDiagnostics(t *testing.T) {
	m := New(t.TempDir(), t.TempDir(), &mockSettings{})
	dir := agentPluginDir(t, "demo", "s")
	writeFiles(t, dir, map[string]string{
		"skills/Bad/SKILL.md": "---\nname: Bad\ndescription: x\n---\n",
		"mcp.json":            `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"x":{"type":"nope"}}}`,
	})
	out := captureStdout(t, func() { require.NoError(t, m.Install(dir, false)) })
	assert.Contains(t, out, "skill skipped")
	assert.Contains(t, out, `MCP server "x" skipped`)
	assert.Contains(t, out, `Agent Plugin "demo": 1 skill(s), 0 MCP server(s)`)
	// Listing reports the diagnostics through the log rather than failing.
	pkgs, err := m.List()
	require.NoError(t, err)
	assert.NotEmpty(t, pkgs[0].Resources.Diagnostics)
}

func TestInstall_InvalidAgentPlugin(t *testing.T) {
	m := New(t.TempDir(), t.TempDir(), &mockSettings{})
	dir := agentPluginDir(t, "demo", "s")
	writeFiles(t, dir, map[string]string{"plugin.json": `{"name":"demo"}`})
	out := captureStdout(t, func() { require.NoError(t, m.Install(dir, false)) })
	assert.Contains(t, out, "plugin.json is invalid")
	assert.Contains(t, out, "plugin rejected")
}

func TestPluginLayerRegistered(t *testing.T) {
	assert.True(t, mcp.PluginLayerRegistered(), "pkg/pkg must register the Agent Plugin MCP layer")
}

func TestInstall_AgentPluginGetsNoJSWrappers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := agentPluginDir(t, "demo", "s")
	writeFiles(t, dir, map[string]string{"index.ts": "export default function(pi){}\n"})
	m := New(t.TempDir(), t.TempDir(), &mockSettings{})
	out := captureStdout(t, func() { require.NoError(t, m.Install(dir, false)) })
	assert.NotContains(t, out, "runtime wrapper")
	_, err := os.Lstat(filepath.Join(dir, "main"))
	assert.True(t, os.IsNotExist(err), "fir must not write into an Agent Plugin package")
}
