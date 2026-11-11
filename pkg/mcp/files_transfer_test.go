package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recorded struct {
	method, path, auth string
	body               []byte
}

// transferEnv returns a filesEnv whose MCP address is a local http server
// (loopback, so path targets are allowed) and the requests it received.
func transferEnv(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) (*filesEnv, *httptest.Server, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), b})
		n := len(reqs)
		mu.Unlock()
		handler(w, r, n)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL + "/mcp")
	dir := t.TempDir()
	e := &filesEnv{
		server:   "s",
		cfg:      FilesConfig{AllowedOrigins: []string{srv.URL}},
		mcpURL:   u,
		token:    func(context.Context) (string, error) { return "tok", nil },
		dir:      func() (string, error) { return dir, nil },
		newHTTP:  defaultTransferClient,
		readFile: os.Open,
	}
	return e, srv, func() []recorded { mu.Lock(); defer mu.Unlock(); return append([]recorded(nil), reqs...) }
}

func pending(t *testing.T, data string) *pendingFile {
	p := writeTemp(t, "a.txt", []byte(data))
	return &pendingFile{id: "f", path: p, name: "a.txt", mimeType: "text/plain", size: int64(len(data)), oob: true}
}

func mustTarget(t *testing.T, e *filesEnv, js string, upload bool) *target {
	t.Helper()
	tg, err := e.parseTarget(json.RawMessage(js), upload)
	require.NoError(t, err)
	return tg
}

func TestTransfer_PathIncludeAttachesCredential(t *testing.T) {
	e, _, got := transferEnv(t, func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(201) })
	f := pending(t, "abc")
	require.NoError(t, e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","path":"/up","credentials":"include"}`, true), f))
	r := got()
	require.Len(t, r, 1)
	assert.Equal(t, "/mcp/up", r[0].path)
	assert.Equal(t, "Bearer tok", r[0].auth)
	assert.Equal(t, "abc", string(r[0].body))
}

func TestTransfer_OmitNeverSendsCredential(t *testing.T) {
	e, srv, got := transferEnv(t, func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(200) })
	f := pending(t, "abc")
	require.NoError(t, e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","url":"`+srv.URL+`/x","credentials":"omit"}`, true), f))
	assert.Empty(t, got()[0].auth)
}

func TestTransfer_PUTRetriedPOSTNot(t *testing.T) {
	e, srv, got := transferEnv(t, func(w http.ResponseWriter, _ *http.Request, n int) {
		if n == 1 || n == 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(201)
	})
	f := pending(t, "abc")
	require.NoError(t, e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","url":"`+srv.URL+`/x","credentials":"omit"}`, true), f))
	assert.Len(t, got(), 2)

	err := e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","url":"`+srv.URL+`/x","method":"POST","credentials":"omit"}`, true), f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 503")
	assert.Len(t, got(), 3, "POST is not resent")
}

func TestTransfer_RedirectAndAuthFailuresFail(t *testing.T) {
	e, srv, _ := transferEnv(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(403)
	})
	f := pending(t, "abc")
	err := e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","url":"`+srv.URL+`/redir","credentials":"omit"}`, true), f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 302")
	err = e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","url":"`+srv.URL+`/x","credentials":"omit"}`, true), f)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestTransfer_FileChangedAfterApproval(t *testing.T) {
	e, srv, got := transferEnv(t, func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(201) })
	f := pending(t, "abc")
	require.NoError(t, os.WriteFile(f.path, []byte("abcdef"), 0o600))
	err := e.transferUp(context.Background(), mustTarget(t, e, `{"fileId":"f","url":"`+srv.URL+`/x","credentials":"omit"}`, true), f)
	require.Error(t, err)
	assert.Empty(t, got())
}

func TestTransfer_PrivateAddressRefusedUnlessAllowed(t *testing.T) {
	e, srv, _ := transferEnv(t, func(w http.ResponseWriter, _ *http.Request, _ int) { w.WriteHeader(201) })
	e.cfg.AllowedOrigins = nil
	// Without an allowlist, plain-http loopback is rejected outright.
	_, err := e.parseTarget(json.RawMessage(`{"fileId":"f","url":"`+srv.URL+`/x","credentials":"omit"}`), true)
	require.Error(t, err)
	// And the dialer refuses loopback for a target that is not allowlisted.
	u, _ := url.Parse(srv.URL)
	tg := &target{resolved: u}
	c, err := defaultTransferClient(tg)
	require.NoError(t, err)
	_, err = c.Get(srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not globally reachable")
}

func TestDownload_Limits(t *testing.T) {
	e, srv, _ := transferEnv(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Path {
		case "/long":
			_, _ = io.WriteString(w, "toolongbody")
		case "/gone":
			w.WriteHeader(404)
		default:
			_, _ = io.WriteString(w, "abc")
		}
	})
	e.cfg.MaxDownloadBytes = 5
	budget := int64(1 << 20)
	dl := func(path string, size int) string {
		return e.downloadOne(context.Background(), json.RawMessage(`{"name":"x.txt","mimeType":"text/plain","size":`+
			itoa(size)+`,"url":"`+srv.URL+path+`","credentials":"omit"}`), &budget)
	}
	assert.Contains(t, dl("/ok", 100), "over the 5-byte limit")
	assert.Contains(t, dl("/long", 3), "expected 3")
	assert.Contains(t, dl("/gone", 3), "HTTP 404")
	assert.Contains(t, dl("/ok", 3), "saved to")
	dir, _ := e.dir()
	entries, _ := os.ReadDir(dir)
	require.Len(t, entries, 1, "failed downloads leave nothing behind")
	assert.Equal(t, "x.txt", filepath.Base(entries[0].Name()))

	budget = 2
	assert.Contains(t, dl("/ok", 3), "download budget")

	lines := e.downloads(context.Background(), nil)
	assert.Nil(t, lines)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
