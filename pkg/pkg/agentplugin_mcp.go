package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/kfet/fir/pkg/agentplugin"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/mcp"
)

func init() {
	mcp.RegisterPluginLayer(PluginMCPServers)
}

// pluginDataDir is the client-managed PLUGIN_DATA directory for a plugin.
// It lives outside the install path so its contents survive updates
// (which replace the install path) and reinstalls.
//
// The directory is named <plugin-name>-<hash of the package source>, so two
// installed packages that share a plugin name (a fork and its upstream, say)
// never share state, while reinstalling the same source finds its data.
func (m *Manager) pluginDataDir(name, source string, projectScope bool) string {
	sum := sha256.Sum256([]byte(source))
	leaf := name + "-" + hex.EncodeToString(sum[:4])
	if projectScope {
		return filepath.Join(m.cwd, ".fir", "plugin-data", leaf)
	}
	return filepath.Join(m.agentDir, "plugin-data", leaf)
}

// PluginMCPServers is the mcp.PluginLayerFunc: it returns the MCP servers of
// every installed Agent Plugin, keyed by server name, with the plugin's
// mcp.json as the source. When two plugins define the same server name the
// first installed one wins and the clash is reported.
func PluginMCPServers(agentDir, projectDir string) (map[string]mcp.ServerConfig, map[string]string, error) {
	m := New(agentDir, projectDir, config.NewSettingsManager(projectDir, agentDir))
	return m.pluginMCPServers()
}

func (m *Manager) pluginMCPServers() (map[string]mcp.ServerConfig, map[string]string, error) {
	pkgs, err := m.List()
	if err != nil {
		return nil, nil, fmt.Errorf("agent plugins: %w", err)
	}
	servers := map[string]mcp.ServerConfig{}
	sources := map[string]string{}
	var errs []error
	for _, p := range pkgs {
		if p.Resources == nil || p.Resources.Plugin == nil {
			continue
		}
		pl := p.Resources.Plugin
		cfgs, diags := pl.MCPServers(m.pluginDataDir(pl.Manifest.Name, p.Source.Raw, p.Scope == "project"))
		for _, d := range diags {
			errs = append(errs, errors.New(d.String()))
		}
		src := filepath.Join(pl.Root, agentplugin.MCPFile)
		for name, cfg := range cfgs {
			if prev, dup := sources[name]; dup {
				errs = append(errs, fmt.Errorf("MCP server %q from %s skipped: already defined by %s", name, src, prev))
				continue
			}
			servers[name] = cfg
			sources[name] = src
		}
	}
	return servers, sources, errors.Join(errs...)
}
