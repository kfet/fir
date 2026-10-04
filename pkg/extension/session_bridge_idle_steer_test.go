package extension

import (
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// TestSessionBridge_SendUserMessage_SteerWhenIdleRuns: steer/followUp sent
// to an idle session (e.g. `fir send --steer` / `--follow`) used to sit in a
// queue no loop drains, so nothing ran and `fir send --wait` hung. With no
// run in progress they are delivered as a prompt.
func TestSessionBridge_SendUserMessage_SteerWhenIdleRuns(t *testing.T) {
	for _, deliverAs := range []string{"steer", "followUp"} {
		t.Run(deliverAs, func(t *testing.T) {
			model := &ai.Model{Provider: "test", ID: "test-model", Name: "Test", ContextWindow: 100000}
			called := make(chan string, 1)
			streamFn := func(_ *ai.Model, c ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
				stream := ai.NewAssistantMessageEventStream()
				go func() {
					msg := &ai.AssistantMessage{
						Role: ai.RoleAssistant, Provider: model.Provider, Model: model.ID,
						StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
						Content: []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}},
					}
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
					stream.End(nil)
				}()
				select {
				case called <- deliverAs:
				default:
				}
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
			t.Cleanup(sess.Close)

			NewSessionBridge(sess).SendUserMessage("do it", &SendUserMessageOptions{DeliverAs: deliverAs})

			select {
			case <-called:
			case <-time.After(20 * time.Second):
				t.Fatalf("%s to an idle session never started a run", deliverAs)
			}
			a.WaitForIdle()
		})
	}
}
