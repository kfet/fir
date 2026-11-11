package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/mcp/mcptest"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type filesFixture struct {
	fs    *mcptest.FilesServer
	mgr   *Manager
	tools map[string]agent.AgentTool
}

func newFilesFixture(t *testing.T, opts mcptest.FilesOptions, fc *FilesConfig) *filesFixture {
	t.Helper()
	fs := mcptest.NewFilesServer(opts)
	t.Cleanup(fs.Close)
	if fc != nil && fc.AllowedOrigins == nil {
		fc.AllowedOrigins = []string{fs.Storage.URL}
	}
	mgr := NewManager(map[string]ServerConfig{"files": {Files: fc}}, false)
	conns := connsForServer(fs.Server)
	t.Cleanup(func() { forgetServerConns(fs.Server) })
	mgr.dialFn = func(string, ServerConfig) (sdk.Transport, error) {
		st, ct := sdk.NewInMemoryTransports()
		go func() { _ = fs.Server.Run(context.Background(), mcptest.WrapServerTransport(st)) }()
		return &breakableTransport{inner: ct, conns: conns}, nil
	}
	tools := startAndWait(t, mgr, context.Background())
	t.Cleanup(func() {
		if dir, err := mgr.filesDirPath(); err == nil {
			_ = os.RemoveAll(dir)
		}
	})
	byName := map[string]agent.AgentTool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	return &filesFixture{fs: fs, mgr: mgr, tools: byName}
}

func (f *filesFixture) call(t *testing.T, tool string, args map[string]any) (agent.AgentToolResult, error) {
	t.Helper()
	tl, ok := f.tools["mcp__files__"+tool]
	require.True(t, ok, "tool %s not listed", tool)
	return tl.Execute(context.Background(), "", args, nil)
}

func resultText(r agent.AgentToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func fileArg(p string) map[string]any {
	return map[string]any{"folder": "f1", "file": map[string]any{"path": p}}
}

func TestFiles_SchemaRewrittenForModel(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true, AutoApprove: true})
	b, err := json.Marshal(f.tools["mcp__files__"+mcptest.SaveToolName].Parameters)
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, "x-mcp-file")
	assert.Contains(t, s, `"path"`)
	assert.NotContains(t, s, `"mimeType"`)
}

func TestFiles_DisabledLeavesSchemaAndDoesNotDeclare(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, nil)
	b, _ := json.Marshal(f.tools["mcp__files__"+mcptest.SaveToolName].Parameters)
	assert.Contains(t, string(b), "x-mcp-file")
	_, err := f.call(t, mcptest.SaveToolName, map[string]any{"file": map[string]any{"id": "x", "mimeType": "text/plain", "size": 0}})
	require.NoError(t, err)
	assert.Equal(t, []bool{false}, f.fs.Declared())
}

func TestFiles_UploadOutOfBandPUT(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true, AutoApprove: true})
	data := []byte("%PDF-1.4 hello world")
	p := writeTemp(t, "Q3 report.pdf", data)
	res, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.NoError(t, err)
	assert.False(t, res.IsError, resultText(res))
	assert.Contains(t, resultText(res), "saved Q3 report.pdf")
	saved := f.fs.Saved()
	require.Len(t, saved, 1)
	assert.Equal(t, data, saved[0].Data)
	assert.False(t, saved[0].Inline)
	assert.Equal(t, "application/pdf", saved[0].MimeType)
	assert.Equal(t, []bool{true, true}, f.fs.Declared(), "initial call and retry both declare the extension")
	reqs := f.fs.StorageRequests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "PUT", reqs[0].Method)
	assert.Equal(t, "application/pdf", reqs[0].Header.Get("Content-Type"))
	assert.Empty(t, reqs[0].Header.Get("Authorization"))
	assert.NotContains(t, resultText(res), "/u/")
}

func TestFiles_UploadForm(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{Form: true}, &FilesConfig{Enabled: true, AutoApprove: true})
	data := []byte("col\nval\n")
	p := writeTemp(t, "data.csv", data)
	res, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.NoError(t, err)
	assert.False(t, res.IsError, resultText(res))
	require.Len(t, f.fs.Saved(), 1)
	assert.Equal(t, data, f.fs.Saved()[0].Data)
	reqs := f.fs.StorageRequests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "POST", reqs[0].Method)
	assert.True(t, strings.HasPrefix(reqs[0].Header.Get("Content-Type"), "multipart/form-data; boundary="))
}

func TestFiles_InlineWithDigest(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{MaxInlineBytes: 1 << 20, RequireDigest: true}, &FilesConfig{Enabled: true, AutoApprove: true})
	data := []byte("small")
	p := writeTemp(t, "a.txt", data)
	res, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.NoError(t, err)
	assert.False(t, res.IsError, resultText(res))
	require.Len(t, f.fs.Saved(), 1)
	assert.True(t, f.fs.Saved()[0].Inline)
	assert.Empty(t, f.fs.StorageRequests())
}

func TestFiles_TooBigForInlineWithoutUpload(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{MaxInlineBytes: 4, NoUpload: true}, &FilesConfig{Enabled: true, AutoApprove: true})
	p := writeTemp(t, "a.txt", []byte("too large"))
	_, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not accept uploads")
}

func TestFiles_AcceptEnforced(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{Accept: []string{"application/pdf"}}, &FilesConfig{Enabled: true, AutoApprove: true})
	p := writeTemp(t, "a.txt", []byte("x"))
	_, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a type the tool accepts")
	assert.Empty(t, f.fs.Saved())
}

func TestFiles_ApprovalRequired(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true})
	p := writeTemp(t, "a.txt", []byte("x"))

	_, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.Error(t, err, "no prompt and no autoApprove must refuse")
	assert.Contains(t, err.Error(), "autoApprove")

	var got FileApprovalRequest
	f.mgr.SetFileApprovalFn(func(_ context.Context, r FileApprovalRequest) (bool, error) { got = r; return false, nil })
	_, err = f.call(t, mcptest.SaveToolName, fileArg(p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declined")
	require.Len(t, got.Files, 1)
	assert.Equal(t, p, got.Files[0].Path)
	assert.Equal(t, mcptest.SaveToolName, got.Tool)
	assert.Empty(t, f.fs.Saved())
	assert.Empty(t, f.fs.Declared(), "nothing reaches the server before approval")

	f.mgr.SetFileApprovalFn(func(context.Context, FileApprovalRequest) (bool, error) { return true, nil })
	res, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Len(t, f.fs.Saved(), 1)
}

func TestFiles_TargetOutsideAllowlistRejected(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{BadTargetURL: "https://evil.example/u/file-1?sig=secret"},
		&FilesConfig{Enabled: true, AutoApprove: true})
	p := writeTemp(t, "a.txt", []byte("x"))
	_, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not run")
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "evil.example")
	assert.Empty(t, f.fs.Saved())
	assert.Len(t, f.fs.Declared(), 1, "no retry after a rejected target")
}

func TestFiles_Download(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true})
	res, err := f.call(t, mcptest.ExportToolName, map[string]any{})
	require.NoError(t, err)
	text := resultText(res)
	assert.Contains(t, text, "Exported")
	assert.NotContains(t, text, "/d/export")
	dir, err := f.mgr.filesDirPath()
	require.NoError(t, err)
	want := filepath.Join(dir, mcptest.ExportFileName)
	assert.Contains(t, text, want)
	b, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Equal(t, mcptest.ExportContent, string(b))
	st, _ := os.Stat(want)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	// The download target never reaches the passthrough data either.
	details, _ := json.Marshal(res.Details)
	assert.NotContains(t, string(details), "/d/export")

	// A second download does not overwrite the first.
	res, err = f.call(t, mcptest.ExportToolName, map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, resultText(res), filepath.Join(dir, "report-1.csv"))
}

func TestFiles_DownloadDigestMismatchDiscarded(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{DownloadDigest: "AAAA"}, &FilesConfig{Enabled: true})
	res, err := f.call(t, mcptest.ExportToolName, map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, resultText(res), "digest mismatch")
	dir, _ := f.mgr.filesDirPath()
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
}

func TestFiles_SecondUploadRequestFails(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{RepeatUpload: true}, &FilesConfig{Enabled: true, AutoApprove: true})
	p := writeTemp(t, "a.txt", []byte("abc"))
	_, err := f.call(t, mcptest.SaveToolName, fileArg(p))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "second upload request")
	assert.Len(t, f.fs.StorageRequests(), 1, "no transfer to the second request's targets")
}

func TestFiles_ManagerCallToolScrubsTargets(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true})
	res, err := f.mgr.CallTool(context.Background(), "files", mcptest.ExportToolName, map[string]any{})
	require.NoError(t, err)
	assert.NotContains(t, res.Meta, FilesExtensionID)

	_, err = f.mgr.CallTool(context.Background(), "files", mcptest.SaveToolName,
		map[string]any{"file": map[string]any{"id": "f", "mimeType": "text/plain", "size": 3}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not run")
}

func TestFiles_RelativePathRejected(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true, AutoApprove: true})
	_, err := f.call(t, mcptest.SaveToolName, fileArg("relative.txt"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestFiles_SymlinkResolvedBeforeApproval(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, &FilesConfig{Enabled: true})
	real := writeTemp(t, "secret.txt", []byte("s"))
	link := filepath.Join(t.TempDir(), "innocent.txt")
	require.NoError(t, os.Symlink(real, link))
	var got FileApprovalRequest
	f.mgr.SetFileApprovalFn(func(_ context.Context, r FileApprovalRequest) (bool, error) { got = r; return false, nil })
	_, err := f.call(t, mcptest.SaveToolName, fileArg(link))
	require.Error(t, err)
	require.Len(t, got.Files, 1)
	want, _ := filepath.EvalSymlinks(real)
	assert.Equal(t, want, got.Files[0].Path)
}

func TestFiles_TargetsScrubbedWhenExtensionUnused(t *testing.T) {
	f := newFilesFixture(t, mcptest.FilesOptions{}, nil)
	res, err := f.call(t, mcptest.ExportToolName, map[string]any{})
	require.NoError(t, err)
	details, _ := json.Marshal(res.Details)
	assert.NotContains(t, string(details), "/d/export")
}
