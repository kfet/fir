package mcp

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The go-sdk decodes a tools/call result into *sdk.CallToolResult, which keeps
// requestState but drops the files extension's "uploads" member and hides the
// result type. filesConnection restores that information on the way in: it
// rewrites an upload_required result so the upload request travels in the
// result's _meta under uploadRequiredKey, where the SDK preserves it.
//
// uploadRequiredKey is fir-private. A server cannot forge it: the connection
// deletes it from every incoming result before (possibly) setting it.
const uploadRequiredKey = "fir.files/uploadRequired"

// wrapTransportForFiles wraps inner so upload_required results survive SDK
// decoding. It is installed only for servers with files enabled.
func wrapTransportForFiles(inner sdk.Transport) sdk.Transport {
	return &filesTransport{inner: inner}
}

type filesTransport struct{ inner sdk.Transport }

func (t *filesTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &filesConnection{Connection: conn}, nil
}

type filesConnection struct{ sdk.Connection }

func (c *filesConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	resp, ok := msg.(*jsonrpc.Response)
	if !ok || len(resp.Result) == 0 {
		return msg, nil
	}
	if out, changed := rewriteUploadRequired(resp.Result); changed {
		resp.Result = out
	}
	return msg, nil
}

// rewriteUploadRequired moves an upload_required result's uploads (and the
// fact that it is one) into _meta[uploadRequiredKey], and strips a
// server-supplied uploadRequiredKey from any other result. It reports
// whether raw changed.
func rewriteUploadRequired(raw json.RawMessage) (json.RawMessage, bool) {
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil {
		return raw, false
	}
	var meta map[string]json.RawMessage
	if m, ok := res["_meta"]; ok {
		_ = json.Unmarshal(m, &meta)
	}
	_, forged := meta[uploadRequiredKey]
	delete(meta, uploadRequiredKey)

	var rt string
	_ = json.Unmarshal(res["resultType"], &rt)
	if rt == "upload_required" {
		if meta == nil {
			meta = map[string]json.RawMessage{}
		}
		uploads := res["uploads"]
		if len(uploads) == 0 {
			uploads = json.RawMessage("null")
		}
		meta[uploadRequiredKey] = json.RawMessage(`{"uploads":` + string(uploads) + `}`)
		delete(res, "uploads")
		// Let the SDK see a plain result: content is required on the wire
		// type, and an unknown resultType is ours to interpret, not its.
		res["resultType"] = json.RawMessage(`"complete"`)
		if _, ok := res["content"]; !ok {
			res["content"] = json.RawMessage("[]")
		}
	} else if !forged {
		return raw, false
	}
	mb, err := json.Marshal(meta)
	if err != nil {
		return raw, false
	}
	res["_meta"] = mb
	out, err := json.Marshal(res)
	if err != nil {
		return raw, false
	}
	return out, true
}
