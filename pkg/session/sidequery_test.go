package session

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kfet/agent"
	core "github.com/kfet/ai"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/resources"
	sessionpkg "github.com/kfet/fir/pkg/session/store"
)

// stubStream builds a session whose LLM calls are answered locally, and
// records the model/options each call was made with.
//
// It swaps agent.DefaultStreamFn — fir's own stream-fn factory, the seam the
// side-query marker travels through — and restores it on cleanup.
type stubStream struct {
	mu     sync.Mutex
	opts   []*core.SimpleStreamOptions
	usage  core.Usage
	answer string
}

func (s *stubStream) install(t *testing.T) {
	t.Helper()
	prev := agent.DefaultStreamFn
	t.Cleanup(func() { agent.DefaultStreamFn = prev })

	agent.DefaultStreamFn = func(ctx context.Context) agent.StreamFn {
		// Mirror the real factory in defaultstream.go: resolve the marker
		// once, at factory time, from the call's context.
		sideQuery := isSideQueryContext(ctx)
		return func(model *core.Model, _ core.Context, opts *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
			if sideQuery {
				opts = providers.ApplySideQueryOptions(model, opts)
			}
			s.mu.Lock()
			s.opts = append(s.opts, opts)
			s.mu.Unlock()

			msg := &core.AssistantMessage{
				Content:    []core.AssistantContent{core.NewTextContent(s.answer)},
				StopReason: core.StopReasonStop,
				Usage:      s.usage,
			}
			stream := core.NewAssistantMessageEventStream()
			go func() {
				stream.Push(core.AssistantMessageEvent{Type: core.EventDone, Message: msg})
				stream.End(msg)
			}()
			return stream
		}
	}
}

func (s *stubStream) lastOptions(t *testing.T) *core.SimpleStreamOptions {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.opts) == 0 {
		t.Fatal("no stream call was made")
	}
	return s.opts[len(s.opts)-1]
}

func newSideQueryTestSession(t *testing.T) *AgentSession {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	rl.Reload()

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "test system prompt",
			ThinkingLevel: "off",
			Model:         &ai.Model{ID: "claude-exec", Provider: ai.ProviderAnthropic, MaxTokens: 8192},
		},
		SessionID: "sess-1",
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return sessionpkg.ConvertToLLM(msgs)
		},
	})

	s := NewAgentSession(AgentSessionOptions{
		Agent:           a,
		SessionStore:    sessionpkg.NewSessionStore(cwd, filepath.Join(agentDir, "sessions")),
		SettingsManager: config.NewSettingsManager(cwd, agentDir),
		ResourceLoader:  rl,
		Cwd:             cwd,
	})
	t.Cleanup(s.Close)
	return s
}

func TestSideQueryContextMarker(t *testing.T) {
	if isSideQueryContext(context.Background()) {
		t.Error("plain context must not be marked")
	}
	//nolint:staticcheck // deliberately exercising the nil-context guard
	if isSideQueryContext(nil) {
		t.Error("nil context must not panic or be marked")
	}
	if !isSideQueryContext(withSideQuery(context.Background())) {
		t.Error("marked context not detected")
	}
}

// The marker only works because fir's own stream-fn factory is what the agent
// module resolves to. If defaultstream.go's init ever stops registering it —
// or fir starts setting a per-agent StreamFn, which resolveStreamFn prefers —
// every side query silently reverts to un-namespaced, short-retention,
// question-tail caching. Correct answers, quietly full price.
func TestDefaultStreamFnIsInstalled(t *testing.T) {
	if agent.DefaultStreamFn == nil {
		t.Fatal("agent.DefaultStreamFn is not installed; the side-query marker has no reader")
	}
	if agent.DefaultStreamFn(withSideQuery(context.Background())) == nil {
		t.Error("the factory returned no stream function")
	}
}

// The whole point of the ctx marker: a side query must reach fir's stream-fn
// factory carrying it, so the request gets its own cache namespace, long
// retention, and anchored breakpoint placement.
func TestSideQueryStream_SpecialisesStreamOptions(t *testing.T) {
	stub := &stubStream{answer: "advice"}
	stub.install(t)
	s := newSideQueryTestSession(t)

	if _, err := s.SideQueryStream(context.Background(), "what now?", nil, nil); err != nil {
		t.Fatalf("SideQueryStream: %v", err)
	}

	opts := stub.lastOptions(t)
	if want := providers.SideQuerySessionID("sess-1", "claude-exec"); opts.SessionID != want {
		t.Errorf("SessionID = %q, want %q", opts.SessionID, want)
	}
	if opts.CacheRetention != ai.CacheLong {
		t.Errorf("CacheRetention = %q, want long", opts.CacheRetention)
	}
	if v, _ := opts.Metadata[providers.MetadataSideQuery].(bool); !v {
		t.Errorf("side-query marker missing from metadata: %v", opts.Metadata)
	}
}

// An ordinary agent turn must be untouched — no namespacing, no forced 1h
// retention (which would double every executor cache write).
func TestExecutorPrompt_StreamOptionsUnchanged(t *testing.T) {
	stub := &stubStream{answer: "hello"}
	stub.install(t)
	s := newSideQueryTestSession(t)

	msgs := []agent.AgentMessage{agent.NewAgentMessage(ai.NewUserMsg("hi", 0))}
	if _, _, err := s.Agent.SimplePromptStream(context.Background(), msgs, nil, nil); err != nil {
		t.Fatalf("SimplePromptStream: %v", err)
	}

	opts := stub.lastOptions(t)
	if opts.SessionID != "sess-1" {
		t.Errorf("executor SessionID = %q, want the raw session id", opts.SessionID)
	}
	if opts.CacheRetention == ai.CacheLong {
		t.Error("executor path must not be forced to long retention")
	}
	if _, ok := opts.Metadata[providers.MetadataSideQuery]; ok {
		t.Error("executor path must not carry the side-query marker")
	}
}

// Usage must reach the caller — it is the only way the advisor path's cache
// behaviour is observable from the extension layer.
func TestSideQueryStream_ReportsUsage(t *testing.T) {
	stub := &stubStream{
		answer: "advice",
		usage:  core.Usage{Input: 120, Output: 45, CacheRead: 48000, CacheWrite: 610},
	}
	stub.install(t)
	s := newSideQueryTestSession(t)

	var usageDeltas []SideQueryDelta
	res, err := s.SideQueryStream(context.Background(), "q", nil, func(d SideQueryDelta) {
		if d.Type == "usage" {
			usageDeltas = append(usageDeltas, d)
		}
	})
	if err != nil {
		t.Fatalf("SideQueryStream: %v", err)
	}

	if res.TokensIn != 120 || res.TokensOut != 45 || res.CacheRead != 48000 || res.CacheWrite != 610 {
		t.Errorf("result usage = %+v", res)
	}
	if len(usageDeltas) != 1 {
		t.Fatalf("expected exactly 1 usage delta, got %d", len(usageDeltas))
	}
	d := usageDeltas[0]
	if d.TokensIn != 120 || d.TokensOut != 45 || d.CacheRead != 48000 || d.CacheWrite != 610 {
		t.Errorf("usage delta = %+v", d)
	}
}

// A cache-only turn (everything read from cache, nothing new) still reports —
// the old guard emitted the delta only when Output > 0.
func TestSideQueryStream_ReportsCacheOnlyUsage(t *testing.T) {
	stub := &stubStream{answer: "advice", usage: core.Usage{CacheRead: 51200}}
	stub.install(t)
	s := newSideQueryTestSession(t)

	var got []SideQueryDelta
	res, err := s.SideQueryStream(context.Background(), "q", nil, func(d SideQueryDelta) {
		if d.Type == "usage" {
			got = append(got, d)
		}
	})
	if err != nil {
		t.Fatalf("SideQueryStream: %v", err)
	}
	if res.CacheRead != 51200 {
		t.Errorf("CacheRead = %d, want 51200", res.CacheRead)
	}
	if len(got) != 1 || got[0].CacheRead != 51200 {
		t.Errorf("usage deltas = %+v", got)
	}
}
