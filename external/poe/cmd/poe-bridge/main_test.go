package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/external/poe/internal/access"
	"github.com/kfet/fir/external/poe/internal/poe"
	relayPkg "github.com/kfet/fir/external/poe/internal/relay"
)

// --- pingResult -----------------------------------------------------------

func TestPingResult_Shape(t *testing.T) {
	r := pingResult()
	if r == nil {
		t.Fatal("pingResult() returned nil")
	}
	if len(r.Content) != 1 {
		t.Fatalf("content len: got %d, want 1", len(r.Content))
	}
	tc, ok := r.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] type: got %T, want *mcp.TextContent", r.Content[0])
	}
	if !strings.Contains(tc.Text, "pong") {
		t.Errorf("text missing 'pong': %q", tc.Text)
	}
	if !strings.Contains(tc.Text, version) {
		t.Errorf("text missing version %q: %q", version, tc.Text)
	}
}

// --- writeChunkSSE --------------------------------------------------------

func TestWriteChunkSSE_TextEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := poe.NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := writeChunkSSE(sse, relayPkg.ReplyChunk{Text: "hello"}); err != nil {
		t.Fatalf("writeChunkSSE: %v", err)
	}
	s := rr.Body.String()
	if !strings.Contains(s, "event: text") {
		t.Errorf("missing event: text in:\n%s", s)
	}
	if !strings.Contains(s, "hello") {
		t.Errorf("missing text content in:\n%s", s)
	}
}

func TestWriteChunkSSE_ReplaceEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := poe.NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := writeChunkSSE(sse, relayPkg.ReplyChunk{Text: "replaced", Replace: true}); err != nil {
		t.Fatalf("writeChunkSSE: %v", err)
	}
	s := rr.Body.String()
	if !strings.Contains(s, "event: replace_response") {
		t.Errorf("missing event: replace_response in:\n%s", s)
	}
	if !strings.Contains(s, "replaced") {
		t.Errorf("missing text content in:\n%s", s)
	}
}

func TestWriteChunkSSE_ErrorEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := poe.NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := writeChunkSSE(sse, relayPkg.ReplyChunk{
		Text:      "something broke",
		IsError:   true,
		ErrorType: "user_caused_error",
	}); err != nil {
		t.Fatalf("writeChunkSSE: %v", err)
	}
	s := rr.Body.String()
	if !strings.Contains(s, "event: error") {
		t.Errorf("missing event: error in:\n%s", s)
	}
	if !strings.Contains(s, "something broke") {
		t.Errorf("missing error text in:\n%s", s)
	}
	if !strings.Contains(s, "user_caused_error") {
		t.Errorf("missing error_type in:\n%s", s)
	}
	if !strings.Contains(s, "allow_retry") {
		t.Errorf("missing allow_retry in:\n%s", s)
	}
}

func TestWriteChunkSSE_ErrorWithoutType(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := poe.NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := writeChunkSSE(sse, relayPkg.ReplyChunk{Text: "oops", IsError: true}); err != nil {
		t.Fatalf("writeChunkSSE: %v", err)
	}
	s := rr.Body.String()
	if !strings.Contains(s, "event: error") {
		t.Errorf("missing event: error in:\n%s", s)
	}
	if strings.Contains(s, "error_type") {
		t.Errorf("unexpected error_type in:\n%s", s)
	}
}

func TestWriteChunkSSE_EmptyText_NoEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	sse, err := poe.NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := writeChunkSSE(sse, relayPkg.ReplyChunk{Text: ""}); err != nil {
		t.Fatalf("writeChunkSSE: %v", err)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("expected no output for empty text, got:\n%s", rr.Body.String())
	}
}

// --- newRelayOnQuery: pairing flow ----------------------------------------

func TestRelayOnQuery_UnpairedUser(t *testing.T) {
	hub := relayPkg.NewHub()
	acl, err := access.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newRelayOnQuery(hub, acl),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"hi","message_id":"m-in"}],"user_id":"u-stranger","conversation_id":"c-a","message_id":"m-pair"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	// Read the full response — the pairing flow emits text+done and returns,
	// so the stream is finite.
	out, _ := io.ReadAll(resp.Body)
	s := string(out)

	if !strings.Contains(s, "Not paired") {
		t.Errorf("missing pairing message in:\n%s", s)
	}
	if !strings.Contains(s, "/poe:access pair") {
		t.Errorf("missing pair command in:\n%s", s)
	}
	if !strings.Contains(s, "event: done") {
		t.Errorf("missing done event in:\n%s", s)
	}
}

func TestRelayOnQuery_SameUnpairedUser_SameCode(t *testing.T) {
	hub := relayPkg.NewHub()
	acl, _ := access.NewStore(t.TempDir())

	h := &poe.Handler{
		AccessKey: "k",
		OnQuery:   newRelayOnQuery(hub, acl),
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	doReq := func(msgID string) string {
		body := `{"version":"1.0","type":"query","query":[],"user_id":"u-repeat","conversation_id":"c-r","message_id":"` + msgID + `"}`
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer k")
		resp, _ := http.DefaultClient.Do(req)
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(out)
	}

	out1 := doReq("m-r1")
	out2 := doReq("m-r2")

	extractCode := func(s string) string {
		idx := strings.Index(s, "pair ")
		if idx < 0 || idx+11 > len(s) {
			return ""
		}
		return s[idx+5 : idx+11]
	}
	c1, c2 := extractCode(out1), extractCode(out2)
	if c1 == "" || c2 == "" {
		t.Fatalf("couldn't extract codes")
	}
	if c1 != c2 {
		t.Errorf("codes differ: %q vs %q", c1, c2)
	}
}

// --- version constant -----------------------------------------------------

func TestVersion_NotEmpty(t *testing.T) {
	if version == "" {
		t.Error("version is empty")
	}
}
