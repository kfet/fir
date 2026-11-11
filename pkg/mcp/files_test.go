package mcp

import (
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func httpsEnv() *filesEnv {
	u, _ := url.Parse("https://mcp.example.com/mcp/abc?x=1")
	return &filesEnv{server: "s", mcpURL: u}
}

func TestParseTarget(t *testing.T) {
	stdio := &filesEnv{server: "s"}
	cases := []struct {
		name   string
		env    *filesEnv
		upload bool
		json   string
		ok     bool
	}{
		{"put ok", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u?sig=1","credentials":"omit"}`, true},
		{"null member is absent", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","path":null,"credentials":"omit"}`, true},
		{"unknown member", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","credentials":"omit","x":1}`, false},
		{"meta ignored", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","credentials":"omit","_meta":{"a":1}}`, true},
		{"both url and path", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","path":"/u","credentials":"omit"}`, false},
		{"no credentials", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u"}`, false},
		{"http url", httpsEnv(), true, `{"fileId":"f","url":"http://s.example/u","credentials":"omit"}`, false},
		{"userinfo", httpsEnv(), true, `{"fileId":"f","url":"https://a:b@s.example/u","credentials":"omit"}`, false},
		{"authorization header", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","headers":{"authorization":"x"},"credentials":"omit"}`, false},
		{"cookie header", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","headers":{"Cookie":"x"},"credentials":"omit"}`, false},
		{"sec- header", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","headers":{"Sec-X":"x"},"credentials":"omit"}`, false},
		{"include other origin", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","credentials":"include"}`, false},
		{"include same origin", httpsEnv(), true, `{"fileId":"f","url":"https://mcp.example.com/up","credentials":"include"}`, true},
		{"include forwarded header", httpsEnv(), true, `{"fileId":"f","url":"https://mcp.example.com/up","headers":{"X-Forwarded-For":"1"},"credentials":"include"}`, false},
		{"path ok", httpsEnv(), true, `{"fileId":"f","path":"/uploads/a","credentials":"include"}`, true},
		{"path dotdot", httpsEnv(), true, `{"fileId":"f","path":"/../x","credentials":"include"}`, false},
		{"path double slash", httpsEnv(), true, `{"fileId":"f","path":"//x","credentials":"include"}`, false},
		{"stdio path", stdio, true, `{"fileId":"f","path":"/u","credentials":"omit"}`, false},
		{"stdio include", stdio, true, `{"fileId":"f","url":"https://s.example/u","credentials":"include"}`, false},
		{"stdio url omit", stdio, true, `{"fileId":"f","url":"https://s.example/u","credentials":"omit"}`, true},
		{"form on PUT", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","form":{"fields":[],"fileField":"file"},"credentials":"omit"}`, false},
		{"form ok", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","method":"POST","form":{"fields":[{"name":"k","value":"v"}],"fileField":"file"},"credentials":"omit"}`, true},
		{"form content-type header", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","method":"POST","headers":{"Content-Type":"x"},"form":{"fields":[],"fileField":"file"},"credentials":"omit"}`, false},
		{"form field equals fileField", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","method":"POST","form":{"fields":[{"name":"file","value":"v"}],"fileField":"file"},"credentials":"omit"}`, false},
		{"form value CRLF", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","method":"POST","form":{"fields":[{"name":"k","value":"a\r\nb"}],"fileField":"file"},"credentials":"omit"}`, false},
		{"bad method", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","method":"DELETE","credentials":"omit"}`, false},
		{"expired", httpsEnv(), true, `{"fileId":"f","url":"https://s.example/u","credentials":"omit","expiresAt":"2000-01-01T00:00:00Z"}`, false},
		{"download ok", httpsEnv(), false, `{"name":"a","mimeType":"text/plain","size":3,"url":"https://s.example/d","credentials":"omit"}`, true},
		{"download no size", httpsEnv(), false, `{"mimeType":"text/plain","url":"https://s.example/d","credentials":"omit"}`, false},
		{"download fileId unknown", httpsEnv(), false, `{"fileId":"f","mimeType":"text/plain","size":3,"url":"https://s.example/d","credentials":"omit"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.env.parseTarget(json.RawMessage(c.json), c.upload)
			if c.ok {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "s.example")
			}
		})
	}
}

func TestResolvePathKeepsQuery(t *testing.T) {
	tg, err := httpsEnv().parseTarget(json.RawMessage(`{"fileId":"f","path":"/uploads","credentials":"include"}`), true)
	require.NoError(t, err)
	assert.Equal(t, "https://mcp.example.com/mcp/abc/uploads?x=1", tg.resolved.String())
}

func TestAllowedOriginsRestrictOmitTargets(t *testing.T) {
	e := httpsEnv()
	e.cfg.AllowedOrigins = []string{"https://ok.example", "http://127.0.0.1:8080"}
	_, err := e.parseTarget(json.RawMessage(`{"fileId":"f","url":"https://other.example/u","credentials":"omit"}`), true)
	assert.Error(t, err)
	_, err = e.parseTarget(json.RawMessage(`{"fileId":"f","url":"https://ok.example:443/u","credentials":"omit"}`), true)
	assert.NoError(t, err)
	_, err = e.parseTarget(json.RawMessage(`{"fileId":"f","url":"http://127.0.0.1:8080/u","credentials":"omit"}`), true)
	assert.NoError(t, err, "allowlisted loopback http")
}

func TestRewriteUploadRequired(t *testing.T) {
	out, changed := rewriteUploadRequired(json.RawMessage(`{"resultType":"upload_required","uploads":[{"fileId":"a"}],"requestState":"rs"}`))
	require.True(t, changed)
	var res sdk.CallToolResult
	require.NoError(t, json.Unmarshal(out, &res))
	assert.Equal(t, "rs", res.RequestState)
	ups, ok, err := uploadRequest(&res)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Len(t, ups, 1)

	// A server cannot forge fir's marker on an ordinary result.
	out, changed = rewriteUploadRequired(json.RawMessage(`{"content":[],"_meta":{"` + uploadRequiredKey + `":{"uploads":[]},"k":1}}`))
	require.True(t, changed)
	assert.NotContains(t, string(out), uploadRequiredKey)
	assert.Contains(t, string(out), `"k":1`)

	_, changed = rewriteUploadRequired(json.RawMessage(`{"content":[]}`))
	assert.False(t, changed)
}

func TestParseServerFiles(t *testing.T) {
	mk := func(pv string, settings map[string]any) *sdk.InitializeResult {
		caps := &sdk.ServerCapabilities{}
		if settings != nil {
			caps.AddExtension(FilesExtensionID, settings)
		}
		return &sdk.InitializeResult{ProtocolVersion: pv, Capabilities: caps}
	}
	sf, ok := parseServerFiles(mk("2026-07-28", map[string]any{"upload": map[string]any{}, "maxInlineBytes": 10}))
	require.True(t, ok)
	assert.Equal(t, serverFiles{upload: true, maxInline: 10}, sf)
	_, ok = parseServerFiles(mk("2025-11-25", map[string]any{"upload": map[string]any{}}))
	assert.False(t, ok, "legacy protocol")
	_, ok = parseServerFiles(mk("2026-07-28", nil))
	assert.False(t, ok, "not advertised")
	_, ok = parseServerFiles(mk("2026-07-28", map[string]any{"revision": "2"}))
	assert.False(t, ok, "unknown revision")
}

func TestRewriteFileSchemasNested(t *testing.T) {
	file := map[string]any{"type": "object", "x-mcp-file": map[string]any{}}
	s := map[string]any{"type": "object", "properties": map[string]any{
		"list": map[string]any{"type": "array", "items": file},
		"one":  map[string]any{"anyOf": []any{file, map[string]any{"type": "string"}}},
	}}
	out, found := rewriteFileSchemas(s)
	require.True(t, found)
	b, _ := json.Marshal(out)
	assert.NotContains(t, string(b), "x-mcp-file")
	orig, _ := json.Marshal(s)
	assert.Contains(t, string(orig), "x-mcp-file", "original untouched")
}

func TestSafeBaseName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd": "passwd",
		`C:\x\y.txt`:       "y.txt",
		"..":               "download",
		".bashrc":          "bashrc",
		"a\nb":             "a_b",
		"":                 "download",
	} {
		assert.Equal(t, want, safeBaseName(in), in)
	}
}

func TestMultipartFrameEscapesFilename(t *testing.T) {
	pre, post, ct, err := multipartFrame(&uploadForm{FileField: "file"}, &pendingFile{name: "a\"b\r\n.txt", mimeType: "text/plain"})
	require.NoError(t, err)
	assert.Contains(t, string(pre), `filename="a%22b%0D%0A.txt"`)
	assert.Contains(t, string(pre), "Content-Type: text/plain")
	assert.True(t, strings.HasPrefix(ct, "multipart/form-data; boundary="))
	assert.Contains(t, string(post), "--\r\n")
}

func TestGloballyReachable(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "127.0.0.1": false, "10.1.2.3": false, "169.254.169.254": false,
		"100.64.0.1": false, "::1": false, "fd00::1": false, "2606:4700::1": true, "::ffff:127.0.0.1": false,
	} {
		assert.Equal(t, want, globallyReachable(netip.MustParseAddr(addr)), addr)
	}
}

func TestParseServerFilesCapsInline(t *testing.T) {
	caps := &sdk.ServerCapabilities{}
	caps.AddExtension(FilesExtensionID, map[string]any{"maxInlineBytes": int64(1) << 40})
	sf, ok := parseServerFiles(&sdk.InitializeResult{ProtocolVersion: "2026-07-28", Capabilities: caps})
	require.True(t, ok)
	assert.Equal(t, int64(maxInlineClient), sf.maxInline)
}
