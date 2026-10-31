package mcp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPluginLayer_LowestPrecedenceWithWarning(t *testing.T) {
	agentDir := t.TempDir()
	project := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "mcp.json"),
		[]byte(`{"mcpServers":{"shared":{"command":"user-cmd"}}}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".fir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".fir", "mcp.json"),
		[]byte(`{"mcpServers":{"proj":{"command":"proj-cmd"}}}`), 0o644))

	var gotAgent, gotProject string
	RegisterPluginLayer(func(a, p string) (map[string]ServerConfig, map[string]string, error) {
		gotAgent, gotProject = a, p
		return map[string]ServerConfig{
				"shared": {Command: "plugin-cmd"},
				"proj":   {Command: "plugin-proj"},
				"only":   {Command: "plugin-only"},
			}, map[string]string{"shared": "/plug/mcp.json", "proj": "/plug/mcp.json", "only": "/plug/mcp.json"},
			errors.New("plugin diag")
	})
	prev := pluginLayer.Load()
	t.Cleanup(func() { pluginLayer.Store(prev) })

	cfg, collisions, err := LoadDefaultConfigsReport(project)
	assert.Equal(t, agentDir, gotAgent)
	assert.Equal(t, project, gotProject)
	assert.Equal(t, "user-cmd", cfg.MCPServers["shared"].Command)
	assert.Equal(t, "proj-cmd", cfg.MCPServers["proj"].Command)
	assert.Equal(t, "plugin-only", cfg.MCPServers["only"].Command)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin diag")
	assert.Contains(t, err.Error(), `MCP server "shared" from plugin /plug/mcp.json is overridden by`)
	assert.Contains(t, err.Error(), `MCP server "proj" from plugin /plug/mcp.json is overridden by`)
	assert.Len(t, collisions, 2)
	src := ConfigSources(project)
	assert.Equal(t, "/plug/mcp.json", src["only"])
}

func TestPluginLayer_DropInShadowsPlugin(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)
	require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "mcp.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "mcp.d", "a.json"),
		[]byte(`{"mcpServers":{"s":{"command":"dropin"}}}`), 0o644))
	RegisterPluginLayer(func(string, string) (map[string]ServerConfig, map[string]string, error) {
		return map[string]ServerConfig{"s": {Command: "plugin"}}, map[string]string{"s": "/plug/mcp.json"}, nil
	})
	prev := pluginLayer.Load()
	t.Cleanup(func() { pluginLayer.Store(prev) })
	cfg, _, err := LoadDefaultConfigsReport(t.TempDir())
	assert.Equal(t, "dropin", cfg.MCPServers["s"].Command)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overridden by")
}

func TestCommandTransport_Cwd(t *testing.T) {
	dir := t.TempDir()
	tr, err := commandTransport(ServerConfig{Command: "true", Cwd: dir})
	require.NoError(t, err)
	assert.Equal(t, dir, transportCmdDir(tr))
}

func TestWithHeaders_SameOriginOnlyAndClientWins(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer other.Close()

	hc := withHeaders(nil, ServerConfig{URL: srv.URL + "/mcp", Headers: map[string]string{"X-Tenant": "t", "Accept": "configured"}})
	require.NotNil(t, hc)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Accept", "client")
	resp, err := hc.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "t", got.Get("X-Tenant"))
	assert.Equal(t, "client", got.Get("Accept"), "client-generated header must win")

	resp, err = hc.Get(other.URL + "/sse-endpoint")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Empty(t, got.Get("X-Tenant"), "configured headers must not go to another origin")

	// Redirects are not followed.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer redir.Close()
	hc = withHeaders(nil, ServerConfig{URL: redir.URL, Headers: map[string]string{"X-Tenant": "t"}})
	resp, err = hc.Get(redir.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)

	assert.Nil(t, withHeaders(nil, ServerConfig{URL: srv.URL}), "no headers keeps the default client")
	assert.True(t, strings.HasSuffix(originOf(mustURL(t, "https://example.com/x")), ":443"))
	assert.Equal(t, originOf(mustURL(t, "https://example.com/x")), originOf(mustURL(t, "https://example.com:443/y")))
}

func transportCmdDir(tr any) string {
	if ct, ok := tr.(*sdk.CommandTransport); ok {
		return ct.Command.Dir
	}
	return ""
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestPluginLayerRegistered_Toggle(t *testing.T) {
	prev := pluginLayer.Load()
	t.Cleanup(func() { pluginLayer.Store(prev) })
	RegisterPluginLayer(nil)
	assert.False(t, PluginLayerRegistered())
	RegisterPluginLayer(func(string, string) (map[string]ServerConfig, map[string]string, error) { return nil, nil, nil })
	assert.True(t, PluginLayerRegistered())
}
