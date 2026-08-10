package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
)

// knownSet builds a Known predicate over "provider/id" pairs.
func knownSet(ids ...string) func(string, string) bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(provider, modelID string) bool { return set[provider+"/"+modelID] }
}

func TestCheckStaleDefaultPin(t *testing.T) {
	const (
		opus46 = "anthropic/claude-opus-4-6"
		opus5  = "anthropic/claude-opus-5"
	)

	tests := []struct {
		name string
		in   StaleDefaultPinInput
		warn bool
	}{
		// --- the case this exists for ---
		{
			name: "older generation of the same line warns",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-4-6", Current: "claude-opus-5",
				Known: knownSet(opus46, opus5),
			},
			warn: true,
		},
		{
			name: "older minor of the same generation warns",
			in: StaleDefaultPinInput{
				Provider: "openai", Pinned: "gpt-5.4", Current: "gpt-5.5",
				Known: knownSet("openai/gpt-5.4", "openai/gpt-5.5"),
			},
			warn: true,
		},
		{
			name: "older naming scheme of the same line warns",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-3-5-sonnet-20241022", Current: "claude-sonnet-4-6",
				Known: knownSet("anthropic/claude-3-5-sonnet-20241022", "anthropic/claude-sonnet-4-6"),
			},
			warn: true,
		},
		{
			name: "bedrock ids compare on the model part",
			in: StaleDefaultPinInput{
				Provider: "amazon-bedrock",
				Pinned:   "us.anthropic.claude-opus-4-6-v1:0", Current: "us.anthropic.claude-opus-5-v1:0",
				Known: knownSet("amazon-bedrock/us.anthropic.claude-opus-4-6-v1:0", "amazon-bedrock/us.anthropic.claude-opus-5-v1:0"),
			},
			warn: true,
		},

		// --- everything below must stay SILENT ---
		{
			name: "no pin",
			in:   StaleDefaultPinInput{Current: "claude-opus-5", Known: knownSet(opus5)},
		},
		{
			name: "provider pinned but model empty",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Current: "claude-opus-5", Known: knownSet(opus5),
			},
		},
		{
			name: "model pinned but provider empty",
			in: StaleDefaultPinInput{
				Pinned: "claude-opus-4-6", Current: "claude-opus-5", Known: knownSet(opus46, opus5),
			},
		},
		{
			name: "provider has no default",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-4-6", Known: knownSet(opus46),
			},
		},
		{
			name: "pin is the current default",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-5", Current: "claude-opus-5",
				Known: knownSet(opus5),
			},
		},
		{
			name: "pinned model unknown to fir (custom models.json entry)",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-4-6-internal", Current: "claude-opus-5",
				Known: knownSet(opus5),
			},
		},
		{
			name: "provider default unknown to fir",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-4-6", Current: "claude-opus-5",
				Known: knownSet(opus46),
			},
		},
		{
			name: "nil Known predicate",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-4-6", Current: "claude-opus-5",
			},
		},
		{
			name: "different product line is a deliberate cost choice",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-sonnet-4-6", Current: "claude-opus-5",
				Known: knownSet("anthropic/claude-sonnet-4-6", opus5),
			},
		},
		{
			name: "pin is newer than the provider default",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-5", Current: "claude-opus-4-6",
				Known: knownSet(opus5, opus46),
			},
		},
		{
			name: "dated snapshot of the current default",
			in: StaleDefaultPinInput{
				Provider: "anthropic", Pinned: "claude-opus-5-20260115", Current: "claude-opus-5",
				Known: knownSet("anthropic/claude-opus-5-20260115", opus5),
			},
		},
		{
			name: "preview spelling of the current default",
			in: StaleDefaultPinInput{
				Provider: "google", Pinned: "gemini-3.1-pro", Current: "gemini-3.1-pro-preview",
				Known: knownSet("google/gemini-3.1-pro", "google/gemini-3.1-pro-preview"),
			},
		},
		{
			name: "unversioned ids cannot be ordered",
			in: StaleDefaultPinInput{
				Provider: "moonshot", Pinned: "kimi-k2-thinking", Current: "kimi-k3",
				Known: knownSet("moonshot/kimi-k2-thinking", "moonshot/kimi-k3"),
			},
		},
		{
			name: "unversioned pin against versioned default",
			in: StaleDefaultPinInput{
				Provider: "deepseek", Pinned: "deepseek-r1", Current: "deepseek-v4",
				Known: knownSet("deepseek/deepseek-r1", "deepseek/deepseek-v4"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckStaleDefaultPin(tt.in)
			if tt.warn != (got != nil) {
				t.Fatalf("CheckStaleDefaultPin() = %+v, want warn=%v", got, tt.warn)
			}
			if got == nil {
				return
			}
			if got.Pinned != tt.in.Pinned || got.Current != tt.in.Current || got.Provider != tt.in.Provider {
				t.Errorf("warning does not echo the inputs: %+v", got)
			}
		})
	}
}

func TestStaleDefaultPinMessages(t *testing.T) {
	p := &StaleDefaultPin{
		Provider: "anthropic",
		Pinned:   "claude-opus-4-6",
		Current:  "claude-opus-5",
		Scope:    "global",
		Path:     "/home/x/.config/fir/settings.json",
	}
	// The message must name the pin, what it shadows, the file, and the fix.
	summary, remediation := p.Summary(), p.Remediation()
	for _, want := range []string{"claude-opus-4-6", "claude-opus-5"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q missing %q", summary, want)
		}
	}
	for _, want := range []string{p.Path, "defaultModel", "claude-opus-5"} {
		if !strings.Contains(remediation, want) {
			t.Errorf("remediation %q missing %q", remediation, want)
		}
	}

	// With no file-backed storage the scope still has to be nameable.
	noPath := &StaleDefaultPin{Provider: "anthropic", Pinned: "a", Current: "b", Scope: "project"}
	if !strings.Contains(noPath.Remediation(), "project settings.json") {
		t.Errorf("remediation without a path must name the scope: %q", noPath.Remediation())
	}
}

// TestStaleDefaultPinFor exercises the wiring: effective pin (project file
// over global file), the provider's current default, and the reported path.
func TestStaleDefaultPinFor(t *testing.T) {
	const provider = "test-stale-pin"
	for _, id := range []string{"testmodel-4-6", "testmodel-5"} {
		ai.RegisterModel(&ai.Model{
			ID: id, Name: id, API: ai.ApiAnthropicMessages, Provider: provider,
			BaseURL: "https://api.test.com", Input: []ai.InputModality{ai.InputText},
			ContextWindow: 1000, MaxTokens: 100,
		})
	}
	ai.RegisterProvider(&ai.RegisteredProvider{ID: provider, DisplayName: provider, DefaultModelID: "testmodel-5"})
	t.Cleanup(func() {
		ai.UnregisterProviderModels(provider)
		ai.UnregisterProvider(provider)
	})

	dir := t.TempDir()
	cwd := filepath.Join(dir, "project")
	if err := os.MkdirAll(filepath.Join(cwd, config.ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(scope, body string) {
		path := filepath.Join(dir, "settings.json")
		if scope == config.ScopeProject {
			path = filepath.Join(cwd, config.ConfigDirName, "settings.json")
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry, _ := setupTestModelRegistry(t, "")

	// Global pin on the older model: warns, naming the global file.
	write(config.ScopeGlobal, `{"defaultProvider":"`+provider+`","defaultModel":"testmodel-4-6"}`)
	pin := StaleDefaultPinFor(config.NewSettingsManager(cwd, dir), registry)
	if pin == nil {
		t.Fatal("expected a warning for a pin on the older model")
	}
	if pin.Scope != config.ScopeGlobal || pin.Path != filepath.Join(dir, "settings.json") {
		t.Errorf("wrong provenance: scope=%q path=%q", pin.Scope, pin.Path)
	}

	// A project pin wins, and here it is current: silent.
	write(config.ScopeProject, `{"defaultModel":"testmodel-5"}`)
	if pin := StaleDefaultPinFor(config.NewSettingsManager(cwd, dir), registry); pin != nil {
		t.Fatalf("project pin on the current default must be silent, got %+v", pin)
	}

	// A stale project pin is reported against the project file.
	write(config.ScopeProject, `{"defaultModel":"testmodel-4-6"}`)
	pin = StaleDefaultPinFor(config.NewSettingsManager(cwd, dir), registry)
	if pin == nil {
		t.Fatal("expected a warning for a stale project pin")
	}
	if pin.Scope != config.ScopeProject || pin.Path != filepath.Join(cwd, config.ConfigDirName, "settings.json") {
		t.Errorf("wrong provenance: scope=%q path=%q", pin.Scope, pin.Path)
	}

	// Nil inputs never panic.
	if StaleDefaultPinFor(nil, registry) != nil || StaleDefaultPinFor(config.NewInMemorySettingsManager(config.Settings{}), nil) != nil {
		t.Error("nil inputs must yield no warning")
	}
}
