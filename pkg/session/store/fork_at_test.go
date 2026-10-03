package store

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
)

func asstText(text string) agent.AgentMessage {
	return agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    []ai.AssistantContent{{Text: &ai.TextContent{Type: ai.ContentTypeText, Text: text}}},
		StopReason: ai.StopReasonStop,
		Timestamp:  2,
	}))
}

// forkFixture writes a parent session:
//
//	e1 user, e2 assistant(call-x, call-y), e3 result x, e4 result y,
//	e5 assistant, e6 user, e7 assistant
func forkFixture(t *testing.T) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = writeSessionFile(t, dir,
		`{"type":"session","version":3,"id":"s1","timestamp":"2026-08-02T07:00:00Z","cwd":"/tmp","invocation":{"model":"claude-opus-4-7"}}`,
		entryLine(t, "e1", "", userMsg("push it").Message),
		entryLine(t, "e2", "e1", asstWithCalls(11, ai.StopReasonToolUse, "call-x", "call-y").Message),
		entryLine(t, "e3", "e2", realResult("call-x").Message),
		entryLine(t, "e4", "e3", realResult("call-y").Message),
		entryLine(t, "e5", "e4", asstText("pushed").Message),
		entryLine(t, "e6", "e5", userMsg("now tag it").Message),
		entryLine(t, "e7", "e6", asstText("tagged").Message),
		"",
	)
	return dir, path
}

func TestForkAt_BranchesWithoutTouchingParent(t *testing.T) {
	dir, path := forkFixture(t)
	before, _ := os.ReadFile(path)

	child, err := ForkAt(path, "e5", "", dir)
	if err != nil {
		t.Fatalf("ForkAt: %v", err)
	}
	defer child.Close()

	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("parent session file was modified")
	}
	if child.GetSessionFile() == path {
		t.Fatal("child must be a new file")
	}
	if child.GetLeafID() != "e5" {
		t.Errorf("child leaf = %q, want e5", child.GetLeafID())
	}
	if got := child.GetHeader().ParentSession; got != path {
		t.Errorf("parentSession = %q, want %q", got, path)
	}
	if inv := child.GetHeader().Invocation; inv == nil || inv.Model != "claude-opus-4-7" {
		t.Errorf("child must inherit the parent's invocation, got %+v", inv)
	}
	eq(t, roles(child.BuildSessionContext().Messages),
		[]string{"user", "assistant", "toolResult:call-x", "toolResult:call-y", "assistant"})
}

func TestForkAt_RejectsDanglingToolUse(t *testing.T) {
	dir, path := forkFixture(t)
	for _, id := range []string{"e2", "e3"} {
		if _, err := ForkAt(path, id, "", dir); err == nil || !strings.Contains(err.Error(), "tool call") {
			t.Errorf("ForkAt(%s) err = %v, want dangling tool call error", id, err)
		}
	}
	if _, err := ForkAt(path, "e4", "", dir); err != nil {
		t.Errorf("ForkAt(e4) after all results: %v", err)
	}
	if _, err := ForkAt(path, "nope", "", dir); err == nil {
		t.Error("expected error for unknown entry")
	}
}

// captureAnthropicBody sends ctx through the real Anthropic provider against
// a local server and returns the raw request body.
func captureAnthropicBody(t *testing.T, msgs []agent.AgentMessage) []byte {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	llm, err := ConvertToLLM(msgs)
	if err != nil {
		t.Fatal(err)
	}
	model := &ai.Model{
		ID: "claude-opus-4-7", API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
		BaseURL: srv.URL, ContextWindow: 200000, MaxTokens: 8192,
	}
	tools := []ai.Tool{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}}
	stream := providers.StreamAnthropic(context.Background(), model,
		ai.Context{SystemPrompt: "sys", Messages: llm, Tools: tools},
		&ai.StreamOptions{APIKey: "test-key", MaxRetries: new(int)})
	for range stream.Events {
	}
	if body == nil {
		t.Fatal("no request captured")
	}
	return body
}

// The child must send exactly the bytes the parent sent for the same turn,
// and its messages must be a byte-identical prefix of the parent's later
// requests, so the Anthropic prompt cache hits across the fork.
func TestForkAt_ByteIdenticalPromptPrefix(t *testing.T) {
	dir, path := forkFixture(t)

	parent, _ := OpenSessionStore(path, dir)
	parentFull := parent.BuildSessionContext().Messages
	parent.Branch("e5")
	parentAtE5 := parent.BuildSessionContext().Messages
	parent.Close()

	child, err := ForkAt(path, "e5", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	childMsgs := child.BuildSessionContext().Messages
	child.Close()

	if got, want := captureAnthropicBody(t, childMsgs), captureAnthropicBody(t, parentAtE5); !bytes.Equal(got, want) {
		t.Fatalf("child request differs from parent at e5:\nchild:  %s\nparent: %s", got, want)
	}

	// Prefix check against the parent's longer history, ignoring the
	// cache_control marker that moves to the newest message.
	strip := func(b []byte) []json.RawMessage {
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		var out []json.RawMessage
		for _, m := range req.Messages {
			if c, ok := m["content"].([]any); ok {
				for _, blk := range c {
					if bm, ok := blk.(map[string]any); ok {
						delete(bm, "cache_control")
					}
				}
			}
			raw, _ := json.Marshal(m)
			out = append(out, raw)
		}
		return out
	}
	c := strip(captureAnthropicBody(t, childMsgs))
	p := strip(captureAnthropicBody(t, parentFull))
	if len(c) == 0 || len(c) > len(p) {
		t.Fatalf("bad lengths child=%d parent=%d", len(c), len(p))
	}
	for i := range c {
		if !bytes.Equal(c[i], p[i]) {
			t.Fatalf("message %d differs:\nchild:  %s\nparent: %s", i, c[i], p[i])
		}
	}
}

func TestForkableLeafID(t *testing.T) {
	dir, path := forkFixture(t)
	ss, _ := OpenSessionStore(path, dir)
	defer ss.Close()
	if got := ss.ForkableLeafID(); got != "e7" {
		t.Errorf("ForkableLeafID at e7 = %q", got)
	}
	ss.Branch("e3") // mid parallel tool results
	if got := ss.ForkableLeafID(); got != "e1" {
		t.Errorf("ForkableLeafID at e3 = %q, want e1", got)
	}
}

func TestResolveSessionRef(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	other := filepath.Join(root, "other")
	for _, d := range []string{dir, other} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	a := filepath.Join(dir, "2026_aaaa-1111.jsonl")
	b := filepath.Join(other, "2026_bbbb-2222.jsonl")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(testHeaderLine+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for ref, want := range map[string]string{
		a:                      a,
		"2026_aaaa-1111.jsonl": a,
		"aaaa":                 a,
		"bbbb-2222":            b,
		"2026":                 a, // project dir is searched first
		"zzzz":                 "",
	} {
		if got := ResolveSessionRef(ref, dir, root); got != want {
			t.Errorf("ResolveSessionRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestForkAt_DropsRuntimeHandle(t *testing.T) {
	dir, path := forkFixture(t)
	if err := WriteState(path, map[string]any{
		"model":   "m",
		"runtime": map[string]any{"handle": "parent-acp-id", "cwd": "/w"},
	}); err != nil {
		t.Fatal(err)
	}
	child, err := ForkAt(path, "e5", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	var st struct {
		Model   string            `json:"model"`
		Runtime map[string]string `json:"runtime"`
	}
	if !ReadState(child.GetSessionFile(), &st) {
		t.Fatal("child state not written")
	}
	if st.Model != "m" || st.Runtime["cwd"] != "/w" {
		t.Errorf("state not carried: %+v", st)
	}
	if h, ok := st.Runtime["handle"]; ok {
		t.Errorf("child inherited parent handle %q", h)
	}
}

func TestForkAt_WriteFailureIsAnError(t *testing.T) {
	_, path := forkFixture(t)
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0500); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if _, err := ForkAt(path, "e5", "", ro); err == nil {
		t.Fatal("expected an error when the child file cannot be written")
	}
}
