package claudeusage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/extension"
)

func TestClaudeUsageExtensionRegisters(t *testing.T) {
	factories := extension.RegisteredFactories()
	found := false
	for _, f := range factories {
		if f.Name == extName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s extension not found in registry", extName)
	}
}

func TestClaudeUsageExtensionLoads(t *testing.T) {
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	if !runner.HasHandlers("session_start") {
		t.Error("expected session_start handler")
	}
	if !runner.HasHandlers("agent_end") {
		t.Error("expected agent_end handler")
	}

	cmds := runner.GetCommands()
	if _, ok := cmds[commandName]; !ok {
		t.Errorf("expected /%s command", commandName)
	}
}

// --- mockUI ---

type mockUI struct {
	statuses   map[string]string
	lastNotify string
	lastLevel  string
}

func newMockUI() *mockUI {
	return &mockUI{statuses: make(map[string]string)}
}

func (m *mockUI) Select(string, []string) (string, error)     { return "", nil }
func (m *mockUI) Confirm(string, string) (bool, error)        { return false, nil }
func (m *mockUI) Input(string, string) (string, error)        { return "", nil }
func (m *mockUI) Notify(message string, level string)         { m.lastNotify = message; m.lastLevel = level }
func (m *mockUI) SetStatus(key string, text string)           { m.statuses[key] = text }
func (m *mockUI) SetWidget(string, []string)                  {}
func (m *mockUI) ClearWidget(string)                          {}

// --- helpers to create auth storage with OAuth creds ---

func makeAuthStorage(t *testing.T, oauthToken string) *core.AuthStorage {
	t.Helper()
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")

	if oauthToken != "" {
		data := map[string]core.AuthCredential{
			providerID: {
				Type:    core.CredentialTypeOAuth,
				Access:  oauthToken,
				Refresh: "refresh-token",
				Expires: 9999999999999,
			},
		}
		raw, _ := json.Marshal(data)
		os.WriteFile(authPath, raw, 0600)
	}

	return core.NewAuthStorage(authPath)
}

func makeModelRegistry(t *testing.T, oauthToken string) *core.ModelRegistry {
	t.Helper()
	as := makeAuthStorage(t, oauthToken)
	return core.NewModelRegistry(as, "")
}

func makeRunner(t *testing.T, oauthToken string) (*extension.Runner, *mockUI) {
	t.Helper()

	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadEnabled([]string{extName}); err != nil {
		t.Fatal(err)
	}

	ui := newMockUI()
	runner.SetUIContext(ui)

	mr := makeModelRegistry(t, oauthToken)
	runner.BindActions(&extension.Actions{
		ModelRegistry: func() *core.ModelRegistry { return mr },
	})

	return runner, ui
}

// --- session_start tests ---

func TestSessionStartNoOAuth(t *testing.T) {
	runner, ui := makeRunner(t, "")

	_ = runner.EmitSessionStart()

	// No OAuth creds → status cleared (empty), not an error
	status := ui.statuses[statusKey]
	if status != "" {
		t.Errorf("expected empty status with no OAuth, got %q", status)
	}
}

func TestSessionStartWithOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(usageData{
			FiveHour: &windowData{Utilization: 30},
			SevenDay: &windowData{Utilization: 10},
		})
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	runner, ui := makeRunner(t, "valid-token")

	_ = runner.EmitSessionStart()

	status := ui.statuses[statusKey]
	if !strings.Contains(status, "◎") {
		t.Errorf("status should contain ◎, got %q", status)
	}
	if !strings.Contains(status, "30%") {
		t.Errorf("status should contain 30%%, got %q", status)
	}
}

func TestSessionStartUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	runner, ui := makeRunner(t, "expired-token")

	_ = runner.EmitSessionStart()

	status := ui.statuses[statusKey]
	if !strings.Contains(status, "expired") || !strings.Contains(status, "/login") {
		t.Errorf("expected expired/login prompt, got %q", status)
	}
}

// --- agent_end tests ---

func TestAgentEndUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(usageData{
			FiveHour: &windowData{Utilization: 45},
			SevenDay: &windowData{Utilization: 20},
		})
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	runner, ui := makeRunner(t, "valid-token")

	_ = runner.EmitAgentEnd(nil)

	status := ui.statuses[statusKey]
	if !strings.Contains(status, "45%") {
		t.Errorf("status should contain 45%%, got %q", status)
	}
}

// --- /claude-usage command tests ---

func TestCommandShowNoOAuth(t *testing.T) {
	ui := newMockUI()
	mr := makeModelRegistry(t, "")
	ctx := &testCommandContext{ui: ui, modelRegistry: mr}

	err := handleShow(ctx)
	if err != nil {
		t.Fatalf("handleShow() error: %v", err)
	}
	if !strings.Contains(ui.lastNotify, "/login") {
		t.Errorf("should prompt to /login, got %q", ui.lastNotify)
	}
	if ui.lastLevel != "warning" {
		t.Errorf("level = %q, want warning", ui.lastLevel)
	}
}

func TestCommandShowSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(usageData{
			FiveHour: &windowData{Utilization: 60},
			SevenDay: &windowData{Utilization: 25},
		})
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	ui := newMockUI()
	mr := makeModelRegistry(t, "valid-token")
	ctx := &testCommandContext{ui: ui, modelRegistry: mr}

	err := handleShow(ctx)
	if err != nil {
		t.Fatalf("handleShow() error: %v", err)
	}
	if !strings.Contains(ui.lastNotify, "5-Hour") || !strings.Contains(ui.lastNotify, "7-Day") {
		t.Errorf("should show both windows, got %q", ui.lastNotify)
	}
	if ui.lastLevel != "info" {
		t.Errorf("level = %q, want info", ui.lastLevel)
	}
	// Should also update status
	if !strings.Contains(ui.statuses[statusKey], "60%") {
		t.Errorf("status should contain 60%%, got %q", ui.statuses[statusKey])
	}
}

func TestCommandShowUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	setUsageEndpoint(t, server.URL)

	ui := newMockUI()
	mr := makeModelRegistry(t, "bad-token")
	ctx := &testCommandContext{ui: ui, modelRegistry: mr}

	err := handleShow(ctx)
	if err != nil {
		t.Fatalf("handleShow() error: %v", err)
	}
	if !strings.Contains(ui.lastNotify, "expired") || !strings.Contains(ui.lastNotify, "/login") {
		t.Errorf("should mention expired and /login, got %q", ui.lastNotify)
	}
}

func TestCommandShowNilModelRegistry(t *testing.T) {
	ui := newMockUI()
	ctx := &testCommandContext{ui: ui, modelRegistry: nil}

	err := handleShow(ctx)
	if err != nil {
		t.Fatalf("handleShow() error: %v", err)
	}
	if ui.lastLevel != "error" {
		t.Errorf("level = %q, want error", ui.lastLevel)
	}
}

func TestCommandDispatch(t *testing.T) {
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadEnabled([]string{extName}); err != nil {
		t.Fatal(err)
	}

	ui := newMockUI()
	runner.SetUIContext(ui)

	mr := makeModelRegistry(t, "")
	runner.BindActions(&extension.Actions{
		ModelRegistry: func() *core.ModelRegistry { return mr },
	})

	found, err := runner.ExecuteCommand(commandName, "")
	if !found {
		t.Fatalf("/%s command not found", commandName)
	}
	if err != nil {
		t.Fatalf("/%s error: %v", commandName, err)
	}
	if ui.lastNotify == "" {
		t.Errorf("expected notification from /%s", commandName)
	}
}

// --- testCommandContext ---

type testCommandContext struct {
	ui            extension.UIContext
	modelRegistry *core.ModelRegistry
}

func (c *testCommandContext) UI() extension.UIContext {
	if c.ui != nil {
		return c.ui
	}
	return newMockUI()
}

func (c *testCommandContext) HasUI() bool                          { return true }
func (c *testCommandContext) Cwd() string                          { return "." }
func (c *testCommandContext) SessionManager() *core.SessionManager { return nil }
func (c *testCommandContext) ModelRegistry() *core.ModelRegistry    { return c.modelRegistry }
func (c *testCommandContext) Model() *ai.Model                     { return nil }
func (c *testCommandContext) IsIdle() bool                          { return true }
func (c *testCommandContext) Abort()                                {}
func (c *testCommandContext) HasPendingMessages() bool              { return false }
func (c *testCommandContext) Shutdown()                             {}
func (c *testCommandContext) GetContextUsage() *core.ContextUsage   { return nil }
func (c *testCommandContext) GetSystemPrompt() string               { return "" }
func (c *testCommandContext) WaitForIdle()                          {}
func (c *testCommandContext) NewSession() (bool, error)             { return false, nil }
func (c *testCommandContext) Fork(string) (bool, error)             { return false, nil }
func (c *testCommandContext) Reload() error                         { return nil }
