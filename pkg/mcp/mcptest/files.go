package mcptest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Names used by NewFilesServer.
const (
	FilesExt        = "io.modelcontextprotocol/files"
	SaveToolName    = "save_file"
	ExportToolName  = "export_file"
	ExportFileName  = "report.csv"
	ExportContent   = "a,b\n1,2\n"
	uploadMarkerKey = "mcptest/uploadRequired"
)

// FilesOptions configures NewFilesServer.
type FilesOptions struct {
	MaxInlineBytes int64 // advertised maxInlineBytes; 0 omits it
	NoUpload       bool  // do not advertise upload
	Form           bool  // issue multipart POST form targets instead of PUT
	Accept         []string
	RequireDigest  bool
	// DownloadDigest, when non-empty, overrides the digest sent with the
	// export download (to test mismatches).
	DownloadDigest string
	// BadTargetURL, when set, is used as the upload target url verbatim.
	BadTargetURL string
	// RepeatUpload answers the retry with another upload request.
	RepeatUpload bool
}

// SavedFile is what the fake recorded for one save_file call.
type SavedFile struct {
	Name, MimeType string
	Data           []byte
	Inline         bool
}

// FilesServer is a fake MCP server implementing the server side of the draft
// files extension, with its own HTTP storage (an httptest server on
// loopback) for upload and download targets.
type FilesServer struct {
	Server  *sdk.Server
	Storage *httptest.Server
	opts    FilesOptions

	mu       sync.Mutex
	staged   map[string][]byte // upload id → bytes received
	saved    []SavedFile
	declared []bool // per tools/call: did the request declare the extension
	reqs     []*http.Request
}

// NewFilesServer starts the fake. Call Close when done.
func NewFilesServer(opts FilesOptions) *FilesServer {
	fs := &FilesServer{opts: opts, staged: map[string][]byte{}}
	fs.Storage = httptest.NewServer(http.HandlerFunc(fs.serveStorage))

	settings := map[string]any{}
	if !opts.NoUpload {
		settings["upload"] = map[string]any{}
	}
	if opts.MaxInlineBytes > 0 {
		settings["maxInlineBytes"] = opts.MaxInlineBytes
	}
	caps := &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}}
	caps.AddExtension(FilesExt, settings)
	s := sdk.NewServer(&sdk.Implementation{Name: "files-srv", Version: "1"}, &sdk.ServerOptions{Capabilities: caps})

	desc := map[string]any{}
	if len(opts.Accept) > 0 {
		desc["accept"] = opts.Accept
	}
	required := []string{"id", "mimeType", "size"}
	if opts.RequireDigest {
		required = append(required, "digest")
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"folder": map[string]any{"type": "string"},
			"file": map[string]any{
				"type":       "object",
				"x-mcp-file": desc,
				"properties": map[string]any{
					"id":       map[string]any{"type": "string"},
					"name":     map[string]any{"type": "string"},
					"mimeType": map[string]any{"type": "string"},
					"size":     map[string]any{"type": "integer"},
					"digest":   map[string]any{"type": "object"},
					"data":     map[string]any{"type": "string"},
				},
				"required": required,
			},
		},
		"required": []string{"file"},
	}
	s.AddTool(&sdk.Tool{Name: SaveToolName, Description: "Save a file", InputSchema: schema}, fs.save)
	s.AddTool(&sdk.Tool{Name: ExportToolName, Description: "Export a file", InputSchema: map[string]any{"type": "object"}}, fs.export)
	fs.Server = s
	return fs
}

// Close stops the storage server.
func (fs *FilesServer) Close() { fs.Storage.Close() }

// Saved returns the files saved so far.
func (fs *FilesServer) Saved() []SavedFile {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]SavedFile(nil), fs.saved...)
}

// Declared reports, per tools/call received, whether it declared the
// extension in its clientCapabilities _meta.
func (fs *FilesServer) Declared() []bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]bool(nil), fs.declared...)
}

// StorageRequests returns the HTTP requests the storage received.
func (fs *FilesServer) StorageRequests() []*http.Request {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]*http.Request(nil), fs.reqs...)
}

func declares(meta sdk.Meta) bool {
	cc, ok := meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
	if !ok {
		return false
	}
	ext, _ := cc["extensions"].(map[string]any)
	_, ok = ext[FilesExt]
	return ok
}

type fileInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Data     string `json:"data"`
	Digest   *struct {
		Value string `json:"value"`
	} `json:"digest"`
}

func toolError(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

func (fs *FilesServer) save(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	fs.mu.Lock()
	fs.declared = append(fs.declared, declares(req.Params.Meta))
	fs.mu.Unlock()
	var args struct {
		File fileInput `json:"file"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return toolError("bad arguments: " + err.Error()), nil
	}
	f := args.File
	if f.ID == "" || f.MimeType == "" {
		return toolError("not a FileInput"), nil
	}
	if fs.opts.RequireDigest && f.Digest == nil {
		return toolError("digest required"), nil
	}
	var data []byte
	inline := false
	switch {
	case f.Data != "":
		b, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil || int64(len(b)) != f.Size {
			return toolError("bad inline data"), nil
		}
		data, inline = b, true
	case f.Size == 0:
	case req.Params.RequestState == "" || fs.opts.RepeatUpload:
		target := map[string]any{"fileId": f.ID, "credentials": "omit"}
		u := fs.Storage.URL + "/u/" + f.ID
		if fs.opts.BadTargetURL != "" {
			u = fs.opts.BadTargetURL
		}
		target["url"] = u
		if fs.opts.Form {
			target["method"] = "POST"
			target["form"] = map[string]any{
				"fields":    []any{map[string]any{"name": "key", "value": "staging/" + f.ID}},
				"fileField": "file",
			}
		} else {
			target["headers"] = map[string]any{"Content-Type": f.MimeType}
		}
		return &sdk.CallToolResult{
			Meta:         sdk.Meta{uploadMarkerKey: map[string]any{"uploads": []any{target}}},
			RequestState: "state:" + f.ID,
		}, nil
	default:
		if req.Params.RequestState != "state:"+f.ID {
			return nil, &jsonrpc.Error{Code: -32602, Message: "bad requestState"}
		}
		fs.mu.Lock()
		b, ok := fs.staged[f.ID]
		fs.mu.Unlock()
		if !ok || int64(len(b)) != f.Size {
			return toolError("bytes have not arrived"), nil
		}
		data = b
	}
	if fs.opts.RequireDigest {
		sum := sha256.Sum256(data)
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.Digest.Value {
			return toolError("digest mismatch"), nil
		}
	}
	fs.mu.Lock()
	fs.saved = append(fs.saved, SavedFile{Name: f.Name, MimeType: f.MimeType, Data: data, Inline: inline})
	fs.mu.Unlock()
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("saved %s (%d bytes)", f.Name, len(data))}}}, nil
}

func (fs *FilesServer) export(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	sum := sha256.Sum256([]byte(ExportContent))
	dg := base64.RawURLEncoding.EncodeToString(sum[:])
	if fs.opts.DownloadDigest != "" {
		dg = fs.opts.DownloadDigest
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: "Exported " + ExportFileName + "."}},
		Meta: sdk.Meta{FilesExt: map[string]any{"downloads": []any{map[string]any{
			"name":        "../" + ExportFileName,
			"mimeType":    "text/csv",
			"size":        len(ExportContent),
			"digest":      map[string]any{"algorithm": "sha-256", "value": dg},
			"url":         fs.Storage.URL + "/d/export",
			"credentials": "omit",
		}}}},
	}, nil
}

func (fs *FilesServer) serveStorage(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.reqs = append(fs.reqs, r.Clone(context.Background()))
	fs.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/d/export":
		_, _ = io.WriteString(w, ExportContent)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/u/"):
		b, _ := io.ReadAll(r.Body)
		fs.stage(strings.TrimPrefix(r.URL.Path, "/u/"), b)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/u/"):
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				b, _ := io.ReadAll(p)
				fs.stage(strings.TrimPrefix(r.URL.Path, "/u/"), b)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (fs *FilesServer) stage(id string, b []byte) {
	fs.mu.Lock()
	fs.staged[id] = b
	fs.mu.Unlock()
}

// WrapServerTransport makes the fake's upload requests real on the wire: a
// result whose _meta carries the fake's marker is rewritten to the
// extension's upload_required result type. (The go-sdk server cannot emit
// that result type itself.)
func WrapServerTransport(t sdk.Transport) sdk.Transport { return &serverTransport{t} }

type serverTransport struct{ inner sdk.Transport }

func (t *serverTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &serverConn{c}, nil
}

type serverConn struct{ sdk.Connection }

func (c *serverConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if resp, ok := msg.(*jsonrpc.Response); ok && len(resp.Result) > 0 {
		var res map[string]any
		if json.Unmarshal(resp.Result, &res) == nil {
			if meta, ok := res["_meta"].(map[string]any); ok {
				if ur, ok := meta[uploadMarkerKey].(map[string]any); ok {
					out := map[string]any{
						"resultType":   "upload_required",
						"uploads":      ur["uploads"],
						"requestState": res["requestState"],
					}
					if b, err := json.Marshal(out); err == nil {
						cp := *resp
						cp.Result = b
						msg = &cp
					}
				}
			}
		}
	}
	return c.Connection.Write(ctx, msg)
}
