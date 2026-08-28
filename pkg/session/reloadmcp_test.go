package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	sessionpkg "github.com/kfet/fir/pkg/session/store"
)

// newSessionWithAuthPath builds a session whose credential store is a known
// file, so a test can write to it the way a second process would.
func newSessionWithAuthPath(t *testing.T) (*AgentSession, string, string) {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	authPath := filepath.Join(agentDir, "auth.json")

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir})
	rl.Reload()

	sess := NewAgentSession(AgentSessionOptions{
		Agent: agent.NewAgent(agent.AgentOptions{
			InitialState: &agent.AgentState{SystemPrompt: "test", ThinkingLevel: "off"},
			ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
				return sessionpkg.ConvertToLLM(msgs)
			},
		}),
		SessionStore:    sessionpkg.InMemorySessionStore(cwd),
		SettingsManager: config.NewSettingsManager(cwd, agentDir),
		ResourceLoader:  rl,
		ModelRegistry:   models.NewModelRegistry(auth.NewAuthStorage(authPath), ""),
		Cwd:             cwd,
	})
	t.Cleanup(func() { sess.Close() })
	return sess, cwd, authPath
}

// A reload that brings up the *first* MCP manager must re-read the credential
// store. Manager.Reload covers the existing-manager branch; this branch
// inherits the session's AuthStorage, whose in-memory view was loaded at
// session start and would otherwise miss an out-of-band `fir mcp login`.
func TestReloadMCP_NilManagerRereadsCredentials(t *testing.T) {
	sess, cwd, authPath := newSessionWithAuthPath(t)
	storage := sess.ModelRegistryRef().AuthStorage()
	if storage.Get("mcp:demo") != nil {
		t.Fatal("fresh storage should hold no MCP credential")
	}

	// A second process mints a token into the same file.
	other := auth.NewAuthStorage(authPath)
	if err := other.Set("mcp:demo", auth.AuthCredential{
		Type:   auth.CredentialTypeOAuth,
		Access: "token-from-another-process",
	}); err != nil {
		t.Fatalf("out-of-band write: %v", err)
	}

	// The first MCP server config appears, so the reload creates the manager.
	if err := os.MkdirAll(filepath.Join(cwd, ".fir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcpServers":{"demo":{"command":"/bin/true"}}}`
	if err := os.WriteFile(filepath.Join(cwd, ".fir", "mcp.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	var mgr *mcp.Manager
	t.Cleanup(func() {
		if mgr != nil {
			_ = mgr.Close()
		}
	})
	if err := ReloadMCP(context.Background(), &mgr, sess, cwd, "", nil); err != nil {
		t.Fatalf("ReloadMCP: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected a manager to be created")
	}
	if cred := storage.Get("mcp:demo"); cred == nil || cred.Access != "token-from-another-process" {
		t.Errorf("reload must re-read credentials written by another process, got %+v", cred)
	}
}
