package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/extension"
)

func TestSandboxExtensionRegisters(t *testing.T) {
	factories := extension.RegisteredFactories()

	found := false
	for _, f := range factories {
		if f.Name == "sandbox" {
			found = true
			break
		}
	}

	if !found {
		t.Error("sandbox extension not found in registry")
	}
}

func TestSandboxExtensionLoads(t *testing.T) {
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Should register the no-sandbox flag
	flags := runner.GetFlags()
	if _, ok := flags["no-sandbox"]; !ok {
		t.Error("expected 'no-sandbox' flag to be registered")
	}

	// Should register the /sandbox command
	commands := runner.GetCommands()
	if _, ok := commands["sandbox"]; !ok {
		t.Error("expected 'sandbox' command to be registered")
	}

	// Should have tool_call handler
	if !runner.HasHandlers("tool_call") {
		t.Error("expected tool_call handler from sandbox extension")
	}

	// Should have session_start handler
	if !runner.HasHandlers("session_start") {
		t.Error("expected session_start handler from sandbox extension")
	}
}

func TestSandboxToolCallBlocking(t *testing.T) {
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Simulate session_start to enable sandbox
	_ = runner.EmitSessionStart()

	// Try accessing a denied path
	home, _ := os.UserHomeDir()
	sshPath := filepath.Join(home, ".ssh")

	result := runner.EmitToolCall("tc1", "bash", map[string]any{
		"command": "cat " + sshPath + "/id_rsa",
	})

	if result == nil {
		t.Fatal("expected tool call to be blocked")
	}
	if !result.Block {
		t.Error("expected block=true")
	}
	if result.Reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestSandboxToolCallAllowed(t *testing.T) {
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Enable sandbox
	_ = runner.EmitSessionStart()

	// Normal command should not be blocked
	result := runner.EmitToolCall("tc2", "bash", map[string]any{
		"command": "echo hello",
	})

	if result != nil {
		t.Errorf("expected nil (allowed), got block=%v reason=%q", result.Block, result.Reason)
	}
}

func TestSandboxDisabledViaFlag(t *testing.T) {
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Set flag to disable
	runner.SetFlagValue("no-sandbox", true)

	// Emit session_start
	_ = runner.EmitSessionStart()

	// Even accessing denied paths should not be blocked
	home, _ := os.UserHomeDir()
	sshPath := filepath.Join(home, ".ssh")

	result := runner.EmitToolCall("tc3", "bash", map[string]any{
		"command": "cat " + sshPath + "/id_rsa",
	})

	if result != nil {
		t.Error("expected nil (sandbox disabled), got block")
	}
}

func TestLoadConfig(t *testing.T) {
	// Create temp directory with sandbox config
	tmpDir := t.TempDir()
	piDir := filepath.Join(tmpDir, ".pi")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}

	config := SandboxConfig{
		Enabled: boolPtr(true),
		Network: &NetworkConfig{
			AllowedDomains: []string{"example.com"},
		},
		Filesystem: &FSConfig{
			DenyRead:   []string{"/secret"},
			AllowWrite: []string{"."},
		},
	}

	data, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(piDir, "sandbox.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded := loadConfig(tmpDir)

	if loaded.Enabled == nil || !*loaded.Enabled {
		t.Error("expected enabled=true")
	}
	if loaded.Network == nil || len(loaded.Network.AllowedDomains) != 1 {
		t.Error("expected 1 allowed domain from project config")
	}
	if loaded.Network.AllowedDomains[0] != "example.com" {
		t.Errorf("expected example.com, got %v", loaded.Network.AllowedDomains[0])
	}
}

func TestMergeConfig(t *testing.T) {
	base := SandboxConfig{
		Enabled: boolPtr(true),
		Network: &NetworkConfig{
			AllowedDomains: []string{"a.com", "b.com"},
		},
	}

	override := SandboxConfig{
		Network: &NetworkConfig{
			AllowedDomains: []string{"c.com"},
		},
		Filesystem: &FSConfig{
			DenyRead: []string{"/secret"},
		},
	}

	result := mergeConfig(base, override)

	if result.Enabled == nil || !*result.Enabled {
		t.Error("enabled should be preserved from base")
	}
	if len(result.Network.AllowedDomains) != 1 || result.Network.AllowedDomains[0] != "c.com" {
		t.Error("network should be overridden")
	}
	if result.Filesystem == nil || len(result.Filesystem.DenyRead) != 1 {
		t.Error("filesystem should be added from override")
	}
}
