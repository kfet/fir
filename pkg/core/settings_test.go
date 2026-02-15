package core

import (
	"encoding/json"
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
	assert.Equal(t, "tree", sm.GetDoubleEscapeAction())
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
}

func TestInMemorySettingsManager_CompactionSettings(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})

	sm.SetCompactionEnabled(false)
	assert.False(t, sm.GetCompactionEnabled())

	result := sm.GetCompactionSettings()
	assert.False(t, result.Enabled)
	assert.Equal(t, 16384, result.ReserveTokens)
	assert.Equal(t, 20000, result.KeepRecentTokens)
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
	assert.Equal(t, 3, *result.Retry.MaxRetries)  // from overrides
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

	t.Setenv("TAU_CLEAR_ON_SHRINK", "1")
	assert.True(t, sm.GetClearOnShrink())
}

func TestSettingsManager_ShowHardwareCursor_EnvVar(t *testing.T) {
	sm := NewInMemorySettingsManager(Settings{})
	assert.False(t, sm.GetShowHardwareCursor())

	t.Setenv("TAU_HARDWARE_CURSOR", "1")
	assert.True(t, sm.GetShowHardwareCursor())
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }
