package extension

import (
	"sync"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// TestSessionBridge_SendUserMessage_Abort verifies that delivering a message
// with deliver_as="abort" cancels the in-flight turn (the ESC equivalent) and
// propagates to an in-flight, stuck tool call — rather than enqueuing anything.
//
// It wires a StreamFn that asks the agent to call a tool that blocks until its
// context is cancelled, starts a turn, then aborts via the bridge and asserts
// the tool's context fired and the agent returned to idle.
func TestSessionBridge_SendUserMessage_Abort(t *testing.T) {
	model := &ai.Model{
		Provider:      "test",
		ID:            "test-model",
		Name:          "Test",
		ContextWindow: 100000,
	}

	toolStarted := make(chan struct{})
	toolCancelled := make(chan struct{})

	var callMu sync.Mutex
	callN := 0
	streamFn := func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		callMu.Lock()
		callN++
		first := callN == 1
		callMu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
			}
			if first {
				msg.StopReason = ai.StopReasonToolUse
				msg.Content = []ai.AssistantContent{{
					ToolCall: &ai.ToolCall{
						Type:      ai.ContentTypeToolCall,
						ID:        "tc-abort-1",
						Name:      "block_forever",
						Arguments: map[string]any{},
					},
				}}
			} else {
				msg.Content = []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "done"}}}
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
			stream.End(nil)
		}()
		return stream
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: model},
		StreamFn:     streamFn,
	})
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.InMemorySessionStore(),
		ResourceLoader: &stubResourceLoader{},
		Cwd:            t.TempDir(),
	})
	sb := NewSessionBridge(sess)

	var startOnce, cancelOnce sync.Once
	// A tool that blocks until its context is cancelled — the stuck-tool case.
	sb.RegisterTool(ToolDefinition{
		Name:        "block_forever",
		Description: "blocks until aborted",
		Parameters:  map[string]any{"type": "object"},
		Execute: func(tc ToolContext) (ToolResult, error) {
			startOnce.Do(func() { close(toolStarted) })
			<-tc.Context.Done()
			cancelOnce.Do(func() { close(toolCancelled) })
			return ToolResult{IsError: true, Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "aborted"}}}, nil
		},
	})

	go func() { _ = sess.Prompt("call the tool") }()

	select {
	case <-toolStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("tool never started")
	}

	// Deliver the abort. This must cancel the run context, propagating to the
	// in-flight tool, not enqueue anything.
	sb.SendUserMessage("", &SendUserMessageOptions{DeliverAs: "abort"})

	select {
	case <-toolCancelled:
		// The in-flight tool's context was cancelled by the abort.
	case <-time.After(15 * time.Second):
		t.Fatal("tool context was not cancelled after abort")
	}

	select {
	case <-a.IdleChan():
	case <-time.After(15 * time.Second):
		t.Fatal("agent did not return to idle after abort")
	}

	if a.HasQueuedMessages() {
		t.Fatal("abort must not enqueue any message")
	}
}
