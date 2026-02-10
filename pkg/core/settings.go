// Ported from: packages/coding-agent/src/core/settings-manager.ts
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// CompactionSettings controls context compaction behavior.
type CompactionSettings struct {
	Enabled          *bool `json:"enabled,omitempty"`
	ReserveTokens    *int  `json:"reserveTokens,omitempty"`
	KeepRecentTokens *int  `json:"keepRecentTokens,omitempty"`
}

// BranchSummarySettings controls branch summary behavior.
type BranchSummarySettings struct {
	ReserveTokens *int `json:"reserveTokens,omitempty"`
}

// RetrySettings controls retry behavior.
type RetrySettings struct {
	Enabled    *bool `json:"enabled,omitempty"`
	MaxRetries *int  `json:"maxRetries,omitempty"`
	BaseDelayMs *int `json:"baseDelayMs,omitempty"`
	MaxDelayMs  *int `json:"maxDelayMs,omitempty"`
}

// TerminalSettings controls terminal display behavior.
type TerminalSettings struct {
	ShowImages    *bool `json:"showImages,omitempty"`
	ClearOnShrink *bool `json:"clearOnShrink,omitempty"`
}

// ImageSettings controls image handling.
type ImageSettings struct {
	AutoResize  *bool `json:"autoResize,omitempty"`
	BlockImages *bool `json:"blockImages,omitempty"`
}

// ThinkingBudgetsSettings maps thinking levels to token budgets.
type ThinkingBudgetsSettings struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

// MarkdownSettings controls markdown rendering.
type MarkdownSettings struct {
	CodeBlockIndent *string `json:"codeBlockIndent,omitempty"`
}

// Settings is the full settings schema.
type Settings struct {
	LastChangelogVersion  string                   `json:"lastChangelogVersion,omitempty"`
	DefaultProvider       string                   `json:"defaultProvider,omitempty"`
	DefaultModel          string                   `json:"defaultModel,omitempty"`
	DefaultThinkingLevel  string                   `json:"defaultThinkingLevel,omitempty"`
	SteeringMode          string                   `json:"steeringMode,omitempty"`
	FollowUpMode          string                   `json:"followUpMode,omitempty"`
	Theme                 string                   `json:"theme,omitempty"`
	Compaction            *CompactionSettings       `json:"compaction,omitempty"`
	BranchSummary         *BranchSummarySettings    `json:"branchSummary,omitempty"`
	Retry                 *RetrySettings            `json:"retry,omitempty"`
	HideThinkingBlock     *bool                     `json:"hideThinkingBlock,omitempty"`
	ShellPath             string                    `json:"shellPath,omitempty"`
	QuietStartup          *bool                     `json:"quietStartup,omitempty"`
	ShellCommandPrefix    string                    `json:"shellCommandPrefix,omitempty"`
	CollapseChangelog     *bool                     `json:"collapseChangelog,omitempty"`
	Packages              []any                     `json:"packages,omitempty"`
	Extensions            []string                  `json:"extensions,omitempty"`
	Skills                []string                  `json:"skills,omitempty"`
	Prompts               []string                  `json:"prompts,omitempty"`
	Themes                []string                  `json:"themes,omitempty"`
	EnableSkillCommands   *bool                     `json:"enableSkillCommands,omitempty"`
	Terminal              *TerminalSettings          `json:"terminal,omitempty"`
	Images                *ImageSettings             `json:"images,omitempty"`
	EnabledModels         []string                  `json:"enabledModels,omitempty"`
	DoubleEscapeAction    string                    `json:"doubleEscapeAction,omitempty"`
	ThinkingBudgets       *ThinkingBudgetsSettings   `json:"thinkingBudgets,omitempty"`
	EditorPaddingX        *int                      `json:"editorPaddingX,omitempty"`
	AutocompleteMaxVisible *int                     `json:"autocompleteMaxVisible,omitempty"`
	ShowHardwareCursor    *bool                     `json:"showHardwareCursor,omitempty"`
	Markdown              *MarkdownSettings          `json:"markdown,omitempty"`
}

// deepMergeSettings merges overrides into base, with nested objects merged recursively.
// Arrays and primitives from overrides win over base.
func deepMergeSettings(base, overrides Settings) Settings {
	// Marshal both to JSON and merge as maps for simplicity
	baseJSON, _ := json.Marshal(base)
	overJSON, _ := json.Marshal(overrides)

	var baseMap, overMap map[string]any
	json.Unmarshal(baseJSON, &baseMap)
	json.Unmarshal(overJSON, &overMap)

	if baseMap == nil {
		baseMap = map[string]any{}
	}

	for k, v := range overMap {
		if v == nil {
			continue
		}
		baseVal, hasBase := baseMap[k]
		// Merge nested objects recursively
		if overObj, ok := v.(map[string]any); ok {
			if baseObj, ok2 := baseVal.(map[string]any); ok2 && hasBase {
				merged := map[string]any{}
				for bk, bv := range baseObj {
					merged[bk] = bv
				}
				for ok, ov := range overObj {
					merged[ok] = ov
				}
				baseMap[k] = merged
				continue
			}
		}
		baseMap[k] = v
	}

	resultJSON, _ := json.Marshal(baseMap)
	var result Settings
	json.Unmarshal(resultJSON, &result)
	return result
}

// SettingsManager manages global and project settings with file persistence.
type SettingsManager struct {
	mu sync.RWMutex

	settingsPath        string
	projectSettingsPath string
	globalSettings      Settings
	inMemoryProject     Settings
	settings            Settings
	persist             bool
	modifiedFields      map[string]bool
	modifiedNested      map[string]map[string]bool
	loadError           error
}

// NewSettingsManager creates a SettingsManager that loads from files.
func NewSettingsManager(cwd, agentDir string) *SettingsManager {
	settingsPath := filepath.Join(agentDir, "settings.json")
	projectSettingsPath := filepath.Join(cwd, ConfigDirName, "settings.json")

	globalSettings := Settings{}
	var loadError error

	data, err := os.ReadFile(settingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &globalSettings); err != nil {
			loadError = err
		} else {
			globalSettings = migrateSettings(globalSettings)
		}
	}

	projectSettings := loadSettingsFile(projectSettingsPath)
	merged := deepMergeSettings(globalSettings, projectSettings)

	return &SettingsManager{
		settingsPath:        settingsPath,
		projectSettingsPath: projectSettingsPath,
		globalSettings:      globalSettings,
		settings:            merged,
		persist:             true,
		modifiedFields:      map[string]bool{},
		modifiedNested:      map[string]map[string]bool{},
		loadError:           loadError,
	}
}

// NewInMemorySettingsManager creates a SettingsManager with no file I/O.
func NewInMemorySettingsManager(initial Settings) *SettingsManager {
	return &SettingsManager{
		globalSettings: initial,
		settings:       initial,
		persist:        false,
		modifiedFields: map[string]bool{},
		modifiedNested: map[string]map[string]bool{},
	}
}

func loadSettingsFile(path string) Settings {
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}
	}
	return migrateSettings(s)
}

func migrateSettings(s Settings) Settings {
	// Migration: queueMode -> steeringMode handled at JSON level if needed
	return s
}

func (sm *SettingsManager) markModified(field string, nestedKeys ...string) {
	sm.modifiedFields[field] = true
	for _, nk := range nestedKeys {
		if sm.modifiedNested[field] == nil {
			sm.modifiedNested[field] = map[string]bool{}
		}
		sm.modifiedNested[field][nk] = true
	}
}

func (sm *SettingsManager) save() {
	if !sm.persist || sm.settingsPath == "" {
		sm.remerge()
		return
	}

	if sm.loadError != nil {
		sm.remerge()
		return
	}

	// Read current file for latest external changes
	current := loadSettingsFile(sm.settingsPath)

	// Merge modified fields from globalSettings onto current
	currentJSON, _ := json.Marshal(current)
	globalJSON, _ := json.Marshal(sm.globalSettings)
	var currentMap, globalMap map[string]any
	json.Unmarshal(currentJSON, &currentMap)
	json.Unmarshal(globalJSON, &globalMap)
	if currentMap == nil {
		currentMap = map[string]any{}
	}

	for field := range sm.modifiedFields {
		if nestedKeys, hasNested := sm.modifiedNested[field]; hasNested {
			baseNested, _ := currentMap[field].(map[string]any)
			if baseNested == nil {
				baseNested = map[string]any{}
			}
			inMemNested, _ := globalMap[field].(map[string]any)
			for nk := range nestedKeys {
				if inMemNested != nil {
					baseNested[nk] = inMemNested[nk]
				}
			}
			currentMap[field] = baseNested
		} else {
			currentMap[field] = globalMap[field]
		}
	}

	resultJSON, _ := json.Marshal(currentMap)
	json.Unmarshal(resultJSON, &sm.globalSettings)

	dir := filepath.Dir(sm.settingsPath)
	os.MkdirAll(dir, 0755)
	os.WriteFile(sm.settingsPath, resultJSON, 0644)

	sm.remerge()
}

func (sm *SettingsManager) remerge() {
	project := Settings{}
	if sm.persist {
		project = loadSettingsFile(sm.projectSettingsPath)
	} else {
		project = sm.inMemoryProject
	}
	sm.settings = deepMergeSettings(sm.globalSettings, project)
}

// Reload re-reads settings from disk.
func (sm *SettingsManager) Reload() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.persist && sm.settingsPath != "" {
		data, err := os.ReadFile(sm.settingsPath)
		if err == nil {
			var gs Settings
			if err := json.Unmarshal(data, &gs); err == nil {
				sm.globalSettings = migrateSettings(gs)
				sm.loadError = nil
			} else {
				sm.loadError = err
			}
		}
	}

	sm.modifiedFields = map[string]bool{}
	sm.modifiedNested = map[string]map[string]bool{}
	sm.remerge()
}

// ApplyOverrides merges additional overrides on top.
func (sm *SettingsManager) ApplyOverrides(overrides Settings) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.settings = deepMergeSettings(sm.settings, overrides)
}

// --- Getters & Setters ---

func (sm *SettingsManager) GetDefaultProvider() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.DefaultProvider
}

func (sm *SettingsManager) GetDefaultModel() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.DefaultModel
}

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

func (sm *SettingsManager) SetDefaultModelAndProvider(provider, modelID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.DefaultProvider = provider
	sm.globalSettings.DefaultModel = modelID
	sm.markModified("defaultProvider")
	sm.markModified("defaultModel")
	sm.save()
}

func (sm *SettingsManager) GetSteeringMode() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.SteeringMode != "" {
		return sm.settings.SteeringMode
	}
	return "one-at-a-time"
}

func (sm *SettingsManager) SetSteeringMode(mode string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.SteeringMode = mode
	sm.markModified("steeringMode")
	sm.save()
}

func (sm *SettingsManager) GetFollowUpMode() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.FollowUpMode != "" {
		return sm.settings.FollowUpMode
	}
	return "one-at-a-time"
}

func (sm *SettingsManager) SetFollowUpMode(mode string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.FollowUpMode = mode
	sm.markModified("followUpMode")
	sm.save()
}

func (sm *SettingsManager) GetTheme() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.Theme
}

func (sm *SettingsManager) SetTheme(theme string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.Theme = theme
	sm.markModified("theme")
	sm.save()
}

func (sm *SettingsManager) GetDefaultThinkingLevel() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.DefaultThinkingLevel
}

func (sm *SettingsManager) SetDefaultThinkingLevel(level string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.DefaultThinkingLevel = level
	sm.markModified("defaultThinkingLevel")
	sm.save()
}

func boolDefault(b *bool, def bool) bool {
	if b != nil {
		return *b
	}
	return def
}

func intDefault(i *int, def int) int {
	if i != nil {
		return *i
	}
	return def
}

func (sm *SettingsManager) GetCompactionEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Compaction != nil {
		return boolDefault(sm.settings.Compaction.Enabled, true)
	}
	return true
}

func (sm *SettingsManager) SetCompactionEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.globalSettings.Compaction == nil {
		sm.globalSettings.Compaction = &CompactionSettings{}
	}
	sm.globalSettings.Compaction.Enabled = &enabled
	sm.markModified("compaction", "enabled")
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

type CompactionResult struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
}

func (sm *SettingsManager) GetCompactionSettings() CompactionResult {
	return CompactionResult{
		Enabled:          sm.GetCompactionEnabled(),
		ReserveTokens:    sm.GetCompactionReserveTokens(),
		KeepRecentTokens: sm.GetCompactionKeepRecentTokens(),
	}
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

type RetryResult struct {
	Enabled    bool
	MaxRetries int
	BaseDelayMs int
	MaxDelayMs  int
}

func (sm *SettingsManager) GetRetrySettings() RetryResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	r := RetryResult{
		Enabled:    true,
		MaxRetries: 3,
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

func (sm *SettingsManager) SetHideThinkingBlock(hide bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.HideThinkingBlock = &hide
	sm.markModified("hideThinkingBlock")
	sm.save()
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

func (sm *SettingsManager) GetShellCommandPrefix() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.ShellCommandPrefix
}

func (sm *SettingsManager) GetCollapseChangelog() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return boolDefault(sm.settings.CollapseChangelog, false)
}

func (sm *SettingsManager) GetExtensionPaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Extensions))
	copy(out, sm.settings.Extensions)
	return out
}

func (sm *SettingsManager) GetSkillPaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Skills))
	copy(out, sm.settings.Skills)
	return out
}

func (sm *SettingsManager) GetPromptTemplatePaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Prompts))
	copy(out, sm.settings.Prompts)
	return out
}

func (sm *SettingsManager) GetThemePaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Themes))
	copy(out, sm.settings.Themes)
	return out
}

func (sm *SettingsManager) GetEnableSkillCommands() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return boolDefault(sm.settings.EnableSkillCommands, true)
}

func (sm *SettingsManager) GetThinkingBudgets() *ThinkingBudgetsSettings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.ThinkingBudgets
}

func (sm *SettingsManager) GetShowImages() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Terminal != nil {
		return boolDefault(sm.settings.Terminal.ShowImages, true)
	}
	return true
}

func (sm *SettingsManager) SetShowImages(show bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.globalSettings.Terminal == nil {
		sm.globalSettings.Terminal = &TerminalSettings{}
	}
	sm.globalSettings.Terminal.ShowImages = &show
	sm.markModified("terminal", "showImages")
	sm.save()
}

func (sm *SettingsManager) GetClearOnShrink() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Terminal != nil && sm.settings.Terminal.ClearOnShrink != nil {
		return *sm.settings.Terminal.ClearOnShrink
	}
	return os.Getenv("PI_CLEAR_ON_SHRINK") == "1"
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

func (sm *SettingsManager) GetEnabledModels() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.EnabledModels
}

func (sm *SettingsManager) SetEnabledModels(patterns []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.EnabledModels = patterns
	sm.markModified("enabledModels")
	sm.save()
}

func (sm *SettingsManager) GetDoubleEscapeAction() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.DoubleEscapeAction != "" {
		return sm.settings.DoubleEscapeAction
	}
	return "tree"
}

func (sm *SettingsManager) GetEditorPaddingX() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return intDefault(sm.settings.EditorPaddingX, 0)
}

func (sm *SettingsManager) GetAutocompleteMaxVisible() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return intDefault(sm.settings.AutocompleteMaxVisible, 5)
}

func (sm *SettingsManager) GetCodeBlockIndent() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Markdown != nil && sm.settings.Markdown.CodeBlockIndent != nil {
		return *sm.settings.Markdown.CodeBlockIndent
	}
	return "  "
}

func (sm *SettingsManager) GetShowHardwareCursor() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.ShowHardwareCursor != nil {
		return *sm.settings.ShowHardwareCursor
	}
	return os.Getenv("PI_HARDWARE_CURSOR") == "1"
}

// GetGlobalSettings returns a copy of global settings.
func (sm *SettingsManager) GetGlobalSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	// Deep copy via JSON
	data, _ := json.Marshal(sm.globalSettings)
	var copy Settings
	json.Unmarshal(data, &copy)
	return copy
}

// GetProjectSettings returns a copy of project settings.
func (sm *SettingsManager) GetProjectSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.persist {
		return loadSettingsFile(sm.projectSettingsPath)
	}
	data, _ := json.Marshal(sm.inMemoryProject)
	var copy Settings
	json.Unmarshal(data, &copy)
	return copy
}
