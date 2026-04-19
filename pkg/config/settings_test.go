package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewInMemorySettingsManager_Defaults(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})

	assert.Equal(t, "", sm.GetDefaultProvider())
	assert.Equal(t, "", sm.GetDefaultModel())
	assert.Equal(t, "one-at-a-time", sm.GetSteeringMode())
	assert.Equal(t, "one-at-a-time", sm.GetFollowUpMode())
	assert.Equal(t, "", sm.GetTheme())
	assert.Equal(t, "", sm.GetDefaultThinkingLevel())
	assert.True(t, sm.GetCompactionEnabled())
	assert.Equal(t, 16384, sm.GetCompactionReserveTokens())
	assert.Equal(t, 20000, sm.GetCompactionKeepRecentTokens())
	assert.True(t, sm.GetRetryEnabled())
	assert.False(t, sm.GetHideThinkingBlock())
	assert.Equal(t, "", sm.GetShellPath())
	assert.False(t, sm.GetQuietStartup())
	assert.True(t, sm.GetShowImages())
	assert.True(t, sm.GetImageAutoResize())
	assert.False(t, sm.GetBlockImages())
	assert.True(t, sm.GetEnableSkillCommands())
	assert.Equal(t, 0, sm.GetEditorPaddingX())
	assert.Equal(t, 5, sm.GetAutocompleteMaxVisible())
	assert.Equal(t, "  ", sm.GetCodeBlockIndent())
	assert.Equal(t, "sse", sm.GetTransport())
}

func TestInMemorySettingsManager_SettersAndGetters(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})

	sm.SetDefaultProvider("openai")
	assert.Equal(t, "openai", sm.GetDefaultProvider())

	sm.SetDefaultModel("gpt-4o")
	assert.Equal(t, "gpt-4o", sm.GetDefaultModel())

	sm.SetDefaultModelAndProvider("anthropic", "claude-3-5-sonnet")
	assert.Equal(t, "anthropic", sm.GetDefaultProvider())
	assert.Equal(t, "claude-3-5-sonnet", sm.GetDefaultModel())

	sm.SetSteeringMode("all")
	assert.Equal(t, "all", sm.GetSteeringMode())

	sm.SetFollowUpMode("all")
	assert.Equal(t, "all", sm.GetFollowUpMode())

	sm.SetTheme("dark")
	assert.Equal(t, "dark", sm.GetTheme())

	sm.SetDefaultThinkingLevel("high")
	assert.Equal(t, "high", sm.GetDefaultThinkingLevel())

	sm.SetHideThinkingBlock(true)
	assert.True(t, sm.GetHideThinkingBlock())

	sm.SetShellPath("/bin/zsh")
	assert.Equal(t, "/bin/zsh", sm.GetShellPath())

	sm.SetShowImages(false)
	assert.False(t, sm.GetShowImages())

	sm.SetTransport("streamingJson")
	assert.Equal(t, "streamingJson", sm.GetTransport())

	sm.SetEnableSkillCommands(false)
	assert.False(t, sm.GetEnableSkillCommands())
	sm.SetEnableSkillCommands(true)
	assert.True(t, sm.GetEnableSkillCommands())
}

func TestInMemorySettingsManager_CompactionSettings(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})

	sm.SetCompactionEnabled(false)
	assert.False(t, sm.GetCompactionEnabled())

	result := sm.GetCompactionSettings()
	assert.False(t, result.Enabled)
	assert.Equal(t, 16384, result.ReserveTokens)
	assert.Equal(t, 20000, result.KeepRecentTokens)
	assert.Nil(t, result.MaxContextTokens)
}

func TestInMemorySettingsManager_CompactionSettingsMaxContextTokens(t *testing.T) {
	maxTokens := 50000
	sm := NewInMemorySettingsManager(Settings{
		Compaction: &CompactionSettings{
			MaxContextTokens: &maxTokens,
		},
	})

	result := sm.GetCompactionSettings()
	assert.True(t, result.Enabled)
	assert.NotNil(t, result.MaxContextTokens)
	assert.Equal(t, 50000, *result.MaxContextTokens)
}

func TestInMemorySettingsManager_SetCompactionMaxContextTokens(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})

	// Default is 0 (disabled)
	assert.Equal(t, 0, sm.GetCompactionMaxContextTokens())

	// Set a value
	sm.SetCompactionMaxContextTokens(100000)
	assert.Equal(t, 100000, sm.GetCompactionMaxContextTokens())

	// Also visible via GetCompactionSettings
	result := sm.GetCompactionSettings()
	assert.NotNil(t, result.MaxContextTokens)
	assert.Equal(t, 100000, *result.MaxContextTokens)

	// Set to 0 disables
	sm.SetCompactionMaxContextTokens(0)
	assert.Equal(t, 0, sm.GetCompactionMaxContextTokens())
}

func TestInMemorySettingsManager_RetrySettings(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})

	r := sm.GetRetrySettings()
	assert.True(t, r.Enabled)
	assert.Equal(t, 3, r.MaxRetries)
	assert.Equal(t, 2000, r.BaseDelayMs)
	assert.Equal(t, 60000, r.MaxDelayMs)

	sm.SetRetryEnabled(false)
	r = sm.GetRetrySettings()
	assert.False(t, r.Enabled)
}

func TestInMemorySettingsManager_EnabledModels(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})
	assert.Nil(t, sm.GetEnabledModels())

	sm.SetEnabledModels([]string{"gpt*", "claude*"})
	assert.Equal(t, []string{"gpt*", "claude*"}, sm.GetEnabledModels())
}

func TestDeepMergeSettings(t *testing.T) {
	base := Settings{
		DefaultProvider: "openai",
		DefaultModel:    "gpt-4",
		Compaction:      &CompactionSettings{Enabled: boolPtr(true)},
	}
	overrides := Settings{
		DefaultModel: "gpt-4o",
		Theme:        "dark",
	}

	result := deepMergeSettings(base, overrides)
	assert.Equal(t, "openai", result.DefaultProvider)
	assert.Equal(t, "gpt-4o", result.DefaultModel)
	assert.Equal(t, "dark", result.Theme)
	assert.NotNil(t, result.Compaction)
}

func TestDeepMergeSettings_NestedMerge(t *testing.T) {
	base := Settings{
		Retry: &RetrySettings{
			Enabled:    boolPtr(true),
			MaxRetries: intPtr(5),
		},
	}
	overrides := Settings{
		Retry: &RetrySettings{
			MaxRetries: intPtr(3),
		},
	}

	result := deepMergeSettings(base, overrides)
	require.NotNil(t, result.Retry)
	assert.Equal(t, true, *result.Retry.Enabled) // from base
	assert.Equal(t, 3, *result.Retry.MaxRetries) // from overrides
}

func TestDeepMergeSettings_CompactionMaxContextTokens(t *testing.T) {
	base := Settings{
		Compaction: &CompactionSettings{
			Enabled: boolPtr(true),
		},
	}
	overrides := Settings{
		Compaction: &CompactionSettings{
			MaxContextTokens: intPtr(50000),
		},
	}

	result := deepMergeSettings(base, overrides)
	require.NotNil(t, result.Compaction)
	assert.Equal(t, true, *result.Compaction.Enabled)           // from base
	assert.Equal(t, 50000, *result.Compaction.MaxContextTokens) // from overrides
}

func TestSettingsManager_FilePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	cwd := filepath.Join(tmpDir, "project")
	os.MkdirAll(agentDir, 0755)
	os.MkdirAll(cwd, 0755)

	sm := NewSettingsManager(cwd, agentDir)
	sm.SetDefaultProvider("google")
	sm.SetDefaultModel("gemini-pro")

	// Read back from file
	data, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	require.NoError(t, err)

	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "google", saved["defaultProvider"])
	assert.Equal(t, "gemini-pro", saved["defaultModel"])
}

func TestSettingsManager_ProjectOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	cwd := filepath.Join(tmpDir, "project")
	os.MkdirAll(agentDir, 0755)
	projectDir := filepath.Join(cwd, ConfigDirName)
	os.MkdirAll(projectDir, 0755)

	// Write global settings
	globalSettings := Settings{DefaultProvider: "openai", Theme: "light"}
	data, _ := json.Marshal(globalSettings)
	os.WriteFile(filepath.Join(agentDir, "settings.json"), data, 0644)

	// Write project settings that override theme
	projectSettings := Settings{Theme: "dark"}
	data, _ = json.Marshal(projectSettings)
	os.WriteFile(filepath.Join(projectDir, "settings.json"), data, 0644)

	sm := NewSettingsManager(cwd, agentDir)
	assert.Equal(t, "openai", sm.GetDefaultProvider())
	assert.Equal(t, "dark", sm.GetTheme()) // project override
}

func TestSettingsManager_Reload(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "agent")
	cwd := filepath.Join(tmpDir, "project")
	os.MkdirAll(agentDir, 0755)
	os.MkdirAll(cwd, 0755)

	sm := NewSettingsManager(cwd, agentDir)
	sm.SetDefaultProvider("openai")

	// External edit
	data, _ := json.Marshal(Settings{DefaultProvider: "anthropic"})
	os.WriteFile(filepath.Join(agentDir, "settings.json"), data, 0644)

	sm.Reload()
	assert.Equal(t, "anthropic", sm.GetDefaultProvider())
}

func TestSettingsManager_ApplyOverrides(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{DefaultProvider: "openai"})
	sm.ApplyOverrides(Settings{DefaultModel: "gpt-4o"})
	assert.Equal(t, "openai", sm.GetDefaultProvider())
	assert.Equal(t, "gpt-4o", sm.GetDefaultModel())
}

func TestSettingsManager_GetGlobalAndProject(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{DefaultProvider: "openai"})
	global := sm.GetGlobalSettings()
	assert.Equal(t, "openai", global.DefaultProvider)

	project := sm.GetProjectSettings()
	assert.Equal(t, "", project.DefaultProvider)
}

func TestSettingsManager_ClearOnShrink_EnvVar(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})
	assert.False(t, sm.GetClearOnShrink())

	t.Setenv("FIR_CLEAR_ON_SHRINK", "1")
	assert.True(t, sm.GetClearOnShrink())
}

func TestSettingsManager_ShowHardwareCursor_EnvVar(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})
	assert.False(t, sm.GetShowHardwareCursor())

	t.Setenv("FIR_HARDWARE_CURSOR", "1")
	assert.True(t, sm.GetShowHardwareCursor())
}

func TestSettingsManager_DrainErrors_NoErrors(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})
	errs := sm.DrainErrors()
	assert.Empty(t, errs)
}

// failingSettingsStorage is a mock SettingsStorage whose WithLock always returns an error.
type failingSettingsStorage struct {
	err error
}

func (f *failingSettingsStorage) WithLock(_ SettingsScope, fn func(string) string) error {
	fn("") // still call fn so the SettingsManager can compute the result
	return f.err
}

func TestSettingsManager_DrainErrors_WriteError(t *testing.T) {
	writeErr := fmt.Errorf("disk full")
	storage := &failingSettingsStorage{err: writeErr}
	sm := NewSettingsManagerFromStorage(storage)

	// Any setter that calls save() triggers persistScopedSettings → WithLock → error.
	sm.SetDefaultProvider("openai")

	errs := sm.DrainErrors()
	require.Len(t, errs, 1)
	assert.ErrorIs(t, errs[0].Err, writeErr)
	assert.Equal(t, ScopeGlobal, errs[0].Scope)
}

func TestSettingsManager_DrainErrors_ClearsAfterDrain(t *testing.T) {
	writeErr := fmt.Errorf("disk full")
	storage := &failingSettingsStorage{err: writeErr}
	sm := NewSettingsManagerFromStorage(storage)

	sm.SetDefaultProvider("openai")
	_ = sm.DrainErrors() // drain once
	errs := sm.DrainErrors()
	assert.Empty(t, errs, "errors should be cleared after first drain")
}

func TestSettingsManager_Flush_IsNoop(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})
	// Should not panic or block
	sm.Flush()
}

func TestSettingsManager_ProjectSetters(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := NewSettingsManager(cwd, agentDir)

	sm.SetProjectEnabledExtensions([]string{"/ext/a", "/ext/b"})
	assert.Equal(t, []string{"/ext/a", "/ext/b"}, sm.GetEnabledExtensions())

	sm.SetProjectSkillPaths([]string{"/skills/a"})
	assert.Equal(t, []string{"/skills/a"}, sm.GetSkillPaths())

	sm.SetProjectPromptTemplatePaths([]string{"/prompts/a"})
	assert.Equal(t, []string{"/prompts/a"}, sm.GetPromptPaths())

	sm.SetProjectThemePaths([]string{"/themes/a"})
	assert.Equal(t, []string{"/themes/a"}, sm.GetThemePaths())

	sm.SetProjectExtensionPaths([]string{"/ext-search/a", "/ext-search/b"})
	assert.Equal(t, []string{"/ext-search/a", "/ext-search/b"}, sm.GetExtensionPaths())

	proj := sm.GetProjectSettings()
	assert.Equal(t, []string{"/ext/a", "/ext/b"}, proj.Extensions)
	assert.Equal(t, []string{"/skills/a"}, proj.Skills)
	assert.Equal(t, []string{"/prompts/a"}, proj.Prompts)
	assert.Equal(t, []string{"/themes/a"}, proj.Themes)
	assert.Equal(t, []string{"/ext-search/a", "/ext-search/b"}, proj.ExtensionPaths)
}

func TestSettingsManager_InMemoryStorage(t *testing.T) {
	storage := &InMemorySettingsStorage{}
	sm := NewSettingsManagerFromStorage(storage)

	sm.SetDefaultProvider("openai")
	sm.SetDefaultModel("gpt-4o")

	assert.Equal(t, "openai", sm.GetDefaultProvider())
	assert.Equal(t, "gpt-4o", sm.GetDefaultModel())
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// Test helper methods moved from settings.go — only used in tests.

func (sm *SettingsManager) SetDefaultProvider(provider string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.DefaultProvider = provider
	sm.markModified("defaultProvider")
	sm.save()
}

func (sm *SettingsManager) SetDefaultModel(modelID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.DefaultModel = modelID
	sm.markModified("defaultModel")
	sm.save()
}

func (sm *SettingsManager) GetCompactionReserveTokens() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Compaction != nil {
		return intDefault(sm.settings.Compaction.ReserveTokens, 16384)
	}
	return 16384
}

func (sm *SettingsManager) GetCompactionKeepRecentTokens() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Compaction != nil {
		return intDefault(sm.settings.Compaction.KeepRecentTokens, 20000)
	}
	return 20000
}

func (sm *SettingsManager) GetRetryEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Retry != nil {
		return boolDefault(sm.settings.Retry.Enabled, true)
	}
	return true
}

func (sm *SettingsManager) SetRetryEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.globalSettings.Retry == nil {
		sm.globalSettings.Retry = &RetrySettings{}
	}
	sm.globalSettings.Retry.Enabled = &enabled
	sm.markModified("retry", "enabled")
	sm.save()
}

func (sm *SettingsManager) GetRetrySettings() RetryResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	r := RetryResult{
		Enabled:     true,
		MaxRetries:  3,
		BaseDelayMs: 2000,
		MaxDelayMs:  60000,
	}
	if sm.settings.Retry != nil {
		r.Enabled = boolDefault(sm.settings.Retry.Enabled, true)
		r.MaxRetries = intDefault(sm.settings.Retry.MaxRetries, 3)
		r.BaseDelayMs = intDefault(sm.settings.Retry.BaseDelayMs, 2000)
		r.MaxDelayMs = intDefault(sm.settings.Retry.MaxDelayMs, 60000)
	}
	return r
}

func (sm *SettingsManager) GetHideThinkingBlock() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return boolDefault(sm.settings.HideThinkingBlock, false)
}

func (sm *SettingsManager) GetShellPath() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.ShellPath
}

func (sm *SettingsManager) SetShellPath(path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.ShellPath = path
	sm.markModified("shellPath")
	sm.save()
}

func (sm *SettingsManager) GetQuietStartup() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return boolDefault(sm.settings.QuietStartup, false)
}

func (sm *SettingsManager) GetShowImages() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Terminal != nil {
		return boolDefault(sm.settings.Terminal.ShowImages, true)
	}
	return true
}

func (sm *SettingsManager) GetClearOnShrink() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Terminal != nil && sm.settings.Terminal.ClearOnShrink != nil {
		return *sm.settings.Terminal.ClearOnShrink
	}
	return os.Getenv("FIR_CLEAR_ON_SHRINK") == "1"
}

func (sm *SettingsManager) GetImageAutoResize() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Images != nil {
		return boolDefault(sm.settings.Images.AutoResize, true)
	}
	return true
}

func (sm *SettingsManager) GetBlockImages() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Images != nil {
		return boolDefault(sm.settings.Images.BlockImages, false)
	}
	return false
}

func (sm *SettingsManager) GetEditorPaddingX() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return intDefault(sm.settings.EditorPaddingX, 0)
}

func (sm *SettingsManager) GetCodeBlockIndent() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Markdown != nil && sm.settings.Markdown.CodeBlockIndent != nil {
		return *sm.settings.Markdown.CodeBlockIndent
	}
	return "  "
}
