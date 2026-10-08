package interactive

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

const (
	handoffPromptText = "HANDOFF: continue from the briefing above."
	queuedText        = "queued during the long tool"
)

// newHandoffSession builds a file-backed session whose scripted model calls
// long_tool on its first request and answers plainly afterwards. long_tool
// signals started, waits for release, then requests a handoff exactly the
// way the self_handoff extension does (bridge.RestartSession).
func newHandoffSession(t *testing.T) (*session.AgentSession, *extension.SessionBridge, chan struct{}, chan struct{}) {
	t.Helper()
	model := &ai.Model{Provider: "test", ID: "test-model", Name: "Test", ContextWindow: 100000}
	var mu sync.Mutex
	calls := 0
	streamFn := func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role: ai.RoleAssistant, Provider: model.Provider, Model: model.ID,
				StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
			}
			if first {
				msg.StopReason = ai.StopReasonToolUse
				msg.Content = []ai.AssistantContent{{ToolCall: &ai.ToolCall{
					Type: ai.ContentTypeToolCall, ID: "tc-long-1", Name: "long_tool", Arguments: map[string]any{},
				}}}
			} else {
				msg.Content = []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}}
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
			stream.End(nil)
		}()
		return stream
	}

	cwd := t.TempDir()
	agentDir := t.TempDir()
	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: model},
		StreamFn:     streamFn,
		ConvertToLLM: store.ConvertToLLM,
	})
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.NewSessionStore(cwd, filepath.Join(agentDir, "sessions")),
		ResourceLoader: resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir}),
		Cwd:            cwd,
	})
	t.Cleanup(sess.Close)
	sess.SessionStore.NewSession(nil)

	bridge := extension.NewSessionBridge(sess)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	bridge.RegisterTool(extension.ToolDefinition{
		Name: "long_tool", Description: "blocks, then hands off",
		Parameters: map[string]any{"type": "object"},
		Execute: func(tc extension.ToolContext) (extension.ToolResult, error) {
			once.Do(func() { close(started) })
			select {
			case <-release:
			case <-tc.Context.Done():
				return extension.ToolResult{IsError: true, Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "aborted"}}}, nil
			}
			if err := bridge.RestartSession(handoffPromptText, "## Briefing\nline2\nline3"); err != nil {
				return extension.ToolResult{}, err
			}
			return extension.ToolResult{Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "handing off"}}}, nil
		},
	})
	return sess, bridge, started, release
}

// runTurnQueueingDuringTool starts a turn, queues a follow-up while
// long_tool is running, then lets the tool trigger the handoff and waits for
// the aborted turn to return.
func runTurnQueueingDuringTool(t *testing.T, sess *session.AgentSession, started, release chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sess.Prompt("start the long tool")
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("long_tool never started")
	}
	// Streaming → queued as a follow-up, exactly like a user typing mid-turn.
	if err := sess.Prompt(queuedText); err != nil {
		t.Fatalf("queue follow-up: %v", err)
	}
	if sess.Agent.FollowUpQueueLen() != 1 {
		t.Fatalf("expected 1 queued follow-up, got %d", sess.Agent.FollowUpQueueLen())
	}
	close(release)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("aborted turn never returned")
	}
}

// userTexts returns the plain-text user messages recorded in a session file,
// in order.
func userTexts(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatalf("open session file: %v", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != "message" || e.Message.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(e.Message.Content, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// TestHandleHandoff_CarriesQueuedFollowUp is the regression test for queued
// prompts being lost on a self_handoff: a follow-up queued while a long tool
// runs must not be drained into the old (discarded) history but arrive in the
// new session as the first user turn after the handoff prompt, with one
// transcript line saying so.
func TestHandleHandoff_CarriesQueuedFollowUp(t *testing.T) {
	sess, bridge, started, release := newHandoffSession(t)
	tm := newTestModeInternal(t, sess)
	m := tm.mode
	// Wired exactly like setupExtensions, but the test drives handleHandoff
	// itself so it can wait for it deterministically.
	bridge.SetRestartFn(func() {})
	oldFile := sess.SessionStore.GetSessionFile()

	runTurnQueueingDuringTool(t, sess, started, release)
	m.handleHandoff(bridge)
	sess.SessionStore.ForceFlush()

	newFile := sess.SessionStore.GetSessionFile()
	if newFile == oldFile {
		t.Fatal("handoff did not start a new session file")
	}
	for _, txt := range userTexts(t, oldFile) {
		if txt == queuedText {
			t.Fatal("queued follow-up leaked into the old session")
		}
	}
	got := userTexts(t, newFile)
	idx := -1
	for i, txt := range got {
		if txt == handoffPromptText {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(got) || got[idx+1] != queuedText {
		t.Fatalf("want queued follow-up as the first user turn after the handoff prompt; user turns = %q", got)
	}
	if !tm.waitForOutput("1 queued message carried into the new session") {
		t.Fatal("expected the carried-queue transcript line")
	}
	// A second take finds nothing: the request was consumed exactly once.
	if _, ok := bridge.TakePendingRestart(); ok {
		t.Fatal("pending restart not consumed")
	}
	m.handleHandoff(bridge) // no pending restart → no-op
	if sess.SessionStore.GetSessionFile() != newFile {
		t.Fatal("handleHandoff without a pending restart must not reset the session")
	}
}

// TestStartNewSession_ClearDiscardsQueueVisibly: /new and Ctrl+N keep the
// clean start — queued messages do not follow into the new session — but the
// drop is reported instead of being silent, and nothing leaks into the old
// session either.
func TestStartNewSession_ClearDiscardsQueueVisibly(t *testing.T) {
	sess, _, started, _ := newHandoffSession(t)
	tm := newTestModeInternal(t, sess)
	m := tm.mode
	oldFile := sess.SessionStore.GetSessionFile()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sess.Prompt("start the long tool")
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("long_tool never started")
	}
	if err := sess.Prompt(queuedText); err != nil {
		t.Fatalf("queue follow-up: %v", err)
	}

	m.handleClearCommand("", "")
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("aborted turn never returned")
	}
	sess.SessionStore.ForceFlush()

	if sess.Agent.HasQueuedMessages() {
		t.Fatal("/new must not keep queued messages")
	}
	for _, txt := range userTexts(t, oldFile) {
		if txt == queuedText {
			t.Fatal("queued follow-up leaked into the old session")
		}
	}
	if !tm.waitForOutput("Discarded 1 queued message") {
		t.Fatal("expected the discarded-queue notice")
	}
}
