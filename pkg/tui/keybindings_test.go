package tui

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
	if len(keys) != 1 || keys[0] != "ctrl+n" {
		t.Errorf("newSession keys = %v, want [ctrl+n]", keys)
	}

	keys = m.GetKeys(ActionSelectThinking)
	if len(keys) != 0 {
		t.Errorf("selectThinking keys = %v, want []", keys)
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

	// Verify array override from file was loaded
	keys = m.GetKeys(ActionExit)
	if len(keys) != 2 {
		t.Fatalf("exit keys = %v, want 2 keys", keys)
	}
	if keys[0] != "ctrl+d" || keys[1] != "ctrl+q" {
		t.Errorf("exit keys = %v, want [ctrl+d ctrl+q]", keys)
	}
}

func TestKeybindingsManager_FromFile_Matches(t *testing.T) {
	dir := t.TempDir()
	config := map[string]any{
		"exit": []string{"ctrl+d", "ctrl+q"},
	}
	data, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(dir, "keybindings.json"), data, 0644)

	m := NewKeybindingsManager(dir)

	// ctrl+d should match exit (byte 0x04)
	if !m.Matches("\x04", ActionExit) {
		t.Error("expected ctrl+d to match exit")
	}

	// ctrl+q should match exit (byte 0x11)
	if !m.Matches("\x11", ActionExit) {
		t.Error("expected ctrl+q to match exit")
	}

	// ctrl+c should NOT match exit
	if m.Matches("\x03", ActionExit) {
		t.Error("expected ctrl+c to NOT match exit")
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

func TestKeybindingsManager_EmptyArrayOverride(t *testing.T) {
	// Overriding with empty array should remove all default bindings
	config := KeybindingsConfig{
		"interrupt": []any{},
	}
	m := NewKeybindingsManagerInMemory(config)

	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 0 {
		t.Errorf("interrupt keys = %v, want []", keys)
	}

	// Matches should return false for the now-removed default
	if m.Matches("\x1b", ActionInterrupt) {
		t.Error("expected escape to NOT match interrupt after empty override")
	}
}

func TestKeybindingsManager_SelectThinkingConfigurable(t *testing.T) {
	// selectThinking has no default keybinding but should be configurable
	config := KeybindingsConfig{
		"selectThinking": "ctrl+t",
	}
	m := NewKeybindingsManagerInMemory(config)

	keys := m.GetKeys(ActionSelectThinking)
	if len(keys) != 1 || keys[0] != "ctrl+t" {
		t.Errorf("selectThinking keys = %v, want [ctrl+t]", keys)
	}
}

func TestKeybindingsManager_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "keybindings.json"), []byte("{invalid json}"), 0644)

	m := NewKeybindingsManager(dir)
	// Should fall back to defaults
	keys := m.GetKeys(ActionInterrupt)
	if len(keys) != 1 || keys[0] != "escape" {
		t.Errorf("interrupt keys = %v, want [escape]", keys)
	}
}

func TestKeybindingsManager_AllDefaultActionsRegistered(t *testing.T) {
	// Verify that every AppAction constant is in DefaultAppKeybindings
	allActions := []AppAction{
		ActionInterrupt,
		ActionClear,
		ActionExit,
		ActionSuspend,
		ActionCycleThinkingLevel,
		ActionCycleModelForward,
		ActionCycleModelBackward,
		ActionSelectModel,
		ActionExpandTools,
		ActionToggleThinking,
		ActionExternalEditor,
		ActionFollowUp,
		ActionDequeue,
		ActionPasteImage,
		ActionNewSession,
		ActionSelectThinking,
		ActionTree,
		ActionResume,
	}

	for _, action := range allActions {
		if !isAppAction(action) {
			t.Errorf("action %q not in DefaultAppKeybindings", action)
		}
	}
}

func TestKeybindingsManager_ProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	os.WriteFile(filepath.Join(globalDir, "keybindings.json"),
		[]byte(`{"exit": "ctrl+q", "clear": "ctrl+k"}`), 0644)

	projectDir := t.TempDir()
	os.WriteFile(filepath.Join(projectDir, "keybindings.json"),
		[]byte(`{"exit": "ctrl+x"}`), 0644)

	m := NewKeybindingsManager(globalDir, projectDir)

	exitKeys := m.GetKeys(ActionExit)
	if len(exitKeys) != 1 || exitKeys[0] != "ctrl+x" {
		t.Errorf("expected [ctrl+x], got %v", exitKeys)
	}

	clearKeys := m.GetKeys(ActionClear)
	if len(clearKeys) != 1 || clearKeys[0] != "ctrl+k" {
		t.Errorf("expected [ctrl+k], got %v", clearKeys)
	}
}

func TestKeybindingsManager_ProjectDirEmpty(t *testing.T) {
	globalDir := t.TempDir()
	os.WriteFile(filepath.Join(globalDir, "keybindings.json"),
		[]byte(`{"exit": "ctrl+q"}`), 0644)

	m := NewKeybindingsManager(globalDir, "")

	exitKeys := m.GetKeys(ActionExit)
	if len(exitKeys) != 1 || exitKeys[0] != "ctrl+q" {
		t.Errorf("expected [ctrl+q], got %v", exitKeys)
	}
}
