package extproc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
)

// newSetupTestSession creates a minimal AgentSession for setup tests.
func newSetupTestSession(t *testing.T, cwd string) *core.AgentSession {
	t.Helper()
	sm := core.InMemorySessionManager()
	dummyModel := &ai.Model{
		Provider:      "test",
		ID:            "test-model",
		Name:          "Test",
		ContextWindow: 100000,
	}
	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: dummyModel},
	})
	return core.NewAgentSession(core.AgentSessionOptions{
		Agent:          a,
		SessionManager: sm,
		ResourceLoader: &stubResourceLoader{},
		Cwd:            cwd,
	})
}

// stubResourceLoader is a no-op core.ResourceLoader for tests.
type stubResourceLoader struct{}

func (s *stubResourceLoader) GetSkills() ([]core.Skill, []core.ResourceDiagnostic) { return nil, nil }
func (s *stubResourceLoader) GetPrompts() ([]core.PromptTemplate, []core.ResourceDiagnostic) {
	return nil, nil
}
func (s *stubResourceLoader) GetAgentsFiles() []core.AgentsFile { return nil }
func (s *stubResourceLoader) GetSystemPrompt() string           { return "" }
func (s *stubResourceLoader) GetAppendSystemPrompt() []string   { return nil }
func (s *stubResourceLoader) GetPathMetadata() map[string]core.PathMetadata {
	return nil
}
func (s *stubResourceLoader) ExtendResources(core.ResourceExtensionPaths) {}
func (s *stubResourceLoader) Reload() error                               { return nil }

// TestSetupHookToolCall verifies that OnToolCall consults extproc extensions
// via hook/tool_call when an extension is loaded through Setup().
func TestSetupHookToolCall(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	// Locate fir_ext SDK dir relative to this file.
	_, thisFile, _, _ := runtime.Caller(0)
	sdkDir, _ := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "sdk", "python"))
	if _, err := os.Stat(filepath.Join(sdkDir, "fir_ext.py")); err != nil {
		t.Fatalf("fir_ext.py not found at %s", sdkDir)
	}

	// Create a temp project with a hook extension that blocks "blocked:*" tools.
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import fir_ext

@fir_ext.on("hook/tool_call")
def on_hook(params, ctx):
    name = params.get("tool_name", "")
    if name.startswith("blocked:"):
        return {"block": True, "reason": "blocked by test ext"}
    return None

fir_ext.run(name="test-hook-ext")
`
	if err := os.WriteFile(filepath.Join(extDir, "hook_ext.py"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a temp trust store so we don't pollute ~/.config/fir/.
	trustPath := filepath.Join(t.TempDir(), "trusted.json")

	session := newSetupTestSession(t, projectDir)

	result, err := Setup(session, SetupOptions{
		ProjectDir:     projectDir,
		Cwd:            projectDir,
		TrustStorePath: trustPath,
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil SetupResult")
	}
	defer result.EmitSessionShutdown()

	hooks := session.Hooks()
	if hooks == nil || hooks.OnToolCall == nil {
		t.Fatal("expected OnToolCall hook")
	}

	// "blocked:..." should be blocked by the extension.
	block := hooks.OnToolCall("tc-1", "blocked:dangerous", map[string]any{})
	if block == nil {
		t.Error("expected block for blocked:dangerous")
	} else if block.Reason == "" {
		t.Error("expected non-empty reason from extproc hook")
	}

	// Normal tools should pass through.
	block = hooks.OnToolCall("tc-2", "bash", map[string]any{})
	if block != nil {
		t.Errorf("expected nil block for normal tool, got %+v", block)
	}
}
