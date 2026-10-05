package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/kfet/fir/pkg/session/store"
)

func cleanupChild(t *testing.T, pa *firAgent, sid string) {
	t.Cleanup(func() {
		if e, ok := pa.removeSession(sid); ok {
			pa.teardownSession(context.Background(), sid, e)
		}
	})
}

// fileLines returns the session file's lines minus the header.
func bodyLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[1:]
}

func TestInitialize_AdvertisesSessionFork(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(acpsdk.InitializeRequest{ProtocolVersion: 1})
	resp, rerr := handler(context.Background(), "initialize", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, _ := json.Marshal(resp)
	var got struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				Fork *struct {
					Meta map[string]any `json:"_meta"`
				} `json:"fork"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	f := got.AgentCapabilities.SessionCapabilities.Fork
	if f == nil || f.Meta["at"] != true {
		t.Fatalf("fork capability missing or without _meta.at: %s", raw)
	}
}

func TestForkSession_DefaultsToLeaf(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	parent := writeForkParent(t, pa.agentDir, cwd)
	before, _ := os.ReadFile(parent)

	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(map[string]any{"sessionId": "parent-uuid", "cwd": cwd, "mcpServers": []any{}})
	resp, rerr := handler(context.Background(), "session/fork", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	sid, _ := resp.(map[string]any)["sessionId"].(string)
	if sid == "" || sid == "parent-uuid" {
		t.Fatalf("child sessionId = %q", sid)
	}
	cleanupChild(t, pa, sid)
	entry := pa.lookupSession(sid)
	if entry == nil {
		t.Fatal("child not registered")
	}
	if got := entry.session.SessionStore.GetLeafID(); got != "e3" {
		t.Errorf("child leaf = %q, want e3", got)
	}
	if after, _ := os.ReadFile(parent); !bytes.Equal(before, after) {
		t.Error("parent file was modified")
	}
	// Byte-identical prefix: every entry line of the child appears verbatim in the parent.
	parentBody := bodyLines(t, parent)
	childBody := bodyLines(t, entry.session.SessionStore.GetSessionFile())
	if len(childBody) != len(parentBody) {
		t.Fatalf("child has %d entries, want %d", len(childBody), len(parentBody))
	}
	for i := range childBody {
		if childBody[i] != parentBody[i] {
			t.Errorf("entry %d differs:\n child  %s\n parent %s", i, childBody[i], parentBody[i])
		}
	}
}

func TestForkSession_MetaAt(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	parent := writeForkParent(t, pa.agentDir, cwd)
	before, _ := os.ReadFile(parent)
	resp, err := pa.UnstableForkSession(context.Background(), acpsdk.UnstableForkSessionRequest{
		SessionId: "parent-uuid", Cwd: cwd, Meta: map[string]any{"at": "e2", "k": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := string(resp.SessionId)
	cleanupChild(t, pa, sid)
	entry := pa.lookupSession(sid)
	if entry == nil {
		t.Fatal("child not registered")
	}
	if got := entry.session.SessionStore.GetLeafID(); got != "e2" {
		t.Errorf("child leaf = %q, want e2", got)
	}
	childBody := bodyLines(t, entry.session.SessionStore.GetSessionFile())
	parentBody := bodyLines(t, parent)
	if len(childBody) != 2 || childBody[0] != parentBody[0] || childBody[1] != parentBody[1] {
		t.Errorf("child prefix not byte-identical: %v", childBody)
	}
	if after, _ := os.ReadFile(parent); !bytes.Equal(before, after) {
		t.Error("parent file was modified")
	}
}

func writeDanglingParent(t *testing.T, agentDir, cwd string) string {
	t.Helper()
	dir := store.DefaultSessionDir(agentDir, cwd)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := dir + "/2026_dangle-uuid.jsonl"
	lines := []string{
		`{"type":"session","version":3,"id":"dangle-uuid","timestamp":"2026-08-02T07:00:00Z","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"e1","parentId":"","timestamp":"2026-08-02T07:00:01Z","message":{"role":"user","content":"hi","timestamp":1}}`,
		`{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-08-02T07:00:02Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{}}],"stopReason":"toolUse","timestamp":2}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestForkSession_DanglingToolUse(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	parent := writeDanglingParent(t, pa.agentDir, cwd)
	before, _ := os.ReadFile(parent)

	// Explicit at on the mid-tool-call entry is rejected.
	if _, err := pa.UnstableForkSession(context.Background(), acpsdk.UnstableForkSessionRequest{
		SessionId: "dangle-uuid", Cwd: cwd, Meta: map[string]any{"at": "e2"},
	}); err == nil || !strings.Contains(err.Error(), "tool call") {
		t.Fatalf("err = %v, want dangling tool call rejection", err)
	}
	// Default fork point steps back to the last forkable entry.
	resp, err := pa.UnstableForkSession(context.Background(), acpsdk.UnstableForkSessionRequest{
		SessionId: "dangle-uuid", Cwd: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := string(resp.SessionId)
	cleanupChild(t, pa, sid)
	if got := pa.lookupSession(sid).session.SessionStore.GetLeafID(); got != "e1" {
		t.Errorf("default leaf = %q, want e1", got)
	}
	if after, _ := os.ReadFile(parent); !bytes.Equal(before, after) {
		t.Error("parent file was modified")
	}
}

func TestForkSession_UnknownSession(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	if _, err := pa.UnstableForkSession(context.Background(), acpsdk.UnstableForkSessionRequest{
		SessionId: "nope", Cwd: cwd,
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestForkSession_LiveParent(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	parent := writeForkParent(t, pa.agentDir, cwd)
	if _, err := pa.resumeSessionLocal(context.Background(), ResumeSessionRequest{SessionId: "parent-uuid", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	cleanupChild(t, pa, "parent-uuid")
	before, _ := os.ReadFile(parent)
	resp, err := pa.UnstableForkSession(context.Background(), acpsdk.UnstableForkSessionRequest{SessionId: "parent-uuid", Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	sid := string(resp.SessionId)
	cleanupChild(t, pa, sid)
	if got := pa.lookupSession(sid).session.SessionStore.GetLeafID(); got != "e3" {
		t.Errorf("child leaf = %q, want e3", got)
	}
	if after, _ := os.ReadFile(parent); !bytes.Equal(before, after) {
		t.Error("parent file was modified")
	}
}
