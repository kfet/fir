package poe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- SSEWriter -----------------------------------------------------------

func TestNewSSEWriter_Headers(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if sse == nil {
		t.Fatal("nil writer")
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control: got %q", got)
	}
	if got := rr.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering: got %q", got)
	}
}

// fakeNonFlusher is an http.ResponseWriter that does NOT implement
// http.Flusher, used to assert NewSSEWriter's failure mode.
type fakeNonFlusher struct {
	h http.Header
	b bytes.Buffer
}

func (f *fakeNonFlusher) Header() http.Header { return f.h }
func (f *fakeNonFlusher) Write(p []byte) (int, error) {
	return f.b.Write(p)
}
func (f *fakeNonFlusher) WriteHeader(int) {}

func TestNewSSEWriter_NoFlusher(t *testing.T) {
	w := &fakeNonFlusher{h: http.Header{}}
	_, err := NewSSEWriter(w)
	if err != ErrFlushUnsupported {
		t.Fatalf("err: got %v, want ErrFlushUnsupported", err)
	}
}

func TestSSEWriter_WriteEvent_Framing(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := sse.WriteEvent("meta", map[string]any{"content_type": "text/markdown"}); err != nil {
		t.Fatalf("WriteEvent meta: %v", err)
	}
	if err := sse.WriteEvent("text", map[string]any{"text": "hello"}); err != nil {
		t.Fatalf("WriteEvent text: %v", err)
	}
	body := rr.Body.String()

	// Each event must be `event: <name>\ndata: <json>\n\n`.
	wantPrefixes := []string{
		"event: meta\ndata: ",
		"event: text\ndata: ",
	}
	for _, p := range wantPrefixes {
		if !strings.Contains(body, p) {
			t.Errorf("body missing prefix %q\nbody=%q", p, body)
		}
	}
	if !strings.Contains(body, `"content_type":"text/markdown"`) {
		t.Errorf("meta payload missing: %q", body)
	}
	if !strings.Contains(body, `"text":"hello"`) {
		t.Errorf("text payload missing: %q", body)
	}
	// Two events => exactly two `\n\n` terminators.
	if got := strings.Count(body, "\n\n"); got != 2 {
		t.Errorf("event terminator count: got %d, want 2 (body=%q)", got, body)
	}
}

func TestSSEWriter_WriteEvent_MarshalError(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	// channels are not JSON-marshalable.
	if err := sse.WriteEvent("text", make(chan int)); err == nil {
		t.Fatal("WriteEvent: expected error on unmarshalable payload")
	}
}

// --- Handler: method + auth ----------------------------------------------

func newReq(t *testing.T, method, body, bearer string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "/poe", strings.NewReader(body))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	h := &Handler{AccessKey: "k"}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(m, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, newReq(t, m, "", "k"))
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("status: got %d, want 405", rr.Code)
			}
			if got := rr.Header().Get("Allow"); got != "POST" {
				t.Errorf("Allow header: got %q, want POST", got)
			}
		})
	}
}

func TestHandler_Auth_Missing(t *testing.T) {
	h := &Handler{AccessKey: "secret"}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, `{"type":"settings"}`, ""))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestHandler_Auth_Wrong(t *testing.T) {
	h := &Handler{AccessKey: "secret"}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, `{"type":"settings"}`, "nope"))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestHandler_Auth_BadScheme(t *testing.T) {
	h := &Handler{AccessKey: "secret"}
	rr := httptest.NewRecorder()
	r := newReq(t, http.MethodPost, `{"type":"settings"}`, "")
	r.Header.Set("Authorization", "Basic c2VjcmV0") // wrong scheme
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestHandler_Auth_DevModeNoKey(t *testing.T) {
	// AccessKey="" means dev mode: any request passes auth.
	h := &Handler{}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, `{"type":"settings"}`, ""))
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

// --- Handler: dispatch ---------------------------------------------------

func TestHandler_BadJSON(t *testing.T) {
	h := &Handler{AccessKey: "k"}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, `not json`, "k"))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

func TestHandler_UnknownType_501(t *testing.T) {
	h := &Handler{AccessKey: "k"}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, `{"type":"future_thing","version":"1.0"}`, "k"))
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status: got %d, want 501", rr.Code)
	}
}

func TestHandler_Settings_OK(t *testing.T) {
	h := &Handler{AccessKey: "k"}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, `{"type":"settings","version":"1.0"}`, "k"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: %q", ct)
	}
	var got SettingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.AllowAttachments != true {
		t.Errorf("AllowAttachments: got %v, want true", got.AllowAttachments)
	}
}

func TestHandler_ReportTypes_200(t *testing.T) {
	h := &Handler{AccessKey: "k"}
	for _, typ := range []string{"report_reaction", "report_error", "report_feedback"} {
		t.Run(typ, func(t *testing.T) {
			rr := httptest.NewRecorder()
			body := `{"type":"` + typ + `","version":"1.0"}`
			h.ServeHTTP(rr, newReq(t, http.MethodPost, body, "k"))
			if rr.Code != http.StatusOK {
				t.Errorf("status: got %d, want 200", rr.Code)
			}
		})
	}
}

func TestHandler_Query_StreamsMetaTextDone(t *testing.T) {
	h := &Handler{AccessKey: "k", BotName: "test-bot"}
	body := `{
		"version":"1.0",
		"type":"query",
		"query":[
			{"role":"user","content":"hi","content_type":"text/markdown","timestamp":1,"message_id":"m-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		],
		"user_id":"u-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"conversation_id":"c-cccccccccccccccccccccccccccccccc",
		"message_id":"m-dddddddddddddddddddddddddddddddd",
		"metadata":""
	}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, body, "k"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", got)
	}

	out := rr.Body.String()

	// Mandatory ordering: meta MUST be first.
	metaIdx := strings.Index(out, "event: meta")
	textIdx := strings.Index(out, "event: text")
	doneIdx := strings.Index(out, "event: done")
	if metaIdx < 0 || textIdx < 0 || doneIdx < 0 {
		t.Fatalf("missing event in output:\n%s", out)
	}
	if !(metaIdx < textIdx && textIdx < doneIdx) {
		t.Errorf("event ordering wrong: meta=%d text=%d done=%d", metaIdx, textIdx, doneIdx)
	}
	if !strings.Contains(out, "test-bot") {
		t.Errorf("body missing bot name: %s", out)
	}
	// echoed identifiers
	for _, want := range []string{
		"u-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"c-cccccccccccccccccccccccccccccccc",
		"m-dddddddddddddddddddddddddddddddd",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestHandler_Query_BadJSON(t *testing.T) {
	h := &Handler{AccessKey: "k"}
	// Valid envelope but inner query field is the wrong type.
	body := `{"type":"query","version":"1.0","query":"not-an-array"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, body, "k"))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rr.Code)
	}
}

// --- end-to-end via httptest.Server (uses real net stack) ---------------

func TestHandler_EndToEnd(t *testing.T) {
	h := &Handler{AccessKey: "k", BotName: "e2e-bot"}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"type":"query","version":"1.0","query":[],"user_id":"u-x","conversation_id":"c-y","message_id":"m-z"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(out)
	for _, want := range []string{"event: meta", "event: text", "event: done", "e2e-bot", "u-x", "c-y", "m-z"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// --- Handler with OnQuery hook -------------------------------------------

func TestHandler_OnQuery_Success(t *testing.T) {
	h := &Handler{
		AccessKey: "k",
		OnQuery: func(ctx context.Context, q *QueryRequest, sse *SSEWriter) error {
			// Emit two text events and done to prove the hook owns the
			// rest of the stream after meta.
			if err := sse.WriteEvent("text", map[string]any{"text": "chunk-1"}); err != nil {
				return err
			}
			if err := sse.WriteEvent("text", map[string]any{"text": "chunk-2"}); err != nil {
				return err
			}
			return sse.WriteEvent("done", map[string]any{})
		},
	}
	body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"hi","message_id":"m-a"}],"user_id":"u-a","conversation_id":"c-a","message_id":"m-a"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, body, "k"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	out := rr.Body.String()
	// meta must still be first (emitted by handleQuery before OnQuery).
	metaIdx := strings.Index(out, "event: meta")
	c1Idx := strings.Index(out, "chunk-1")
	c2Idx := strings.Index(out, "chunk-2")
	doneIdx := strings.Index(out, "event: done")
	if metaIdx < 0 || c1Idx < 0 || c2Idx < 0 || doneIdx < 0 {
		t.Fatalf("missing markers in:\n%s", out)
	}
	if !(metaIdx < c1Idx && c1Idx < c2Idx && c2Idx < doneIdx) {
		t.Errorf("ordering wrong: meta=%d c1=%d c2=%d done=%d", metaIdx, c1Idx, c2Idx, doneIdx)
	}
}

func TestHandler_OnQuery_ErrorEmitsErrorAndDone(t *testing.T) {
	h := &Handler{
		AccessKey: "k",
		OnQuery: func(ctx context.Context, q *QueryRequest, sse *SSEWriter) error {
			return errors.New("something went wrong")
		},
	}
	body := `{"version":"1.0","type":"query","query":[],"user_id":"u-a","conversation_id":"c-a","message_id":"m-a"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq(t, http.MethodPost, body, "k"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (SSE already started)", rr.Code)
	}
	out := rr.Body.String()
	if !strings.Contains(out, "event: meta") {
		t.Error("missing meta event")
	}
	if !strings.Contains(out, "event: error") {
		t.Error("missing error event")
	}
	if !strings.Contains(out, "something went wrong") {
		t.Errorf("error text not in output: %s", out)
	}
	if !strings.Contains(out, "event: done") {
		t.Error("missing done event after error")
	}
	// error must come before done
	errIdx := strings.Index(out, "event: error")
	doneIdx := strings.Index(out, "event: done")
	if errIdx > doneIdx {
		t.Errorf("error event after done: err=%d done=%d", errIdx, doneIdx)
	}
}
