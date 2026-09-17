package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/mcp"
)

// TestSendNotice_UsesExtensionChannel is the core regression test for the bug
// this plumbing exists to fix: an operational notice must go out as an
// extension notification, never as agent message text (which is
// indistinguishable from the model's own answer tokens on the same stream).
func TestSendNotice_UsesExtensionChannel(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}

	pa.sendNotice("s1", "warning", "provider_retry", "⏳ retrying")

	if got := len(mc.getUpdates()); got != 0 {
		t.Fatalf("notice leaked into agent message text: %d updates", got)
	}
	notices := mc.noticesOf(NoticeMethod)
	if len(notices) != 1 {
		t.Fatalf("expected 1 notice on %s, got %d", NoticeMethod, len(notices))
	}
	p, ok := notices[0].params.(noticeParams)
	if !ok {
		t.Fatalf("unexpected params type %T", notices[0].params)
	}
	if p.SessionId != "s1" || p.Level != "warning" || p.Kind != "provider_retry" || p.Text != "⏳ retrying" {
		t.Errorf("unexpected notice payload: %+v", p)
	}
}

func TestSendNotice_NilConnIsNoop(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	pa.sendNotice("s1", "info", "k", "text") // must not panic
}

func TestSendNotice_DeliveryFailureIsSwallowed(t *testing.T) {
	mc := newMockConn()
	mc.notifyErr = errors.New("pipe closed")
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}

	pa.sendNotice("s1", "warning", "provider_retry", "⏳ retrying") // must not panic

	if len(mc.noticesOf(NoticeMethod)) != 1 {
		t.Error("notice should still have been attempted")
	}
}

func TestNoticeParams_JSONShape(t *testing.T) {
	raw, err := json.Marshal(noticeParams{SessionId: "s1", Level: "warning", Kind: "provider_retry", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sessionId":"s1","level":"warning","kind":"provider_retry","text":"hi"}`
	if string(raw) != want {
		t.Errorf("wire shape drift:\n got %s\nwant %s", raw, want)
	}
}

func TestNoticeMethod_IsExtensionNamespaced(t *testing.T) {
	if !strings.HasPrefix(NoticeMethod, "_") {
		t.Errorf("ACP extension methods must start with '_', got %q", NoticeMethod)
	}
}

// TestSendAgentMessage_TrailingBlankLine covers the separation guarantee for
// genuine agent replies (command output, handoff failures).
func TestSendAgentMessage_TrailingBlankLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello\n\n"},
		{"hello\n", "hello\n\n"},
		{"hello\n\n", "hello\n\n"},
		{"hello\n\n\n", "hello\n\n"},
	}
	for _, tc := range cases {
		mc := newMockConn()
		pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
		pa.sendAgentMessage("s1", tc.in)

		updates := mc.getUpdates()
		if len(updates) != 1 {
			t.Fatalf("expected 1 update, got %d", len(updates))
		}
		raw, _ := json.Marshal(updates[0])
		var decoded struct {
			Update struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Update.Content.Text != tc.want {
			t.Errorf("sendAgentMessage(%q) = %q, want %q", tc.in, decoded.Update.Content.Text, tc.want)
		}
	}
}

// TestDrainMCPEvents_Silent asserts that MCP lifecycle events produce no
// client-visible output at all — they are logged only. This is what stops a
// server-connected line landing mid-sentence inside a streamed reply, and
// also what stops it defeating a relay's ambient-silence detection.
func TestDrainMCPEvents_Silent(t *testing.T) {
	done := make(chan struct{})
	events := make(chan mcp.ServerEvent)
	drained := make(chan struct{})

	go func() {
		drainMCPEvents("s1", done, events)
		close(drained)
	}()

	boom := errors.New("boom")
	for _, ev := range []mcp.ServerEvent{
		{Kind: mcp.ServerConnecting, Name: "srv"},
		{Kind: mcp.ServerReady, Name: "srv"},
		{Kind: mcp.ServerReady, Name: "srv", Err: boom},
		{Kind: mcp.ServerDisconnected, Name: "srv"},
		{Kind: mcp.ServerDisconnected, Name: "srv", Err: boom},
		{Kind: mcp.ServerEventKind(99), Name: "srv"}, // unknown kind: ignored
	} {
		events <- ev
	}

	close(done)
	<-drained
}

// TestInitialize_AdvertisesNoticeCapability pins the _meta advertisement that
// lets a client know it may receive notice notifications.
func TestInitialize_AdvertisesNoticeCapability(t *testing.T) {
	caps := acpsdk.AgentCapabilities{
		Meta: map[string]any{
			"dev.acp-kit": map[string]any{
				"notice": map[string]any{"method": NoticeMethod, "version": 1},
			},
		},
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"_meta"`) || !strings.Contains(string(raw), NoticeMethod) {
		t.Errorf("capability advertisement missing from %s", raw)
	}
}

func TestRawConn_NotifyExtension_RejectsNonExtensionMethod(t *testing.T) {
	r := &rawConn{}
	err := r.NotifyExtension(context.Background(), "session/update", nil)
	if err == nil || !strings.Contains(err.Error(), "must start with '_'") {
		t.Errorf("expected rejection of non-extension method, got %v", err)
	}
}

func TestRawConn_NotifyExtension_WritesNotification(t *testing.T) {
	var out bytes.Buffer
	conn := acpsdk.NewConnection(
		func(context.Context, string, json.RawMessage) (any, *acpsdk.RequestError) { return nil, nil },
		&out, strings.NewReader(""),
	)
	r := &rawConn{conn: conn}

	if err := r.NotifyExtension(context.Background(), NoticeMethod, noticeParams{SessionId: "s1", Text: "hi"}); err != nil {
		t.Fatalf("NotifyExtension: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, NoticeMethod) || !strings.Contains(got, `"s1"`) {
		t.Errorf("notification not on the wire: %q", got)
	}
	if strings.Contains(got, `"id"`) {
		t.Errorf("must be a notification, not a request: %q", got)
	}
}
