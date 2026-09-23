package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session/store"
)

const gateVendorErr = "400 Claude Code 2.1.112 does not support this model; version 2.1.251 or newer is required. Run 'claude update', or update the Claude desktop app, then try again. (request-id: req_x)"

// Wiring only: the classifier and predicate are table-tested in pkg/models.
func TestClientVersionGateWiring(t *testing.T) {
	const provider = "gate-wiring"
	for id, hdr := range map[string]map[string]string{
		"oauth-model":  {anthropicOAuthHeader: "oauth-2025-04-20"},
		"apikey-model": nil,
	} {
		ai.RegisterModel(&ai.Model{
			ID: id, Name: id, API: ai.ApiAnthropicMessages, Provider: provider,
			BaseURL: "https://api.test.invalid", Input: []ai.InputModality{ai.InputText},
			ContextWindow: 1000, MaxTokens: 100, Headers: hdr,
		})
	}
	t.Cleanup(func() { ai.UnregisterProviderModels(provider) })

	cwd, agentDir := t.TempDir(), t.TempDir()
	logPath := models.DoctorLogPath(agentDir)
	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	if err := rl.Reload(); err != nil {
		t.Fatal(err)
	}
	reg := models.NewModelRegistry(auth.NewAuthStorage(filepath.Join(agentDir, "auth.json")), "")
	s := NewAgentSession(AgentSessionOptions{
		ResourceLoader:  rl,
		Agent:           agent.NewAgent(agent.AgentOptions{InitialState: &agent.AgentState{}}),
		SessionStore:    store.InMemorySessionStore(cwd),
		SettingsManager: config.NewInMemorySettingsManager(config.Settings{}),
		ModelRegistry:   reg,
		Cwd:             cwd,
		DoctorLogPath:   logPath,
	})
	t.Cleanup(s.Close)

	errMsg := func(model, text string) *ai.AssistantMessage {
		return &ai.AssistantMessage{Provider: provider, Model: model, StopReason: ai.StopReasonError, ErrorMessage: text}
	}

	// API-key model, same text: not an OAuth request, not classified.
	m := errMsg("apikey-model", gateVendorErr)
	s.classifyClientVersionGate(m)
	if m.ErrorMessage != gateVendorErr {
		t.Fatalf("non-OAuth model classified: %q", m.ErrorMessage)
	}
	// OAuth model, ordinary error: untouched.
	m = errMsg("oauth-model", "429 rate limited (rate_limit_error)")
	s.classifyClientVersionGate(m)
	if m.ErrorMessage != "429 rate limited (rate_limit_error)" {
		t.Fatalf("ordinary error rewritten: %q", m.ErrorMessage)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("nothing should have been recorded yet")
	}

	// Through the real event path: message_end rewrites before emit.
	am := agent.NewAgentMessage(ai.NewAssistantMsg(*errMsg("oauth-model", gateVendorErr)))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventMessageEnd, Message: &am})
	if got := am.AsAssistant().ErrorMessage; !models.IsGateErrorMessage(got) {
		t.Fatalf("message_end not classified: %q", got)
	}

	// OAuth model, gate signature: rewritten and recorded, once.
	for i := 0; i < 3; i++ {
		m = errMsg("oauth-model", gateVendorErr)
		s.classifyClientVersionGate(m)
		if !models.IsGateErrorMessage(m.ErrorMessage) || !strings.Contains(m.ErrorMessage, "2.1.112") {
			t.Fatalf("not rewritten: %q", m.ErrorMessage)
		}
		before := m.ErrorMessage
		s.classifyClientVersionGate(m) // second pass must not double-wrap
		if m.ErrorMessage != before {
			t.Fatal("double-wrapped")
		}
	}
	recs, err := models.ReadGateRecords(logPath)
	if err != nil || len(recs) != 1 {
		t.Fatalf("want 1 deduped record, got %d (%v)", len(recs), err)
	}
	r := recs[0]
	if r.Pin != "2.1.112" || r.Required != "2.1.251" || r.Model != "oauth-model" || r.PinSource != models.PinSourceEmbeddedFloor ||
		r.VendorError != gateVendorErr || r.Host == "" || r.Timestamp == 0 || !r.PinMismatch {
		t.Fatalf("record: %+v", r)
	}

	// The effective pin (embedded floor) is already past 2.1.112, so this
	// record is resolved: no diagnostic.
	if d := s.clientVersionGateDiagnostics(); len(d) != 0 {
		t.Fatalf("resolved gate still warns: %+v", d)
	}
	// A record AT the effective pin still warns.
	eff := reg.ClientVersion(models.ClientVersionKeyClaudeCode)
	if err := models.AppendGateRecord(logPath, models.GateRecord{Key: models.ClientVersionKeyClaudeCode, Pin: eff, Required: "999.0", Model: "oauth-model", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	d := s.Introspect(IntrospectOptions{}).Diagnostics
	if len(d) != 1 || d[0].Code != "client_version_gate" || !strings.Contains(d[0].Summary, eff) || strings.Contains(d[0].Summary, "\n") {
		t.Fatalf("diagnostics: %+v", d)
	}
}

func TestClientVersionGateDisabledWithoutLog(t *testing.T) {
	s := &AgentSession{}
	if d := s.clientVersionGateDiagnostics(); d != nil {
		t.Fatal(d)
	}
	s.classifyClientVersionGate(nil)
}
