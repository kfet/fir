package agentplugin

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain doubles as the fixture plugin's stdio MCP server: copied into a
// plugin as ./bin/server and launched with AP_TEST_SERVER=1, it serves one
// tool, "probe", reporting what the process actually received.
func TestMain(m *testing.M) {
	if os.Getenv("AP_TEST_SERVER") == "1" {
		runProbeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type probeReport struct {
	Cwd  string            `json:"cwd"`
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

func runProbeServer() {
	server := sdk.NewServer(&sdk.Implementation{Name: "probe", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{
		Name:        "probe",
		Description: "Report cwd, args and selected env",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		wd, _ := os.Getwd()
		rep := probeReport{Cwd: wd, Args: os.Args[1:], Env: map[string]string{}}
		for _, k := range []string{"PLUGIN_ROOT", "PLUGIN_DATA", "PROBE_CONFIG", "PROBE_LITERAL"} {
			rep.Env[k] = os.Getenv(k)
		}
		b, _ := json.Marshal(rep)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, nil
	})
	_ = server.Run(context.Background(), &sdk.StdioTransport{})
}

// copyFixture copies testdata/fixture-plugin to a temp dir and installs the
// test binary as its bundled ./bin/server. Returns the (unresolved) root.
func copyFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "fixture-plugin")
	src := filepath.Join("testdata", "fixture-plugin")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		return copyFile(p, out, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(exe, filepath.Join(dst, "bin", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

const minimalManifest = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"p"}`

// writePlugin creates a plugin from a path→content map. A nil-content entry
// ending in "/" creates a directory.
func writePlugin(t *testing.T, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugin")
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustLoad(t *testing.T, dir string) *Plugin {
	t.Helper()
	p, diags := Load(dir)
	if p == nil {
		t.Fatalf("plugin rejected: %v", diags)
	}
	return p
}

func diagText(d []Diagnostic) string {
	var b strings.Builder
	for _, x := range d {
		b.WriteString(x.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func skillCfg(name string) string {
	return "---\nname: " + name + "\ndescription: test skill " + name + "\n---\n"
}

func mcpDoc(servers string) string {
	return `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":` + servers + `}`
}

func sortStrings(s []string) { sort.Strings(s) }
