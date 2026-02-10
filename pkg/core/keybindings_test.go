package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKeybindingsManager_Defaults(t *testing.T) {
	m := NewKeybindingsManagerInMemory(nil)

	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 1 || keys[0] != "escape" {
		t.Errorf("interrupt keys = %v, want [escape]", keys)
	}

	keys = m.GetKeys(ActionExit)
	if len(keys) != 1 || keys[0] != "ctrl+d" {
		t.Errorf("exit keys = %v, want [ctrl+d]", keys)
	}

	keys = m.GetKeys(ActionNewSession)
	if len(keys) != 0 {
		t.Errorf("newSession keys = %v, want []", keys)
	}
}

func TestKeybindingsManager_Override(t *testing.T) {
	config := KeybindingsConfig{
		"interrupt": "ctrl+c",
	}
	m := NewKeybindingsManagerInMemory(config)

	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 1 || keys[0] != "ctrl+c" {
		t.Errorf("interrupt keys = %v, want [ctrl+c]", keys)
	}

	// Other defaults should still work
	keys = m.GetKeys(ActionExit)
	if len(keys) != 1 || keys[0] != "ctrl+d" {
		t.Errorf("exit keys = %v, want [ctrl+d]", keys)
	}
}

func TestKeybindingsManager_OverrideArray(t *testing.T) {
	config := KeybindingsConfig{
		"interrupt": []any{"escape", "ctrl+c"},
	}
	m := NewKeybindingsManagerInMemory(config)

	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 2 {
		t.Fatalf("interrupt keys = %v, want 2 keys", keys)
	}
	if keys[0] != "escape" || keys[1] != "ctrl+c" {
		t.Errorf("interrupt keys = %v, want [escape ctrl+c]", keys)
	}
}

func TestKeybindingsManager_FromFile(t *testing.T) {
	dir := t.TempDir()
	config := map[string]any{
		"interrupt": "ctrl+c",
		"exit":      []string{"ctrl+d", "ctrl+q"},
	}
	data, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(dir, "keybindings.json"), data, 0644)

	m := NewKeybindingsManager(dir)
	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 1 || keys[0] != "ctrl+c" {
		t.Errorf("interrupt keys = %v, want [ctrl+c]", keys)
	}
}

func TestKeybindingsManager_MissingFile(t *testing.T) {
	m := NewKeybindingsManager("/nonexistent")
	// Should use all defaults
	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 1 || keys[0] != "escape" {
		t.Errorf("interrupt keys = %v, want [escape]", keys)
	}
}

func TestKeybindingsManager_UnknownAction(t *testing.T) {
	config := KeybindingsConfig{
		"unknownAction": "ctrl+x",
	}
	m := NewKeybindingsManagerInMemory(config)
	// Should not crash, unknown actions are ignored
	keys := m.GetKeys(AppAction("unknownAction"))
	if len(keys) != 0 {
		t.Errorf("unknown action keys = %v, want []", keys)
	}
}
