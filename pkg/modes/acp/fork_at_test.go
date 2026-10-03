package acp

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/kfet/fir/pkg/session/store"
)

func writeForkParent(t *testing.T, agentDir, cwd string) string {
	t.Helper()
	dir := store.DefaultSessionDir(agentDir, cwd)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := dir + "/2026_parent-uuid.jsonl"
	lines := []string{
		`{"type":"session","version":3,"id":"parent-uuid","timestamp":"2026-08-02T07:00:00Z","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"e1","parentId":"","timestamp":"2026-08-02T07:00:01Z","message":{"role":"user","content":"hi","timestamp":1}}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-08-02T07:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"yo"}],"stopReason":"stop","timestamp":2}}`,
		`{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-08-02T07:00:03Z","message":{"role":"user","content":"more","timestamp":3}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResumeSession_At_ForksChild(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	parent := writeForkParent(t, pa.agentDir, cwd)
	before, _ := os.ReadFile(parent)

	resp, err := pa.resumeSessionLocal(context.Background(), ResumeSessionRequest{
		SessionId: "parent-uuid", Cwd: cwd, At: "e2",
	})
	if err != nil {
		t.Fatalf("resume at: %v", err)
	}
	if resp.SessionId == "" || resp.SessionId == "parent-uuid" {
		t.Fatalf("child sessionId = %q", resp.SessionId)
	}
	entry := pa.lookupSession(resp.SessionId)
	if entry == nil {
		t.Fatal("child session not registered")
	}
	t.Cleanup(func() {
		if e, ok := pa.removeSession(resp.SessionId); ok {
			pa.teardownSession(context.Background(), resp.SessionId, e)
		}
	})
	if got := entry.session.SessionStore.GetLeafID(); got != "e2" {
		t.Errorf("child leaf = %q, want e2", got)
	}
	if after, _ := os.ReadFile(parent); !bytes.Equal(before, after) {
		t.Error("parent session file was modified")
	}

	if _, err := pa.resumeSessionLocal(context.Background(), ResumeSessionRequest{
		SessionId: "parent-uuid", Cwd: cwd, At: "missing",
	}); err == nil {
		t.Error("expected error for unknown entry")
	}
	if _, err := pa.resumeSessionLocal(context.Background(), ResumeSessionRequest{
		SessionId: "no-such-uuid", Cwd: cwd, At: "e2",
	}); err == nil {
		t.Error("expected error for unknown session with at")
	}
}

func TestSplitAtFlag(t *testing.T) {
	rest, at := splitAtFlag("3 --at abc")
	if rest != "3" || at != "abc" {
		t.Fatalf("got %q %q", rest, at)
	}
	if rest, at := splitAtFlag("/x/y.jsonl"); rest != "/x/y.jsonl" || at != "" {
		t.Fatalf("got %q %q", rest, at)
	}
}

func TestResumeSessionSDK_At_ReturnsChildInMeta(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	writeForkParent(t, pa.agentDir, cwd)
	resp, err := pa.ResumeSession(context.Background(), acpsdk.ResumeSessionRequest{
		SessionId: "parent-uuid", Cwd: cwd, McpServers: []acpsdk.McpServer{},
		Meta: map[string]any{"at": "e2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := resp.Meta["sessionId"].(string)
	if sid == "" || pa.lookupSession(sid) == nil {
		t.Fatalf("child sessionId in _meta = %q", sid)
	}
	t.Cleanup(func() {
		if e, ok := pa.removeSession(sid); ok {
			pa.teardownSession(context.Background(), sid, e)
		}
	})
}

func TestHandleResumeArg_At(t *testing.T) {
	mc := newMockConn()
	pa := &firAgent{conn: mc, sessions: make(map[string]*firSession)}
	agentDir := t.TempDir()
	cwd := t.TempDir()
	parent := writeForkParent(t, agentDir, cwd)
	sess := newMinimalSession(t)
	defer sess.Close()
	entry := &firSession{termState: newTerminalState(), agentDir: agentDir, cwd: cwd, session: sess}

	pa.handleResumeArg("s1", entry, parent+" --at")
	if msg := getLastAgentMessage(mc.getUpdates()); !strings.Contains(msg, "requires an entry id") {
		t.Errorf("bare --at: got %q", msg)
	}

	pa.handleResumeArg("s1", entry, parent+" --at nope")
	if msg := getLastAgentMessage(mc.getUpdates()); !strings.Contains(msg, "Failed to fork") {
		t.Errorf("bad entry: got %q", msg)
	}

	pa.handleResumeArg("s1", entry, parent+" --at e2")
	if got := sess.SessionStore.GetLeafID(); got != "e2" {
		t.Errorf("leaf after /resume --at = %q, want e2", got)
	}
	if sess.SessionStore.GetSessionFile() == parent {
		t.Error("switched to the parent file instead of a child")
	}
}
