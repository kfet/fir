package mcptest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callSave(t *testing.T, fs *FilesServer, args any, state string, meta sdk.Meta) (*sdk.CallToolResult, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return fs.save(context.Background(), &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{
		Name: SaveToolName, Arguments: raw, RequestState: state, Meta: meta,
	}})
}

func text(r *sdk.CallToolResult) string { return r.Content[0].(*sdk.TextContent).Text }

func file(id string, size int, data string) map[string]any {
	f := map[string]any{"id": id, "name": "a.txt", "mimeType": "text/plain", "size": size}
	if data != "" {
		f["data"] = data
	}
	return map[string]any{"file": f}
}

func TestFilesServerSaveInlineAndErrors(t *testing.T) {
	fs := NewFilesServer(FilesOptions{MaxInlineBytes: 100, NoUpload: true, Accept: []string{"text/plain"}})
	defer fs.Close()

	declared := sdk.Meta{"io.modelcontextprotocol/clientCapabilities": map[string]any{
		"extensions": map[string]any{FilesExt: map[string]any{}}}}
	res, err := callSave(t, fs, file("f1", 2, base64.StdEncoding.EncodeToString([]byte("hi"))), "", declared)
	require.NoError(t, err)
	assert.Equal(t, "saved a.txt (2 bytes)", text(res))
	assert.Equal(t, []SavedFile{{Name: "a.txt", MimeType: "text/plain", Data: []byte("hi"), Inline: true}}, fs.Saved())

	res, _ = callSave(t, fs, file("f2", 0, ""), "", nil)
	assert.False(t, res.IsError)
	assert.Equal(t, []bool{true, false}, fs.Declared())

	res, _ = callSave(t, fs, file("f3", 5, "!!"), "", nil)
	assert.True(t, res.IsError, "bad inline data")
	res, _ = callSave(t, fs, map[string]any{"file": map[string]any{"name": "x"}}, "", nil)
	assert.True(t, res.IsError, "not a FileInput")
	res, _ = callSave(t, fs, "not an object", "", nil)
	assert.True(t, res.IsError, "bad arguments")

	assert.False(t, declares(sdk.Meta{"io.modelcontextprotocol/clientCapabilities": "x"}))
}

func TestFilesServerDigest(t *testing.T) {
	fs := NewFilesServer(FilesOptions{RequireDigest: true, MaxInlineBytes: 10})
	defer fs.Close()
	res, _ := callSave(t, fs, file("f", 0, ""), "", nil)
	assert.Equal(t, "digest required", text(res))

	data := []byte("abc")
	sum := sha256.Sum256(data)
	args := file("f", 3, base64.StdEncoding.EncodeToString(data))
	args["file"].(map[string]any)["digest"] = map[string]any{"algorithm": "sha-256", "value": "wrong"}
	res, _ = callSave(t, fs, args, "", nil)
	assert.Equal(t, "digest mismatch", text(res))
	args["file"].(map[string]any)["digest"] = map[string]any{"algorithm": "sha-256", "value": base64.RawURLEncoding.EncodeToString(sum[:])}
	res, _ = callSave(t, fs, args, "", nil)
	assert.False(t, res.IsError)
}

func TestFilesServerUploadFlowPUT(t *testing.T) {
	fs := NewFilesServer(FilesOptions{})
	defer fs.Close()
	res, err := callSave(t, fs, file("f1", 3, ""), "", nil)
	require.NoError(t, err)
	assert.Equal(t, "state:f1", res.RequestState)
	ur := res.Meta[uploadMarkerKey].(map[string]any)["uploads"].([]any)[0].(map[string]any)
	assert.Equal(t, fs.Storage.URL+"/u/f1", ur["url"])

	// Retry before the bytes arrived.
	res, _ = callSave(t, fs, file("f1", 3, ""), "state:f1", nil)
	assert.Equal(t, "bytes have not arrived", text(res))
	// Wrong requestState.
	_, err = callSave(t, fs, file("f1", 3, ""), "state:other", nil)
	var je *jsonrpc.Error
	require.ErrorAs(t, err, &je)

	req, _ := http.NewRequest(http.MethodPut, fs.Storage.URL+"/u/f1", strings.NewReader("abc"))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	res, err = callSave(t, fs, file("f1", 3, ""), "state:f1", nil)
	require.NoError(t, err)
	assert.Equal(t, "saved a.txt (3 bytes)", text(res))
	assert.Len(t, fs.StorageRequests(), 1)
}

func TestFilesServerRepeatUpload(t *testing.T) {
	fs := NewFilesServer(FilesOptions{RepeatUpload: true})
	defer fs.Close()
	res, err := callSave(t, fs, file("f1", 3, ""), "state:f1", nil)
	require.NoError(t, err)
	assert.Contains(t, res.Meta, uploadMarkerKey)
}

func TestFilesServerUploadForm(t *testing.T) {
	fs := NewFilesServer(FilesOptions{Form: true, BadTargetURL: "https://x.example/u"})
	defer fs.Close()
	res, _ := callSave(t, fs, file("f1", 3, ""), "", nil)
	ur := res.Meta[uploadMarkerKey].(map[string]any)["uploads"].([]any)[0].(map[string]any)
	assert.Equal(t, "POST", ur["method"])
	assert.Equal(t, "https://x.example/u", ur["url"])

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("key", "k")
	fw, _ := w.CreateFormFile("file", "a.txt")
	_, _ = fw.Write([]byte("abc"))
	_ = w.Close()
	resp, err := http.Post(fs.Storage.URL+"/u/f1", w.FormDataContentType(), &body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, []byte("abc"), fs.staged["f1"])

	resp, err = http.Post(fs.Storage.URL+"/u/f1", "", nil)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestFilesServerExportAndStorage(t *testing.T) {
	fs := NewFilesServer(FilesOptions{DownloadDigest: "bad"})
	defer fs.Close()
	res, err := fs.export(context.Background(), &sdk.CallToolRequest{Params: &sdk.CallToolParamsRaw{}})
	require.NoError(t, err)
	dl := res.Meta[FilesExt].(map[string]any)["downloads"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"algorithm": "sha-256", "value": "bad"}, dl["digest"])

	resp, err := http.Get(dl["url"].(string))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, err = http.Get(fs.Storage.URL + "/nope")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

type fakeConn struct {
	sdk.Connection
	written jsonrpc.Message
}

func (c *fakeConn) Write(_ context.Context, m jsonrpc.Message) error { c.written = m; return nil }

type fakeTransport struct {
	conn sdk.Connection
	err  error
}

func (t fakeTransport) Connect(context.Context) (sdk.Connection, error) { return t.conn, t.err }

func TestWrapServerTransport(t *testing.T) {
	_, err := WrapServerTransport(fakeTransport{err: errors.New("boom")}).Connect(context.Background())
	require.Error(t, err)

	fc := &fakeConn{}
	c, err := WrapServerTransport(fakeTransport{conn: fc}).Connect(context.Background())
	require.NoError(t, err)

	marked := `{"content":[],"requestState":"rs","_meta":{"` + uploadMarkerKey + `":{"uploads":[{"fileId":"a"}]}}}`
	require.NoError(t, c.Write(context.Background(), &jsonrpc.Response{Result: json.RawMessage(marked)}))
	out := string(fc.written.(*jsonrpc.Response).Result)
	assert.Contains(t, out, `"resultType":"upload_required"`)
	assert.Contains(t, out, `"requestState":"rs"`)
	assert.NotContains(t, out, uploadMarkerKey)

	plain := json.RawMessage(`{"content":[]}`)
	require.NoError(t, c.Write(context.Background(), &jsonrpc.Response{Result: plain}))
	assert.Equal(t, string(plain), string(fc.written.(*jsonrpc.Response).Result))
}
