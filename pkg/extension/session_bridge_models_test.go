package extension

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

func TestSessionBridgeGetProviderModelIDs(t *testing.T) {
	if ids := (&SessionBridge{}).GetProviderModelIDs("anthropic"); ids != nil {
		t.Fatalf("no session: got %v", ids)
	}

	newSess := func(reg *models.ModelRegistry) *SessionBridge {
		return NewSessionBridge(session.NewAgentSession(session.AgentSessionOptions{
			Agent:          agent.NewAgent(agent.AgentOptions{InitialState: &agent.AgentState{}}),
			SessionStore:   store.InMemorySessionStore(),
			ResourceLoader: &stubResourceLoader{},
			Cwd:            t.TempDir(),
			ModelRegistry:  reg,
		}))
	}
	if ids := newSess(nil).GetProviderModelIDs("anthropic"); ids != nil {
		t.Fatalf("no registry: got %v", ids)
	}

	t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")
	dir := t.TempDir()
	reg := models.NewModelRegistry(auth.NewAuthStorage(filepath.Join(dir, "auth.json")), filepath.Join(dir, "models.json"))
	ids := newSess(reg).GetProviderModelIDs("anthropic")
	want := ai.GetModels("anthropic")
	if len(want) == 0 || !slices.Contains(ids, want[0].ID) {
		t.Fatalf("ids = %v, want to include %s", ids, want[0].ID)
	}
}
