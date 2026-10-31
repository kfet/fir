// Package agentplugin loads Agent Plugins v1.0.0 packages
// (https://agent-plugins.org/specification): it validates plugin.json,
// discovers skills from skills/*/SKILL.md, and maps mcp.json into fir's
// native MCP server configuration.
//
// Every failure is non-fatal to fir and as narrow as the spec allows: a bad
// manifest rejects only that plugin, a bad mcp.json disables only that
// plugin's MCP servers, a bad entry skips only that server, and a bad skill
// skips only that skill. Each one is reported as a Diagnostic.
//
// The 1.0.0 schemas are embedded for reference (see Schemas); they are never
// fetched. Validation is implemented in Go because the specification text is
// authoritative and adds rules JSON Schema cannot express.
package agentplugin

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Canonical schema identifiers for Agent Plugins 1.0.0.
const (
	SpecVersion     = "1.0.0"
	PluginSchemaID  = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	MCPSchemaID     = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	ManifestFile    = "plugin.json"
	MCPFile         = "mcp.json"
	SkillsDir       = "skills"
	ClientNamespace = "dev.fir" // reserved for fir; fir defines no contents yet
)

// Schemas holds the embedded 1.0.0 JSON Schemas (schemas/1.0.0/*.json).
//
//go:embed schemas
var Schemas embed.FS

// pluginSchemas maps a recognised plugin.json $schema to its spec version.
var pluginSchemas = map[string]string{PluginSchemaID: SpecVersion}

// mcpSchemas maps a recognised mcp.json $schema to its spec version.
var mcpSchemas = map[string]string{MCPSchemaID: SpecVersion}

// Diagnostic is one reported problem. Path is the file or directory it is
// about; Message says what was wrong and what fir did about it.
type Diagnostic struct {
	Path    string
	Message string
}

func (d Diagnostic) String() string { return d.Path + ": " + d.Message }

// IsPlugin reports whether dir is an Agent Plugin root, i.e. has a
// plugin.json entry. Packages without one keep fir's native loading.
func IsPlugin(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ManifestFile))
	return err == nil
}

// realRoot returns the filesystem-resolved absolute plugin root.
func realRoot(dir string) (string, error) {
	abs, _ := filepath.Abs(dir) // fails only if the cwd is gone; EvalSymlinks then reports it
	return filepath.EvalSymlinks(abs)
}

var errNotRegular = errors.New("not a regular file")

// readRegular reads p, which must be a regular file (not a directory, FIFO
// or device).
func readRegular(p string) ([]byte, error) {
	fi, err := os.Stat(p)
	if err == nil && !fi.Mode().IsRegular() {
		err = errNotRegular
	}
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// contained resolves p (symlinks included) and reports the resolved path and
// whether it lies within root, which must itself be resolved. A path that
// does not exist cannot be resolved and is reported as an error.
func contained(root, p string) (string, bool, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false, err
	}
	return real, within(root, real), nil
}

// within reports whether resolved path p is root or below it.
// Both must be absolute and clean.
func within(root, p string) bool {
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return p == root || strings.HasPrefix(p, prefix)
}

// Plugin is a loaded plugin manifest.
type Plugin struct {
	Root     string // filesystem-resolved plugin root
	Manifest Manifest
}

// Load validates the plugin at dir. A nil Plugin means it was rejected; the
// diagnostics say why. Diagnostics may be non-empty for an accepted plugin
// (unknown fields, a non-object extensions field).
func Load(dir string) (*Plugin, []Diagnostic) {
	root, err := realRoot(dir)
	if err != nil {
		return nil, []Diagnostic{{dir, fmt.Sprintf("plugin rejected: cannot resolve plugin root: %v", err)}}
	}
	mpath := filepath.Join(root, ManifestFile)
	real, ok, err := contained(root, mpath)
	if err != nil {
		return nil, []Diagnostic{{mpath, fmt.Sprintf("plugin rejected: %v", err)}}
	}
	if !ok {
		return nil, []Diagnostic{{mpath, "plugin rejected: plugin.json resolves outside the plugin root"}}
	}
	data, err := readRegular(real)
	if err != nil {
		return nil, []Diagnostic{{mpath, fmt.Sprintf("plugin rejected: plugin.json: %v", err)}}
	}
	m, diags, err := ParseManifest(data)
	for i := range diags {
		diags[i].Path = mpath
	}
	if err != nil {
		return nil, append(diags, Diagnostic{mpath, "plugin rejected: " + err.Error()})
	}
	return &Plugin{Root: root, Manifest: *m}, diags
}
