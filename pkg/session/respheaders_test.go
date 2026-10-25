package session

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/kfet/agent"
	core "github.com/kfet/ai"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
)

// The default stream factory must record the response headers a provider
// reports under the call's session id, clear stale ones when a new call
// starts, and leave side queries out.
func TestDefaultStreamFn_RecordsResponseHeaders(t *testing.T) {
	const api = "resp-headers-test-api"
	report := map[string]string{"x-ratelimit-remaining": "5"}
	ai.DefaultRegistry.RegisterApiProvider(&ai.ApiProvider{
		Api: api,
		StreamSimple: func(ctx context.Context, _ *core.Model, _ core.Context, _ *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
			if sink := providers.ResponseHeaderSinkFrom(ctx); sink != nil && report != nil {
				sink(report)
			}
			msg := &core.AssistantMessage{StopReason: core.StopReasonStop, Timestamp: time.Now().UnixMilli()}
			s := core.NewAssistantMessageEventStream()
			go func() {
				s.Push(core.AssistantMessageEvent{Type: core.EventDone, Message: msg})
				s.End(msg)
			}()
			return s
		},
	}, "resp-headers-test")
	t.Cleanup(func() { ai.DefaultRegistry.UnregisterApiProviders("resp-headers-test") })

	model := &ai.Model{API: api, ID: "m", Provider: "p", MaxTokens: 1000}
	sess := &AgentSession{Agent: agent.NewAgent(agent.AgentOptions{SessionID: "rh-sess"})}
	t.Cleanup(func() { dropResponseHeaders("rh-sess") })
	call := func(ctx context.Context, sid string) int64 {
		// The provider stamps the message after the call started, as real
		// providers do; that stamp is what message_end matching uses.
		return agent.DefaultStreamFn(ctx)(model, core.Context{}, &core.SimpleStreamOptions{StreamOptions: core.StreamOptions{SessionID: sid}}).Result().Timestamp
	}
	waitNextMs := func() {
		for t0 := time.Now().UnixMilli(); time.Now().UnixMilli() == t0; {
			runtime.Gosched()
		}
	}

	if sess.ResponseHeadersFor(0) != nil {
		t.Fatal("headers before any call")
	}
	first := call(context.Background(), "rh-sess")
	if got := sess.ResponseHeadersFor(0); got["x-ratelimit-remaining"] != "5" {
		t.Fatalf("headers = %v", got)
	}
	waitNextMs()

	// A later call with no response: its own message gets nil, while a
	// message stamped during the first call still gets the first's headers
	// (event delivery may lag the agent loop).
	report = nil
	second := call(context.Background(), "rh-sess")
	if got := sess.ResponseHeadersFor(second); got != nil {
		t.Fatalf("second call headers = %v", got)
	}
	if got := sess.ResponseHeadersFor(first); got["x-ratelimit-remaining"] != "5" {
		t.Fatalf("first call headers lost: %v", got)
	}
	if sess.ResponseHeadersFor(first-1000) != nil {
		t.Fatal("message older than every slot matched")
	}

	// Side queries and calls without a session id are not recorded.
	report = map[string]string{"x": "1"}
	call(withSideQuery(context.Background()), "rh-sess")
	call(context.Background(), "")
	if got := sess.ResponseHeadersFor(0); got != nil {
		t.Fatalf("side query recorded: %v", got)
	}
	if _, ok := responseHeaderLogs.Load(""); ok {
		t.Fatal("empty session id recorded")
	}

	// The log is bounded.
	for i := 0; i < respHeaderSlots+3; i++ {
		call(context.Background(), "rh-sess")
	}
	v, _ := responseHeaderLogs.Load("rh-sess")
	if n := len(v.(*respHeaderLog).slots); n != respHeaderSlots {
		t.Fatalf("slots = %d", n)
	}

	var nilSess *AgentSession
	if nilSess.ResponseHeadersFor(0) != nil {
		t.Fatal("nil session")
	}
	if (&AgentSession{Agent: agent.NewAgent(agent.AgentOptions{SessionID: "none"})}).ResponseHeadersFor(0) != nil {
		t.Fatal("unknown session")
	}
}
