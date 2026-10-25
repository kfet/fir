package extension

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

const headersProbeScript = `#!/usr/bin/env python3
# ---
# name: headers-probe
# ---
import json, os
import fir_ext

LOG = os.environ["PROBE_LOG"]

def record(name, params):
    if name == "message_end" and params.get("role") != "assistant":
        return
    with open(LOG, "a") as fh:
        fh.write(name + " " + json.dumps(params.get("response_headers") or {}) + "\n")

for _event in ("message_end", "provider_error"):
    fir_ext.on(_event)(lambda params, ctx, _n=_event: record(_n, params))

fir_ext.run(name="headers-probe")
`

// TestSetupForwardsResponseHeaders drives real turns through fir's default
// stream function and checks that the headers the provider transport saw
// reach extensions on message_end and provider_error, redacted.
func TestSetupForwardsResponseHeaders(t *testing.T) {
	requirePython(t)
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "headers_probe.py"), []byte(headersProbeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(projectDir, "events.log")
	t.Setenv("PROBE_LOG", logPath)

	const api = "headers-e2e-api"
	var calls atomic.Int32 // first call succeeds, later ones fail
	ai.DefaultRegistry.RegisterApiProvider(&ai.ApiProvider{
		Api: api,
		StreamSimple: func(ctx context.Context, m *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			if sink := providers.ResponseHeaderSinkFrom(ctx); sink != nil {
				sink(providers.RedactHeaderMap(map[string]string{"X-Ratelimit-Remaining": "4", "Set-Cookie": "s"}))
			}
			msg := &ai.AssistantMessage{
				Role: ai.RoleAssistant, API: m.API, Provider: m.Provider, Model: m.ID,
				Content:    []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}},
				StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
			}
			ev := ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg}
			if calls.Add(1) > 1 {
				msg.StopReason, msg.ErrorMessage = ai.StopReasonError, "400 bad request"
				ev = ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: msg}
			}
			s := ai.NewAssistantMessageEventStream()
			go func() {
				s.Push(ev)
				s.End(msg)
			}()
			return s
		},
	}, "headers-e2e")
	t.Cleanup(func() { ai.DefaultRegistry.UnregisterApiProviders("headers-e2e") })

	model := &ai.Model{API: api, Provider: "test", ID: "m", Name: "M", ContextWindow: 100000, MaxTokens: 1000}
	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: model},
		SessionID:    "headers-e2e-session",
		GetAPIKey:    func(string) (string, error) { return "k", nil },
	})
	asession := session.NewAgentSession(session.AgentSessionOptions{
		Agent: a, SessionStore: store.InMemorySessionStore(), ResourceLoader: &stubResourceLoader{}, Cwd: projectDir,
	})
	t.Cleanup(asession.Close)
	result := setupLifecycleProbe(t, asession, projectDir, "acp")
	t.Cleanup(result.EmitSessionShutdown)

	if err := asession.Prompt("hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	a.WaitForIdle()
	if err := asession.Prompt("again"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	want := []string{
		`message_end {"x-ratelimit-remaining": "4"}`,
		`provider_error {"x-ratelimit-remaining": "4"}`,
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		got := string(data)
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(got, w)
		}
		if ok {
			if strings.Contains(got, "cookie") {
				t.Fatalf("cookie leaked: %s", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log = %q, want lines %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
