package agentplugin

// Conformance tests for Agent Plugins 1.0.0, one test per item of the
// specification's Appendix A checklist
// (https://agent-plugins.org/specification#appendix-a-conformance-checklist).
// Test names carry the checklist section and the spec clause.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Plugin loader ---------------------------------------------------------

// §5.1, §5.2: parse and validate plugin.json.
func TestConformance_Loader_ParseAndValidateManifest(t *testing.T) {
	p := mustLoad(t, copyFixture(t))
	m := p.Manifest
	assert.Equal(t, "fixture-plugin", m.Name)
	assert.Equal(t, SpecVersion, m.Version)
	assert.Equal(t, "1.0.0", m.PluginVer)
	assert.Equal(t, &Author{Name: "Example Author", Email: "author@example.com", URL: "https://example.com"}, m.Author)
	assert.Equal(t, []string{"fixture", "conformance"}, m.Keywords)

	for name, doc := range map[string]string{
		"not JSON":          `{`,
		"not an object":     `["x"]`,
		"null":              `null`,
		"version not str":   `{"$schema":"` + PluginSchemaID + `","name":"p","version":1}`,
		"keywords not strs": `{"$schema":"` + PluginSchemaID + `","name":"p","keywords":["a",2]}`,
		"author not object": `{"$schema":"` + PluginSchemaID + `","name":"p","author":"me"}`,
		"author extra":      `{"$schema":"` + PluginSchemaID + `","name":"p","author":{"name":"a","x":"y"}}`,
		"author.url number": `{"$schema":"` + PluginSchemaID + `","name":"p","author":{"url":5}}`,
		"license null":      `{"$schema":"` + PluginSchemaID + `","name":"p","license":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := writePlugin(t, map[string]string{"plugin.json": doc, "skills/a/SKILL.md": skillCfg("a")})
			p, diags := Load(dir)
			assert.Nil(t, p, "fatal schema violation must reject the plugin")
			assert.Contains(t, diagText(diags), "plugin rejected")
		})
	}

	// Metadata is validated by JSON type only (§5.4).
	dir := writePlugin(t, map[string]string{"plugin.json": `{"$schema":"` + PluginSchemaID +
		`","name":"p","version":"not-semver","homepage":"not a url","license":"nonsense","author":{"email":"nope"}}`})
	mustLoad(t, dir)
}

// §5.3, §5.2: required $schema and name; unrecognised $schema rejects (never fetched).
func TestConformance_Loader_RequiredSchemaAndName(t *testing.T) {
	for name, doc := range map[string]string{
		"no $schema":      `{"name":"p"}`,
		"$schema not str": `{"$schema":1,"name":"p"}`,
		"unknown version": `{"$schema":"https://agent-plugins.org/schemas/9.9.9/plugin.schema.json","name":"p"}`,
		"mcp schema id":   `{"$schema":"` + MCPSchemaID + `","name":"p"}`,
		"no name":         `{"$schema":"` + PluginSchemaID + `"}`,
		"name not string": `{"$schema":"` + PluginSchemaID + `","name":7}`,
		"name empty":      `{"$schema":"` + PluginSchemaID + `","name":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, diags := Load(writePlugin(t, map[string]string{"plugin.json": doc}))
			assert.Nil(t, p)
			require.NotEmpty(t, diags)
		})
	}
	_, diags := Load(writePlugin(t, map[string]string{"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/2.0.0/plugin.schema.json","name":"p"}`}))
	assert.Contains(t, diagText(diags), "unsupported Agent Plugins version")
}

// §5.5: plugin name constraints.
func TestConformance_Loader_NameConstraints(t *testing.T) {
	for _, n := range []string{"my-plugin", "acme.tools", "lint3r", "a", "a.b-c", strings.Repeat("a", 64)} {
		assert.True(t, ValidPluginName(n), n)
	}
	for _, n := range []string{"", "My-Plugin", "-start", "end-", ".start", "end.", "has--double", "too.many..dots", "under_score", "sp ace", strings.Repeat("a", 65)} {
		assert.False(t, ValidPluginName(n), n)
		p, _ := Load(writePlugin(t, map[string]string{"plugin.json": `{"$schema":"` + PluginSchemaID + `","name":` + jsonStr(n) + `}`}))
		assert.Nil(t, p, n)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// §5.2: unknown top-level fields are reported and ignored.
func TestConformance_Loader_UnknownFieldsReportedAndIgnored(t *testing.T) {
	dir := writePlugin(t, map[string]string{
		"plugin.json":       `{"$schema":"` + PluginSchemaID + `","name":"p","skills":"./other","mcpServers":{},"zzz":1}`,
		"skills/a/SKILL.md": skillCfg("a"),
	})
	p, diags := Load(dir)
	require.NotNil(t, p)
	txt := diagText(diags)
	for _, f := range []string{"skills", "mcpServers", "zzz"} {
		assert.Contains(t, txt, `unknown top-level field "`+f+`"`)
	}
	// No semantics assigned: skills still come only from the fixed location.
	skills, _ := p.Skills()
	assert.Len(t, skills, 1)
}

// §8.1: unimplemented namespaces are ignored unvalidated; non-object
// extensions is reported and ignored.
func TestConformance_Loader_ExtensionsNamespacesIgnored(t *testing.T) {
	p := mustLoad(t, copyFixture(t))
	assert.Contains(t, p.Manifest.Extensions, "com.example.client")

	for _, ext := range []string{`"str"`, `[1]`, `null`, `5`} {
		dir := writePlugin(t, map[string]string{"plugin.json": `{"$schema":"` + PluginSchemaID + `","name":"p","extensions":` + ext + `}`})
		p, diags := Load(dir)
		require.NotNil(t, p, ext)
		assert.Contains(t, diagText(diags), `"extensions" is not an object`)
	}
	// Namespace contents of any shape are not validated...
	mustLoad(t, writePlugin(t, map[string]string{"plugin.json": `{"$schema":"` + PluginSchemaID + `","name":"p","extensions":{"com.example":{"a":[1,{"b":null}]},"org.example":{}}}`}))
	// ...but each namespace value must itself be an object (schema violation, fatal).
	p, diags := Load(writePlugin(t, map[string]string{"plugin.json": `{"$schema":"` + PluginSchemaID + `","name":"p","extensions":{"com.example":"x"}}`}))
	assert.Nil(t, p)
	assert.Contains(t, diagText(diags), `extensions["com.example"] must be an object`)
}

// §4.1: package paths resolving outside the plugin root are rejected with
// the narrowest failure boundary.
func TestConformance_Loader_PathContainment(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "plugin.json"), []byte(minimalManifest), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "skills", "x"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "skills", "x", "SKILL.md"), []byte(skillCfg("x")), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "mcp.json"), []byte(mcpDoc(`{}`)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "tool"), []byte("#!/bin/sh\n"), 0o755))

	t.Run("plugin.json outside rejects plugin", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"skills/a/SKILL.md": skillCfg("a")})
		require.NoError(t, os.Symlink(filepath.Join(outside, "plugin.json"), filepath.Join(dir, "plugin.json")))
		p, diags := Load(dir)
		assert.Nil(t, p)
		assert.Contains(t, diagText(diags), "outside the plugin root")
	})
	t.Run("skills/ outside invalidates skills only", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest,
			"mcp.json": mcpDoc(`{"s":{"type":"streamable-http","url":"https://example.com/mcp"}}`)})
		require.NoError(t, os.Symlink(filepath.Join(outside, "skills"), filepath.Join(dir, "skills")))
		p := mustLoad(t, dir)
		skills, diags := p.Skills()
		assert.Empty(t, skills)
		assert.Contains(t, diagText(diags), "outside the plugin root")
		servers, _ := p.MCPServers(t.TempDir())
		assert.Len(t, servers, 1, "MCP must still load")
	})
	t.Run("SKILL.md outside skips that skill", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "skills/a/SKILL.md": skillCfg("a"), "skills/x/": ""})
		require.NoError(t, os.Symlink(filepath.Join(outside, "skills", "x", "SKILL.md"), filepath.Join(dir, "skills", "x", "SKILL.md")))
		skills, diags := mustLoad(t, dir).Skills()
		require.Len(t, skills, 1)
		assert.Equal(t, "a", filepath.Base(filepath.Dir(skills[0])))
		assert.Contains(t, diagText(diags), "outside the plugin root")
	})
	t.Run("skill dir outside skips that skill", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "skills/a/SKILL.md": skillCfg("a")})
		require.NoError(t, os.Symlink(filepath.Join(outside, "skills", "x"), filepath.Join(dir, "skills", "x")))
		skills, _ := mustLoad(t, dir).Skills()
		assert.Len(t, skills, 1)
	})
	t.Run("mcp.json outside disables MCP only", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "skills/a/SKILL.md": skillCfg("a")})
		require.NoError(t, os.Symlink(filepath.Join(outside, "mcp.json"), filepath.Join(dir, "mcp.json")))
		p := mustLoad(t, dir)
		servers, diags := p.MCPServers(t.TempDir())
		assert.Empty(t, servers)
		assert.Contains(t, diagText(diags), "MCP disabled")
		skills, _ := p.Skills()
		assert.Len(t, skills, 1)
	})
	t.Run("command or cwd outside skips that server", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "bin/ok": "#!/bin/sh\n", "sub/": ""})
		require.NoError(t, os.Symlink(filepath.Join(outside, "tool"), filepath.Join(dir, "bin", "link")))
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "out")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(mcpDoc(`{
			"ok":{"type":"stdio","command":"./bin/ok","cwd":"./sub"},
			"dotdot":{"type":"stdio","command":"../tool"},
			"symlink":{"type":"stdio","command":"./bin/link"},
			"cwd-dotdot":{"type":"stdio","command":"./bin/ok","cwd":"./../x"},
			"cwd-root-dotdot":{"type":"stdio","command":"./bin/ok","cwd":"${PLUGIN_ROOT}/.."},
			"cwd-symlink":{"type":"stdio","command":"./bin/ok","cwd":"./out"},
			"cwd-data-dotdot":{"type":"stdio","command":"./bin/ok","cwd":"${PLUGIN_DATA}/../x"}
		}`)), 0o644))
		servers, diags := mustLoad(t, dir).MCPServers(filepath.Join(t.TempDir(), "data"))
		assert.Equal(t, []string{"ok"}, keys(servers))
		assert.Equal(t, 6, strings.Count(diagText(diags), "skipped"), diagText(diags))
	})
	t.Run("in-root symlinks are allowed", func(t *testing.T) {
		dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "real/a/SKILL.md": skillCfg("a"), "bin/ok": "#!/bin/sh\n"})
		require.NoError(t, os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "skills")))
		require.NoError(t, os.Symlink("ok", filepath.Join(dir, "bin", "alias")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(mcpDoc(`{"s":{"type":"stdio","command":"./bin/alias"}}`)), 0o644))
		p := mustLoad(t, dir)
		skills, _ := p.Skills()
		assert.Len(t, skills, 1)
		servers, _ := p.MCPServers(t.TempDir())
		assert.Len(t, servers, 1)
	})
}

// §8.2: fir implements no file-based client extension, so namespace
// directories (including fir's reserved dev.fir) contribute nothing.
func TestConformance_Loader_ExtensionDirectories(t *testing.T) {
	p := mustLoad(t, copyFixture(t))
	skills, _ := p.Skills()
	for _, s := range skills {
		assert.NotContains(t, s, "dev.fir")
		assert.NotContains(t, s, "com.example.client")
	}
}

// --- Component discovery ----------------------------------------------------

// §6.1, §7.1: skills only from skills/*/SKILL.md, one level deep; invalid
// skills skipped and reported.
func TestConformance_Discovery_FixedLocations(t *testing.T) {
	p := mustLoad(t, copyFixture(t))
	skills, diags := p.Skills()
	require.Len(t, skills, 1, diagText(diags))
	assert.Equal(t, filepath.Join(p.Root, "skills", "summarize", "SKILL.md"), skills[0])
	txt := diagText(diags)
	assert.Contains(t, txt, "Bad_Name")
	assert.Contains(t, txt, "description is required")
	assert.NotContains(t, txt, "deeper")

	// Root-level SKILL.md and markdown files are not skills in a plugin.
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "SKILL.md": skillCfg("plugin"),
		"README.md": "# readme", "other/x/SKILL.md": skillCfg("x"), "skills/SKILL.md": skillCfg("skills")})
	skills, _ = mustLoad(t, dir).Skills()
	assert.Empty(t, skills)
}

// §6.2: missing fixed locations are not errors; a wrong filesystem kind
// invalidates only that component type.
func TestConformance_Discovery_MissingLocations(t *testing.T) {
	p := mustLoad(t, writePlugin(t, map[string]string{"plugin.json": minimalManifest}))
	skills, d1 := p.Skills()
	servers, d2 := p.MCPServers(t.TempDir())
	assert.Empty(t, skills)
	assert.Empty(t, servers)
	assert.Empty(t, d1)
	assert.Empty(t, d2)

	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "skills": "a file", "mcp.json/": ""})
	p = mustLoad(t, dir)
	_, d1 = p.Skills()
	_, d2 = p.MCPServers(t.TempDir())
	assert.Contains(t, diagText(d1), "not a readable directory")
	assert.Contains(t, diagText(d2), "not a regular file")
}

// --- MCP configuration ------------------------------------------------------

// §7.2.1, §7.2.2, §10.1: $schema selection and the closed mcp.json schema.
func TestConformance_MCP_SchemaAndClosedVariants(t *testing.T) {
	for name, doc := range map[string]string{
		"invalid JSON":       `{`,
		"not object":         `[]`,
		"no $schema":         `{"mcpServers":{}}`,
		"unknown version":    `{"$schema":"https://agent-plugins.org/schemas/9.9.9/mcp.schema.json","mcpServers":{}}`,
		"plugin schema id":   `{"$schema":"` + PluginSchemaID + `","mcpServers":{}}`,
		"no mcpServers":      `{"$schema":"` + MCPSchemaID + `"}`,
		"mcpServers not obj": `{"$schema":"` + MCPSchemaID + `","mcpServers":[]}`,
		"unknown top-level":  `{"$schema":"` + MCPSchemaID + `","mcpServers":{},"servers":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": doc, "skills/a/SKILL.md": skillCfg("a")})
			p := mustLoad(t, dir)
			servers, diags := p.MCPServers(t.TempDir())
			assert.Empty(t, servers)
			assert.Contains(t, diagText(diags), "MCP disabled")
			skills, _ := p.Skills()
			assert.Len(t, skills, 1, "other component types keep loading")
		})
	}

	// A plugin with no stdio servers never creates PLUGIN_DATA.
	remoteOnly := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{"r":{"type":"sse","url":"https://example.com/sse"},"bad":{"type":"stdio","command":"../x"}}`)})
	untouched := filepath.Join(t.TempDir(), "never")
	_, _ = mustLoad(t, remoteOnly).MCPServers(untouched)
	_, statErr := os.Stat(untouched)
	assert.True(t, os.IsNotExist(statErr), "PLUGIN_DATA must not be created without a valid stdio entry")

	// Version mismatch with plugin.json disables MCP (§10.1). Simulate a
	// second recognised version so the mismatch path is reachable.
	mcpSchemas["https://agent-plugins.org/schemas/1.1.0/mcp.schema.json"] = "1.1.0"
	t.Cleanup(func() { delete(mcpSchemas, "https://agent-plugins.org/schemas/1.1.0/mcp.schema.json") })
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest,
		"mcp.json": `{"$schema":"https://agent-plugins.org/schemas/1.1.0/mcp.schema.json","mcpServers":{}}`})
	_, diags := mustLoad(t, dir).MCPServers(t.TempDir())
	assert.Contains(t, diagText(diags), "targets Agent Plugins 1.1.0 but plugin.json targets 1.0.0")

	// An empty mcpServers object is valid.
	_, diags = mustLoad(t, writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{}`)})).MCPServers(t.TempDir())
	assert.Empty(t, diags)

	// Entry-level violations skip only that entry.
	dir = writePlugin(t, map[string]string{"plugin.json": minimalManifest, "bin/s": "#!/bin/sh\n", "mcp.json": mcpDoc(`{
		"good":{"type":"stdio","command":"./bin/s"},
		"not-object":"x",
		"no-type":{"command":"./bin/s"},
		"type-not-str":{"type":1,"command":"./bin/s"},
		"unknown-field":{"type":"stdio","command":"./bin/s","shell":true},
		"cross-variant":{"type":"stdio","command":"./bin/s","url":"https://example.com"},
		"remote-with-command":{"type":"streamable-http","url":"https://example.com","command":"x"},
		"no-command":{"type":"stdio"},
		"empty-command":{"type":"stdio","command":""},
		"args-not-strs":{"type":"stdio","command":"./bin/s","args":[1]},
		"env-not-strs":{"type":"stdio","command":"./bin/s","env":{"A":1}},
		"cwd-not-str":{"type":"stdio","command":"./bin/s","cwd":1},
		"no-url":{"type":"sse"},
		"headers-not-strs":{"type":"sse","url":"https://example.com","headers":{"A":1}}
	}`)})
	servers, diags := mustLoad(t, dir).MCPServers(t.TempDir())
	assert.Equal(t, []string{"good"}, keys(servers))
	assert.Equal(t, 13, strings.Count(diagText(diags), "skipped"), diagText(diags))
}

// §7.2.1 transport support: stdio and streamable-http (and sse) are mapped.
// The stdio path is exercised end to end in TestConformance_Env_StdioRuntime.
func TestConformance_MCP_Transports(t *testing.T) {
	servers, _ := mustLoad(t, copyFixture(t)).MCPServers(t.TempDir())
	assert.Equal(t, "stdio", servers["probe"].Transport)
	assert.Equal(t, "streamable", servers["remote"].Transport)
	assert.Equal(t, "sse", servers["legacy"].Transport)
}

// §7.2.1: each entry's declared type selects the initial transport.
func TestConformance_MCP_DeclaredTransport(t *testing.T) {
	servers, _ := mustLoad(t, copyFixture(t)).MCPServers(t.TempDir())
	assert.Equal(t, mcp.ServerConfig{Transport: "streamable", URL: "https://mcp.example.com/mcp",
		Headers: map[string]string{"X-Tenant": "public-tenant"}}, servers["remote"])
	assert.Equal(t, mcp.ServerConfig{Transport: "sse", URL: "http://127.0.0.1:9/sse"}, servers["legacy"])
}

// §7.2.1: remote URL rules and literal headers.
func TestConformance_MCP_URLAndHeaders(t *testing.T) {
	for _, u := range []string{"https://example.com/mcp", "http://localhost:8080/mcp", "http://127.0.0.1/mcp",
		"http://127.1.2.3/mcp", "http://[::1]:9/mcp", "https://example.com/mcp?x=${PLUGIN_ROOT}"} {
		assert.NoError(t, ValidateURL(u), u)
	}
	for _, u := range []string{"http://example.com/mcp", "http://localhost.example.com/", "ftp://example.com/",
		"/relative", "example.com/mcp", "https://user:pw@example.com/", "https://user@example.com/",
		"https://example.com/mcp#frag", "https://example.com/mcp#", "https:///nohost", "mailto:a@example.com"} {
		assert.Error(t, ValidateURL(u), u)
	}

	assert.NoError(t, ValidateHeaders(map[string]string{"X-Tenant": "a", "Accept-Language": "en, de\tq=1"}))
	for name, h := range map[string]map[string]string{
		"dup case":      {"X-Tenant": "a", "x-tenant": "b"},
		"bad name":      {"X Tenant": "a"},
		"empty name":    {"": "a"},
		"colon in name": {"X:T": "a"},
		"CRLF value":    {"X-T": "a\r\nInjected: 1"},
		"NUL value":     {"X-T": "a\x00"},
		"leading space": {"X-T": " a"},
	} {
		assert.Error(t, ValidateHeaders(h), name)
	}

	// No expansion in url or headers.
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{
		"s":{"type":"streamable-http","url":"https://example.com/${PLUGIN_ROOT}","headers":{"X-Root":"${PLUGIN_ROOT}","X-Home":"${HOME}"}}}`)})
	servers, diags := mustLoad(t, dir).MCPServers(t.TempDir())
	require.Contains(t, servers, "s", diagText(diags))
	assert.Equal(t, "https://example.com/${PLUGIN_ROOT}", servers["s"].URL)
	assert.Equal(t, map[string]string{"X-Root": "${PLUGIN_ROOT}", "X-Home": "${HOME}"}, servers["s"].Headers)
}

// --- Environment and expansion ----------------------------------------------

// §9.1, §7.2.1, §9.2: end-to-end stdio launch of the fixture's bundled
// server through fir's MCP manager. Covers PLUGIN_ROOT/PLUGIN_DATA, the
// default cwd, env overlay, reserved-variable precedence and expansion.
func TestConformance_Env_StdioRuntime(t *testing.T) {
	// The plugin root's own path contains a placeholder, proving expansion
	// is a single non-recursive pass.
	parent := filepath.Join(t.TempDir(), "x${PLUGIN_DATA}y")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	root := filepath.Join(parent, "fixture-plugin")
	require.NoError(t, os.Rename(copyFixture(t), root))
	dataDir := filepath.Join(t.TempDir(), "plugin-data", "fixture-plugin")

	p := mustLoad(t, root)
	servers, diags := p.MCPServers(dataDir)
	txt := diagText(diags)
	assert.Contains(t, txt, `"websocket" skipped: unsupported transport type "websocket"`)
	assert.Contains(t, txt, `"escape" skipped`)
	fi, err := os.Stat(dataDir)
	require.NoError(t, err, "PLUGIN_DATA must exist before launch")
	assert.True(t, fi.IsDir())
	dataReal, _ := filepath.EvalSymlinks(dataDir)

	// Launch only the stdio servers.
	stdio := map[string]mcp.ServerConfig{"probe": servers["probe"], "probe-cwd": servers["probe-cwd"]}
	mgr := mcp.NewManager(stdio, false)
	t.Cleanup(func() { _ = mgr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mgr.Start(ctx)
	require.NoError(t, mgr.WaitReady(ctx))

	tools := mgr.ToolsByServer()
	call := func(server string) probeReport {
		t.Helper()
		require.Len(t, tools[server], 1, "server %s not connected", server)
		res, err := tools[server][0].Execute(ctx, "c", map[string]any{}, nil)
		require.NoError(t, err)
		var rep probeReport
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &rep))
		return rep
	}

	rep := call("probe")
	assert.Equal(t, p.Root, rep.Env["PLUGIN_ROOT"])
	assert.Equal(t, dataReal, rep.Env["PLUGIN_DATA"])
	assert.Equal(t, p.Root, rep.Cwd, "default cwd is the plugin root")
	assert.Equal(t, []string{
		"--root=" + p.Root,
		"--data=" + dataReal + "/x",
		"${HOME}", "$PLUGIN_ROOT", "${PLUGIN_ROOT",
	}, rep.Args)
	assert.Equal(t, p.Root+"/config.json", rep.Env["PROBE_CONFIG"])
	assert.Equal(t, "${OTHER}", rep.Env["PROBE_LITERAL"])

	rep = call("probe-cwd")
	assert.Equal(t, filepath.Join(dataReal, "work"), rep.Cwd)

	// PLUGIN_DATA is writable by the subprocess's owner.
	require.NoError(t, os.WriteFile(filepath.Join(dataReal, "probe"), []byte("x"), 0o600))
}

// §7.2.1: command is one bare or plugin-relative token; no expansion.
func TestConformance_Env_CommandToken(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "bin/s": "#!/bin/sh\n", "mcp.json": mcpDoc(`{
		"bare":{"type":"stdio","command":"node"},
		"rel":{"type":"stdio","command":"./bin/s"},
		"shell-string":{"type":"stdio","command":"node server.js"},
		"absolute":{"type":"stdio","command":"/usr/bin/node"},
		"implicit-rel":{"type":"stdio","command":"bin/s"},
		"placeholder":{"type":"stdio","command":"${PLUGIN_ROOT}/bin/s"},
		"missing":{"type":"stdio","command":"./bin/missing"},
		"dir":{"type":"stdio","command":"./bin"}
	}`)})
	p := mustLoad(t, dir)
	servers, diags := p.MCPServers(t.TempDir())
	assert.Equal(t, []string{"bare", "rel"}, keys(servers), diagText(diags))
	assert.Equal(t, "node", servers["bare"].Command)
	assert.Equal(t, filepath.Join(p.Root, "bin", "s"), servers["rel"].Command)
}

// §7.2.1: plugin root is the default working directory.
func TestConformance_Env_DefaultCwd(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{"s":{"type":"stdio","command":"node"}}`)})
	p := mustLoad(t, dir)
	servers, _ := p.MCPServers(t.TempDir())
	assert.Equal(t, p.Root, servers["s"].Cwd)
}

// §7.2.1: explicit cwd forms and post-resolution containment.
func TestConformance_Env_CwdForms(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "sub/dir/": "", "mcp.json": mcpDoc(`{
		"rel":{"type":"stdio","command":"node","cwd":"./sub"},
		"dot":{"type":"stdio","command":"node","cwd":"./"},
		"root":{"type":"stdio","command":"node","cwd":"${PLUGIN_ROOT}"},
		"root-sub":{"type":"stdio","command":"node","cwd":"${PLUGIN_ROOT}/sub/dir"},
		"data":{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}"},
		"data-sub":{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}/cache/a"},
		"bare-rel":{"type":"stdio","command":"node","cwd":"sub"},
		"absolute":{"type":"stdio","command":"node","cwd":"/tmp"},
		"root-prefix":{"type":"stdio","command":"node","cwd":"${PLUGIN_ROOT}sub"},
		"data-mid":{"type":"stdio","command":"node","cwd":"./${PLUGIN_DATA}"},
		"home":{"type":"stdio","command":"node","cwd":"${HOME}"},
		"missing":{"type":"stdio","command":"node","cwd":"./nope"}
	}`)})
	p := mustLoad(t, dir)
	data := filepath.Join(t.TempDir(), "d")
	servers, diags := p.MCPServers(data)
	dataReal, _ := filepath.EvalSymlinks(data)
	assert.Equal(t, []string{"data", "data-sub", "dot", "rel", "root", "root-sub"}, keys(servers), diagText(diags))
	assert.Equal(t, filepath.Join(p.Root, "sub"), servers["rel"].Cwd)
	assert.Equal(t, p.Root, servers["dot"].Cwd)
	assert.Equal(t, p.Root, servers["root"].Cwd)
	assert.Equal(t, filepath.Join(p.Root, "sub", "dir"), servers["root-sub"].Cwd)
	assert.Equal(t, dataReal, servers["data"].Cwd)
	assert.Equal(t, filepath.Join(dataReal, "cache", "a"), servers["data-sub"].Cwd)
}

// §9.1: configured env overlays the base environment; PLUGIN_ROOT and
// PLUGIN_DATA are set afterwards and cannot be configured.
func TestConformance_Env_OverlayAndReserved(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{
		"ok":{"type":"stdio","command":"node","env":{"HOME":"/override","A":"${PLUGIN_DATA}"}},
		"root":{"type":"stdio","command":"node","env":{"PLUGIN_ROOT":"/x"}},
		"data":{"type":"stdio","command":"node","env":{"PLUGIN_DATA":"/x"}}
	}`)})
	p := mustLoad(t, dir)
	data := t.TempDir()
	servers, diags := p.MCPServers(data)
	dataReal, _ := filepath.EvalSymlinks(data)
	assert.Equal(t, []string{"ok"}, keys(servers))
	assert.Contains(t, diagText(diags), `reserved variable "PLUGIN_ROOT"`)
	assert.Contains(t, diagText(diags), `reserved variable "PLUGIN_DATA"`)
	assert.Equal(t, map[string]string{"HOME": "/override", "A": dataReal, "PLUGIN_ROOT": p.Root, "PLUGIN_DATA": dataReal}, servers["ok"].Env)
}

// §7.2.1: a configured PATH does not take part in resolving a bare command.
// fir leaves the bare name unresolved in the config; the launcher resolves
// it with fir's own PATH (os/exec resolves against the parent environment,
// not cmd.Env).
func TestConformance_Env_PathNotRequired(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{
		"s":{"type":"stdio","command":"node","env":{"PATH":"/nonexistent"}}}`)})
	servers, _ := mustLoad(t, dir).MCPServers(t.TempDir())
	assert.Equal(t, "node", servers["s"].Command)
	assert.Equal(t, "/nonexistent", servers["s"].Env["PATH"])
}

// §9.2: only ${PLUGIN_ROOT}/${PLUGIN_DATA}, single pass, in args/env/cwd —
// not env keys or command.
func TestConformance_Env_ExpansionScope(t *testing.T) {
	dir := writePlugin(t, map[string]string{"plugin.json": minimalManifest, "mcp.json": mcpDoc(`{
		"s":{"type":"stdio","command":"node",
		     "args":["${PLUGIN_ROOT}${PLUGIN_ROOT}","${plugin_root}","$${PLUGIN_DATA}","${USER}","%PLUGIN_ROOT%"],
		     "env":{"K_${PLUGIN_ROOT}":"v"}}}`)})
	p := mustLoad(t, dir)
	data := t.TempDir()
	servers, _ := p.MCPServers(data)
	dataReal, _ := filepath.EvalSymlinks(data)
	s := servers["s"]
	assert.Equal(t, []string{p.Root + p.Root, "${plugin_root}", "$" + dataReal, "${USER}", "%PLUGIN_ROOT%"}, s.Args)
	assert.Equal(t, "v", s.Env["K_${PLUGIN_ROOT}"])
}

// --- Resilience -------------------------------------------------------------

// §11.3, §7: unsupported component types (commands/, hooks, …) are ignored.
func TestConformance_Resilience_UnsupportedComponentTypes(t *testing.T) {
	p := mustLoad(t, copyFixture(t))
	skills, diags := p.Skills()
	assert.Len(t, skills, 1)
	assert.NotContains(t, diagText(diags), "commands")
}

// §7.2.2: an entry with an unsupported transport is skipped alone.
func TestConformance_Resilience_UnsupportedTransportSkipped(t *testing.T) {
	servers, diags := mustLoad(t, copyFixture(t)).MCPServers(t.TempDir())
	assert.NotContains(t, servers, "websocket")
	assert.Contains(t, diagText(diags), `unsupported transport type "websocket"`)
	assert.Equal(t, []string{"legacy", "probe", "probe-cwd", "remote"}, keys(servers))
}

// §11.3, §7.2.2(5): an independent component failing does not stop others —
// a server that cannot start leaves its sibling connected.
func TestConformance_Resilience_IndependentFailure(t *testing.T) {
	root := copyFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "broken"), []byte("not an executable format"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "mcp.json"), []byte(mcpDoc(`{
		"probe":{"type":"stdio","command":"./bin/server","env":{"AP_TEST_SERVER":"1"}},
		"broken":{"type":"stdio","command":"./bin/broken"}}`)), 0o644))
	servers, diags := mustLoad(t, root).MCPServers(t.TempDir())
	require.Len(t, servers, 2, diagText(diags))

	mgr := mcp.NewManager(servers, false)
	t.Cleanup(func() { _ = mgr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mgr.Start(ctx)
	_ = mgr.WaitReady(ctx)
	tools := mgr.ToolsByServer()
	assert.Len(t, tools["probe"], 1)
	assert.Empty(t, tools["broken"])
}

// §11.1(8): fir supports both component types.
func TestConformance_Resilience_SupportsComponentType(t *testing.T) {
	p := mustLoad(t, copyFixture(t))
	skills, _ := p.Skills()
	servers, _ := p.MCPServers(t.TempDir())
	assert.NotEmpty(t, skills)
	assert.NotEmpty(t, servers)
}

func keys(m map[string]mcp.ServerConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
