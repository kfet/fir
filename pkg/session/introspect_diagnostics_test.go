package session

import (
	"path/filepath"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session/store"
)

// Introspect is the channel the doctor extension reads, so the stale-pin check
// has to actually reach it. The decision logic itself is table-tested in
// pkg/models; this covers the wiring only.
func TestIntrospectSurfacesStaleDefaultPin(t *testing.T) {
	const provider = "introspect-stale"
	for _, id := range []string{"probemodel-4-6", "probemodel-5"} {
		ai.RegisterModel(&ai.Model{
			ID: id, Name: id, API: ai.ApiAnthropicMessages, Provider: provider,
			BaseURL: "https://api.test.invalid", Input: []ai.InputModality{ai.InputText},
			ContextWindow: 1000, MaxTokens: 100,
		})
	}
	ai.RegisterProvider(&ai.RegisteredProvider{ID: provider, DisplayName: provider, DefaultModelID: "probemodel-5"})
	t.Cleanup(func() {
		ai.UnregisterProviderModels(provider)
		ai.UnregisterProvider(provider)
	})

	newSession := func(t *testing.T, settings config.Settings) *AgentSession {
		t.Helper()
		cwd, agentDir := t.TempDir(), t.TempDir()
		rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
		if err := rl.Reload(); err != nil {
			t.Fatal(err)
		}
		s := NewAgentSession(AgentSessionOptions{
			ResourceLoader:  rl,
			Agent:           agent.NewAgent(agent.AgentOptions{InitialState: &agent.AgentState{}}),
			SessionStore:    store.InMemorySessionStore(cwd),
			SettingsManager: config.NewInMemorySettingsManager(settings),
			ModelRegistry:   models.NewModelRegistry(auth.NewAuthStorage(filepath.Join(agentDir, "auth.json")), ""),
			Cwd:             cwd,
		})
		t.Cleanup(s.Close)
		return s
	}

	stale := newSession(t, config.Settings{DefaultProvider: provider, DefaultModel: "probemodel-4-6"})
	got := stale.Introspect(IntrospectOptions{Version: "test", Mode: "print"}).Diagnostics
	if len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want exactly one", got)
	}
	if got[0].Code != "stale_default_model" || got[0].Severity != "warning" {
		t.Errorf("unexpected diagnostic: %+v", got[0])
	}
	if got[0].Summary == "" || got[0].Remediation == "" {
		t.Errorf("a diagnostic must be actionable: %+v", got[0])
	}

	current := newSession(t, config.Settings{DefaultProvider: provider, DefaultModel: "probemodel-5"})
	if got := current.Introspect(IntrospectOptions{}).Diagnostics; len(got) != 0 {
		t.Errorf("a current pin must be silent, got %+v", got)
	}

	none := newSession(t, config.Settings{})
	if got := none.Introspect(IntrospectOptions{}).Diagnostics; len(got) != 0 {
		t.Errorf("no pin must be silent, got %+v", got)
	}
}
