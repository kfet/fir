// Ported from: packages/coding-agent/src/core/settings-manager.ts
// Upstream hash: 9e22d391
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
	Transport             string                   `json:"transport,omitempty"`
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
// Arrays and primitives from overrides win over base. Nil pointers and empty strings
// in overrides are treated as "not set" and don't overwrite base values.
func deepMergeSettings(base, overrides Settings) Settings {
	r := base

	// Simple string fields: override wins if non-empty
	mergeStr(&r.LastChangelogVersion, overrides.LastChangelogVersion)
	mergeStr(&r.DefaultProvider, overrides.DefaultProvider)
	mergeStr(&r.DefaultModel, overrides.DefaultModel)
	mergeStr(&r.DefaultThinkingLevel, overrides.DefaultThinkingLevel)
	mergeStr(&r.Transport, overrides.Transport)
	mergeStr(&r.SteeringMode, overrides.SteeringMode)
	mergeStr(&r.FollowUpMode, overrides.FollowUpMode)
	mergeStr(&r.Theme, overrides.Theme)
	mergeStr(&r.ShellPath, overrides.ShellPath)
	mergeStr(&r.ShellCommandPrefix, overrides.ShellCommandPrefix)
	mergeStr(&r.DoubleEscapeAction, overrides.DoubleEscapeAction)

	// Pointer fields: override wins if non-nil
	mergeBool(&r.HideThinkingBlock, overrides.HideThinkingBlock)
	mergeBool(&r.QuietStartup, overrides.QuietStartup)
	mergeBool(&r.CollapseChangelog, overrides.CollapseChangelog)
	mergeBool(&r.EnableSkillCommands, overrides.EnableSkillCommands)
	mergeBool(&r.ShowHardwareCursor, overrides.ShowHardwareCursor)
	mergeInt(&r.EditorPaddingX, overrides.EditorPaddingX)
	mergeInt(&r.AutocompleteMaxVisible, overrides.AutocompleteMaxVisible)

	// Slice fields: override wins if non-nil
	if overrides.Packages != nil {
		r.Packages = overrides.Packages
	}
	if overrides.Extensions != nil {
		r.Extensions = overrides.Extensions
	}
	if overrides.Skills != nil {
		r.Skills = overrides.Skills
	}
	if overrides.Prompts != nil {
		r.Prompts = overrides.Prompts
	}
	if overrides.Themes != nil {
		r.Themes = overrides.Themes
	}
	if overrides.EnabledModels != nil {
		r.EnabledModels = overrides.EnabledModels
	}

	// Nested struct pointers: merge field-by-field if both set, override wins if only override set
	if overrides.Compaction != nil {
		if r.Compaction == nil {
			r.Compaction = overrides.Compaction
		} else {
			c := *r.Compaction
			mergeBool(&c.Enabled, overrides.Compaction.Enabled)
			mergeInt(&c.ReserveTokens, overrides.Compaction.ReserveTokens)
			mergeInt(&c.KeepRecentTokens, overrides.Compaction.KeepRecentTokens)
			r.Compaction = &c
		}
	}
	if overrides.BranchSummary != nil {
		if r.BranchSummary == nil {
			r.BranchSummary = overrides.BranchSummary
		} else {
			b := *r.BranchSummary
			mergeInt(&b.ReserveTokens, overrides.BranchSummary.ReserveTokens)
			r.BranchSummary = &b
		}
	}
	if overrides.Retry != nil {
		if r.Retry == nil {
			r.Retry = overrides.Retry
		} else {
			rt := *r.Retry
			mergeBool(&rt.Enabled, overrides.Retry.Enabled)
			mergeInt(&rt.MaxRetries, overrides.Retry.MaxRetries)
			mergeInt(&rt.BaseDelayMs, overrides.Retry.BaseDelayMs)
			mergeInt(&rt.MaxDelayMs, overrides.Retry.MaxDelayMs)
			r.Retry = &rt
		}
	}
	if overrides.Terminal != nil {
		if r.Terminal == nil {
			r.Terminal = overrides.Terminal
		} else {
			t := *r.Terminal
			mergeBool(&t.ShowImages, overrides.Terminal.ShowImages)
			mergeBool(&t.ClearOnShrink, overrides.Terminal.ClearOnShrink)
			r.Terminal = &t
		}
	}
	if overrides.Images != nil {
		if r.Images == nil {
			r.Images = overrides.Images
		} else {
			img := *r.Images
			mergeBool(&img.AutoResize, overrides.Images.AutoResize)
			mergeBool(&img.BlockImages, overrides.Images.BlockImages)
			r.Images = &img
		}
	}
	if overrides.ThinkingBudgets != nil {
		if r.ThinkingBudgets == nil {
			r.ThinkingBudgets = overrides.ThinkingBudgets
		} else {
			tb := *r.ThinkingBudgets
			mergeInt(&tb.Minimal, overrides.ThinkingBudgets.Minimal)
			mergeInt(&tb.Low, overrides.ThinkingBudgets.Low)
			mergeInt(&tb.Medium, overrides.ThinkingBudgets.Medium)
			mergeInt(&tb.High, overrides.ThinkingBudgets.High)
			r.ThinkingBudgets = &tb
		}
	}
	if overrides.Markdown != nil {
		if r.Markdown == nil {
			r.Markdown = overrides.Markdown
		} else {
			md := *r.Markdown
			mergeStrPtr(&md.CodeBlockIndent, overrides.Markdown.CodeBlockIndent)
			r.Markdown = &md
		}
	}

	return r
}

// mergeStr overwrites dst with src if src is non-empty.
func mergeStr(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

// mergeBool overwrites dst with src if src is non-nil.
func mergeBool(dst **bool, src *bool) {
	if src != nil {
		*dst = src
	}
}

// mergeInt overwrites dst with src if src is non-nil.
func mergeInt(dst **int, src *int) {
	if src != nil {
		*dst = src
	}
}

// mergeStrPtr overwrites dst with src if src is non-nil.
func mergeStrPtr(dst **string, src *string) {
	if src != nil {
		*dst = src
	}
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
	// Note: legacy "websockets" boolean from upstream TS is silently ignored
	// by json.Unmarshal (unknown field). Transport defaults to "sse" via
	// GetTransport() if unset, so no explicit migration is needed.
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
	os.WriteFile(sm.settingsPath, resultJSON, 0600)

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

func (sm *SettingsManager) GetTransport() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Transport != "" {
		return sm.settings.Transport
	}
	return "sse"
}

func (sm *SettingsManager) SetTransport(transport string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.Transport = transport
	sm.markModified("transport")
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
