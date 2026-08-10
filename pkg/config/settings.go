// Ported from: packages/coding-agent/src/core/settings-manager.ts
// Upstream hash: 380236a0
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// DefaultMCPToolTimeout is the wall-clock timeout applied to a single MCP
// tool call dispatched by the model when no explicit override is configured.
// It bounds an unresponsive MCP server so a hung tools/call cannot hang the
// whole turn. The ecosystem norm (MCP TypeScript SDK) is 60s; fir uses a more
// conservative 120s so legitimately slow tools (browser automation, large
// fetches/queries) are not clipped. Overridable via settings.json
// (mcp.toolTimeoutSeconds) or the FIR_MCP_TOOL_TIMEOUT env var (seconds);
// a value <= 0 disables the bound entirely.
const DefaultMCPToolTimeout = 120 * time.Second

// CompactionSettings controls context compaction behavior.
type CompactionSettings struct {
	Enabled          *bool `json:"enabled,omitempty"`
	ReserveTokens    *int  `json:"reserveTokens,omitempty"`
	KeepRecentTokens *int  `json:"keepRecentTokens,omitempty"`
	MaxContextTokens *int  `json:"maxContextTokens,omitempty"`
}

// ServerCompactionSettings controls Anthropic server-side context compaction.
type ServerCompactionSettings struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	TriggerTokens *int   `json:"triggerTokens,omitempty"`
	Instructions  string `json:"instructions,omitempty"`
}

// BranchSummarySettings controls branch summary behavior.
type BranchSummarySettings struct {
	ReserveTokens *int `json:"reserveTokens,omitempty"`
}

// MCPSettings controls behaviour of MCP (Model Context Protocol) servers.
type MCPSettings struct {
	// ToolTimeoutSeconds bounds a single model-dispatched MCP tool call.
	//   nil / absent : use DefaultMCPToolTimeout (or FIR_MCP_TOOL_TIMEOUT).
	//   > 0          : that many seconds.
	//   <= 0         : disable the bound (call runs until it finishes or the
	//                  turn is cancelled).
	ToolTimeoutSeconds *int `json:"toolTimeoutSeconds,omitempty"`
}

// RetrySettings controls retry behavior.
type RetrySettings struct {
	Enabled     *bool `json:"enabled,omitempty"`
	MaxRetries  *int  `json:"maxRetries,omitempty"`
	BaseDelayMs *int  `json:"baseDelayMs,omitempty"`
	MaxDelayMs  *int  `json:"maxDelayMs,omitempty"`
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

// DebugLogSettings controls rotation of the debug log file (<agent-dir>/debug.log).
type DebugLogSettings struct {
	MaxSizeMB         *int  `json:"maxSizeMB,omitempty"`
	Keep              *int  `json:"keep,omitempty"`
	Compress          *bool `json:"compress,omitempty"`
	CheckEveryWrites  *int  `json:"checkEveryWrites,omitempty"`
	CheckEverySeconds *int  `json:"checkEverySeconds,omitempty"`
}

// Settings is the full settings schema.
type Settings struct {
	DefaultProvider        string                    `json:"defaultProvider,omitempty"`
	DefaultModel           string                    `json:"defaultModel,omitempty"`
	DefaultThinkingLevel   string                    `json:"defaultThinkingLevel,omitempty"`
	Transport              string                    `json:"transport,omitempty"`
	SteeringMode           string                    `json:"steeringMode,omitempty"`
	FollowUpMode           string                    `json:"followUpMode,omitempty"`
	Theme                  string                    `json:"theme,omitempty"`
	Compaction             *CompactionSettings       `json:"compaction,omitempty"`
	BranchSummary          *BranchSummarySettings    `json:"branchSummary,omitempty"`
	Retry                  *RetrySettings            `json:"retry,omitempty"`
	HideThinkingBlock      *bool                     `json:"hideThinkingBlock,omitempty"`
	ShellPath              string                    `json:"shellPath,omitempty"`
	ShellCommandPrefix     string                    `json:"shellCommandPrefix,omitempty"`
	CollapseChangelog      *bool                     `json:"collapseChangelog,omitempty"`
	Packages               []any                     `json:"packages,omitempty"`
	Extensions             []string                  `json:"extensions,omitempty"`
	ExtensionPaths         []string                  `json:"extensionPaths,omitempty"`
	Skills                 []string                  `json:"skills,omitempty"`
	Themes                 []string                  `json:"themes,omitempty"`
	EnableSkillCommands    *bool                     `json:"enableSkillCommands,omitempty"`
	EnableSysExtensions    *bool                     `json:"enableSysExtensions,omitempty"`
	Terminal               *TerminalSettings         `json:"terminal,omitempty"`
	Images                 *ImageSettings            `json:"images,omitempty"`
	EnabledModels          []string                  `json:"enabledModels,omitempty"`
	ThinkingBudgets        *ThinkingBudgetsSettings  `json:"thinkingBudgets,omitempty"`
	AutocompleteMaxVisible *int                      `json:"autocompleteMaxVisible,omitempty"`
	ShowHardwareCursor     *bool                     `json:"showHardwareCursor,omitempty"`
	Markdown               *MarkdownSettings         `json:"markdown,omitempty"`
	ServerTools            []string                  `json:"serverTools,omitempty"`
	ServerCompaction       *ServerCompactionSettings `json:"serverCompaction,omitempty"`
	DebugLog               *DebugLogSettings         `json:"debugLog,omitempty"`
	MCP                    *MCPSettings              `json:"mcp,omitempty"`
}

// deepMergeSettings merges overrides into base, with nested objects merged recursively.
// Arrays and primitives from overrides win over base. Nil pointers and empty strings
// in overrides are treated as "not set" and don't overwrite base values.
func deepMergeSettings(base, overrides Settings) Settings {
	r := base

	// Simple string fields: override wins if non-empty
	mergeStr(&r.DefaultProvider, overrides.DefaultProvider)
	mergeStr(&r.DefaultModel, overrides.DefaultModel)
	mergeStr(&r.DefaultThinkingLevel, overrides.DefaultThinkingLevel)
	mergeStr(&r.Transport, overrides.Transport)
	mergeStr(&r.SteeringMode, overrides.SteeringMode)
	mergeStr(&r.FollowUpMode, overrides.FollowUpMode)
	mergeStr(&r.Theme, overrides.Theme)
	mergeStr(&r.ShellPath, overrides.ShellPath)
	mergeStr(&r.ShellCommandPrefix, overrides.ShellCommandPrefix)

	// Pointer fields: override wins if non-nil
	mergeBool(&r.HideThinkingBlock, overrides.HideThinkingBlock)
	mergeBool(&r.CollapseChangelog, overrides.CollapseChangelog)
	mergeBool(&r.EnableSkillCommands, overrides.EnableSkillCommands)
	mergeBool(&r.EnableSysExtensions, overrides.EnableSysExtensions)
	mergeBool(&r.ShowHardwareCursor, overrides.ShowHardwareCursor)
	mergeInt(&r.AutocompleteMaxVisible, overrides.AutocompleteMaxVisible)

	// Slice fields: override wins if non-nil
	if overrides.Packages != nil {
		r.Packages = overrides.Packages
	}
	if overrides.Extensions != nil {
		r.Extensions = overrides.Extensions
	}
	if overrides.ExtensionPaths != nil {
		r.ExtensionPaths = overrides.ExtensionPaths
	}
	if overrides.Skills != nil {
		r.Skills = overrides.Skills
	}
	if overrides.Themes != nil {
		r.Themes = overrides.Themes
	}
	if overrides.EnabledModels != nil {
		r.EnabledModels = overrides.EnabledModels
	}
	if overrides.ServerTools != nil {
		r.ServerTools = overrides.ServerTools
	}
	if overrides.ServerCompaction != nil {
		if r.ServerCompaction == nil {
			r.ServerCompaction = overrides.ServerCompaction
		} else {
			sc := *r.ServerCompaction
			mergeBool(&sc.Enabled, overrides.ServerCompaction.Enabled)
			mergeInt(&sc.TriggerTokens, overrides.ServerCompaction.TriggerTokens)
			mergeStr(&sc.Instructions, overrides.ServerCompaction.Instructions)
			r.ServerCompaction = &sc
		}
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
			mergeInt(&c.MaxContextTokens, overrides.Compaction.MaxContextTokens)
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
	if overrides.DebugLog != nil {
		if r.DebugLog == nil {
			r.DebugLog = overrides.DebugLog
		} else {
			dl := *r.DebugLog
			mergeInt(&dl.MaxSizeMB, overrides.DebugLog.MaxSizeMB)
			mergeInt(&dl.Keep, overrides.DebugLog.Keep)
			mergeBool(&dl.Compress, overrides.DebugLog.Compress)
			mergeInt(&dl.CheckEveryWrites, overrides.DebugLog.CheckEveryWrites)
			mergeInt(&dl.CheckEverySeconds, overrides.DebugLog.CheckEverySeconds)
			r.DebugLog = &dl
		}
	}

	if overrides.MCP != nil {
		if r.MCP == nil {
			r.MCP = overrides.MCP
		} else {
			m := *r.MCP
			mergeInt(&m.ToolTimeoutSeconds, overrides.MCP.ToolTimeoutSeconds)
			r.MCP = &m
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

// SettingsScope identifies which settings file (global or project) is being operated on.
type SettingsScope = string

const (
	ScopeGlobal  SettingsScope = "global"
	ScopeProject SettingsScope = "project"
)

// SettingsError records an error that occurred in a specific settings scope.
type SettingsError struct {
	Scope SettingsScope
	Err   error
}

func (e *SettingsError) Error() string {
	return e.Scope + ": " + e.Err.Error()
}

// SettingsStorage abstracts the storage and locking mechanism for settings.
// fn receives the current JSON content (nil/empty if not found) and returns
// new content to write (nil/empty to skip write).
// WithLock returns an error if the storage backend cannot persist the updated content.
type SettingsStorage interface {
	WithLock(scope SettingsScope, fn func(current string) string) error
}

// FileSettingsStorage stores settings in JSON files on disk.
type FileSettingsStorage struct {
	mu                  sync.Mutex
	globalSettingsPath  string
	projectSettingsPath string
}

// NewFileSettingsStorage creates a file-backed storage for settings.
func NewFileSettingsStorage(cwd, agentDir string) *FileSettingsStorage {
	return &FileSettingsStorage{
		globalSettingsPath:  filepath.Join(agentDir, "settings.json"),
		projectSettingsPath: filepath.Join(cwd, ConfigDirName, "settings.json"),
	}
}

func (s *FileSettingsStorage) pathForScope(scope SettingsScope) string {
	if scope == ScopeGlobal {
		return s.globalSettingsPath
	}
	return s.projectSettingsPath
}

// PathForScope exposes the backing file for a scope so diagnostics can tell
// the operator exactly which file to edit.
func (s *FileSettingsStorage) PathForScope(scope SettingsScope) string {
	return s.pathForScope(scope)
}

func (s *FileSettingsStorage) WithLock(scope SettingsScope, fn func(current string) string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.pathForScope(scope)

	var current string
	if data, err := os.ReadFile(path); err == nil {
		current = string(data)
	}

	next := fn(current)
	if next != "" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(next), 0600); err != nil {
			return err
		}
	}
	return nil
}

// InMemorySettingsStorage stores settings in memory (no file I/O).
type InMemorySettingsStorage struct {
	mu      sync.Mutex
	global  string
	project string
}

func (s *InMemorySettingsStorage) WithLock(scope SettingsScope, fn func(current string) string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var current string
	if scope == ScopeGlobal {
		current = s.global
	} else {
		current = s.project
	}

	next := fn(current)
	if next != "" {
		if scope == ScopeGlobal {
			s.global = next
		} else {
			s.project = next
		}
	}
	return nil
}

// SettingsManager manages global and project settings with storage persistence.
type SettingsManager struct {
	mu sync.RWMutex

	storage               SettingsStorage
	globalSettings        Settings
	projectSettings       Settings
	settings              Settings
	modifiedFields        map[string]bool
	modifiedNested        map[string]map[string]bool
	modifiedProjectFields map[string]bool
	modifiedProjectNested map[string]map[string]bool
	globalLoadError       error
	projectLoadError      error
	errors                []SettingsError
}

// NewSettingsManager creates a SettingsManager that loads from files.
func NewSettingsManager(cwd, agentDir string) *SettingsManager {
	storage := NewFileSettingsStorage(cwd, agentDir)
	return newSettingsManagerFromStorage(storage)
}

// NewSettingsManagerFromStorage creates a SettingsManager backed by the given storage.
func NewSettingsManagerFromStorage(storage SettingsStorage) *SettingsManager {
	return newSettingsManagerFromStorage(storage)
}

// NewInMemorySettingsManager creates a SettingsManager with no file I/O.
func NewInMemorySettingsManager(initial Settings) *SettingsManager {
	storage := &InMemorySettingsStorage{}
	sm := &SettingsManager{
		storage:               storage,
		globalSettings:        initial,
		projectSettings:       Settings{},
		modifiedFields:        map[string]bool{},
		modifiedNested:        map[string]map[string]bool{},
		modifiedProjectFields: map[string]bool{},
		modifiedProjectNested: map[string]map[string]bool{},
	}
	sm.settings = deepMergeSettings(sm.globalSettings, sm.projectSettings)
	return sm
}

func newSettingsManagerFromStorage(storage SettingsStorage) *SettingsManager {
	sm := &SettingsManager{
		storage:               storage,
		modifiedFields:        map[string]bool{},
		modifiedNested:        map[string]map[string]bool{},
		modifiedProjectFields: map[string]bool{},
		modifiedProjectNested: map[string]map[string]bool{},
	}

	globalSettings, globalErr := loadSettingsFromStorage(storage, ScopeGlobal)
	sm.globalSettings = globalSettings
	if globalErr != nil {
		sm.globalLoadError = globalErr
		sm.recordError(ScopeGlobal, globalErr)
	}

	projectSettings, projectErr := loadSettingsFromStorage(storage, ScopeProject)
	sm.projectSettings = projectSettings
	if projectErr != nil {
		sm.projectLoadError = projectErr
		sm.recordError(ScopeProject, projectErr)
	}

	sm.settings = deepMergeSettings(sm.globalSettings, sm.projectSettings)
	return sm
}

func loadSettingsFromStorage(storage SettingsStorage, scope SettingsScope) (Settings, error) {
	var result Settings
	var parseErr error
	storage.WithLock(scope, func(current string) string { //nolint:errcheck // read-only: fn returns ""
		if current == "" {
			return ""
		}
		var s Settings
		if err := json.Unmarshal([]byte(current), &s); err != nil {
			parseErr = err
			return ""
		}
		result = migrateSettings(s)
		return ""
	})
	return result, parseErr
}

func migrateSettings(s Settings) Settings {
	// Note: legacy "websockets" boolean from upstream TS is silently ignored
	// by json.Unmarshal (unknown field). Transport defaults to "sse" via
	// GetTransport() if unset, so no explicit migration is needed.
	return s
}

func (sm *SettingsManager) recordError(scope SettingsScope, err error) {
	if err != nil {
		sm.errors = append(sm.errors, SettingsError{Scope: scope, Err: err})
	}
}

// DrainErrors returns and clears all accumulated settings errors.
func (sm *SettingsManager) DrainErrors() []SettingsError {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	drained := make([]SettingsError, len(sm.errors))
	copy(drained, sm.errors)
	sm.errors = sm.errors[:0]
	return drained
}

// Flush waits for any pending writes. In this synchronous implementation, writes
// complete immediately so Flush is a no-op.
func (sm *SettingsManager) Flush() {}

func (sm *SettingsManager) markModified(field string, nestedKeys ...string) {
	sm.modifiedFields[field] = true
	for _, nk := range nestedKeys {
		if sm.modifiedNested[field] == nil {
			sm.modifiedNested[field] = map[string]bool{}
		}
		sm.modifiedNested[field][nk] = true
	}
}

func (sm *SettingsManager) markProjectModified(field string, nestedKeys ...string) {
	sm.modifiedProjectFields[field] = true
	for _, nk := range nestedKeys {
		if sm.modifiedProjectNested[field] == nil {
			sm.modifiedProjectNested[field] = map[string]bool{}
		}
		sm.modifiedProjectNested[field][nk] = true
	}
}

func (sm *SettingsManager) persistScopedSettings(
	scope SettingsScope,
	snapshotSettings Settings,
	modifiedFields map[string]bool,
	modifiedNested map[string]map[string]bool,
) {
	err := sm.storage.WithLock(scope, func(current string) string {
		var currentFileSettings Settings
		if current != "" {
			json.Unmarshal([]byte(current), &currentFileSettings) //nolint:errcheck
			currentFileSettings = migrateSettings(currentFileSettings)
		}

		// Merge modified fields from snapshot onto current file settings
		currentJSON, _ := json.Marshal(currentFileSettings)
		snapshotJSON, _ := json.Marshal(snapshotSettings)
		var currentMap, snapshotMap map[string]any
		json.Unmarshal(currentJSON, &currentMap)   //nolint:errcheck
		json.Unmarshal(snapshotJSON, &snapshotMap) //nolint:errcheck
		if currentMap == nil {
			currentMap = map[string]any{}
		}

		for field := range modifiedFields {
			if nestedKeys, hasNested := modifiedNested[field]; hasNested {
				baseNested, _ := currentMap[field].(map[string]any)
				if baseNested == nil {
					baseNested = map[string]any{}
				}
				inMemNested, _ := snapshotMap[field].(map[string]any)
				for nk := range nestedKeys {
					if inMemNested != nil {
						baseNested[nk] = inMemNested[nk]
					}
				}
				currentMap[field] = baseNested
			} else {
				currentMap[field] = snapshotMap[field]
			}
		}

		result, _ := json.MarshalIndent(currentMap, "", "  ")
		return string(result)
	})
	sm.recordError(scope, err)
}

func (sm *SettingsManager) save() {
	sm.settings = deepMergeSettings(sm.globalSettings, sm.projectSettings)

	if sm.globalLoadError != nil {
		return
	}

	snapshotGlobal := deepCopySettings(sm.globalSettings)
	modifiedFields := copyStringBoolMap(sm.modifiedFields)
	modifiedNested := copyNestedMap(sm.modifiedNested)

	sm.persistScopedSettings(ScopeGlobal, snapshotGlobal, modifiedFields, modifiedNested)
}

func (sm *SettingsManager) saveProjectSettings(settings Settings) {
	sm.projectSettings = deepCopySettings(settings)
	sm.settings = deepMergeSettings(sm.globalSettings, sm.projectSettings)

	if sm.projectLoadError != nil {
		return
	}

	snapshotProject := deepCopySettings(sm.projectSettings)
	modifiedFields := copyStringBoolMap(sm.modifiedProjectFields)
	modifiedNested := copyNestedMap(sm.modifiedProjectNested)

	sm.persistScopedSettings(ScopeProject, snapshotProject, modifiedFields, modifiedNested)
}

func (sm *SettingsManager) remerge() {
	sm.settings = deepMergeSettings(sm.globalSettings, sm.projectSettings)
}

// deepCopySettings makes a deep copy of settings via JSON round-trip.
func deepCopySettings(s Settings) Settings {
	data, _ := json.Marshal(s)
	var result Settings
	json.Unmarshal(data, &result) //nolint:errcheck
	return result
}

func copyStringBoolMap(m map[string]bool) map[string]bool {
	result := make(map[string]bool, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

func copyNestedMap(m map[string]map[string]bool) map[string]map[string]bool {
	result := make(map[string]map[string]bool, len(m))
	for k, v := range m {
		inner := make(map[string]bool, len(v))
		for ik, iv := range v {
			inner[ik] = iv
		}
		result[k] = inner
	}
	return result
}

// Reload re-reads settings from storage.
func (sm *SettingsManager) Reload() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	globalSettings, globalErr := loadSettingsFromStorage(sm.storage, ScopeGlobal)
	if globalErr == nil {
		sm.globalSettings = globalSettings
		sm.globalLoadError = nil
	} else {
		sm.globalLoadError = globalErr
		sm.recordError(ScopeGlobal, globalErr)
	}

	projectSettings, projectErr := loadSettingsFromStorage(sm.storage, ScopeProject)
	if projectErr == nil {
		sm.projectSettings = projectSettings
		sm.projectLoadError = nil
	} else {
		sm.projectLoadError = projectErr
		sm.recordError(ScopeProject, projectErr)
	}

	sm.modifiedFields = map[string]bool{}
	sm.modifiedNested = map[string]map[string]bool{}
	sm.modifiedProjectFields = map[string]bool{}
	sm.modifiedProjectNested = map[string]map[string]bool{}
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

// DefaultModelPin reports the effective defaultProvider/defaultModel pin and
// which settings file supplies it — the project file when it sets
// defaultModel, otherwise the global one. Path is "" for storage backends
// that are not file-backed (tests, embedders).
//
// Provenance matters because a diagnostic about a stale pin is useless unless
// it names the file to edit. defaultProvider and defaultModel merge
// independently, so a project file setting only defaultProvider reports the
// global scope — correct, because defaultModel is the key to edit.
func (sm *SettingsManager) DefaultModelPin() (provider, modelID string, scope SettingsScope, path string) {
	sm.mu.RLock()
	provider, modelID = sm.settings.DefaultProvider, sm.settings.DefaultModel
	scope = ScopeGlobal
	if sm.projectSettings.DefaultModel != "" {
		scope = ScopeProject
	}
	sm.mu.RUnlock()

	if p, ok := sm.storage.(interface {
		PathForScope(SettingsScope) string
	}); ok {
		path = p.PathForScope(scope)
	}
	return provider, modelID, scope, path
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

// GetCompactionMaxContextTokens returns the hard token cap (0 = disabled).
func (sm *SettingsManager) GetCompactionMaxContextTokens() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.Compaction != nil && sm.settings.Compaction.MaxContextTokens != nil {
		return *sm.settings.Compaction.MaxContextTokens
	}
	return 0
}

// SetCompactionMaxContextTokens sets the hard token cap. 0 disables the cap.
func (sm *SettingsManager) SetCompactionMaxContextTokens(tokens int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.globalSettings.Compaction == nil {
		sm.globalSettings.Compaction = &CompactionSettings{}
	}
	sm.globalSettings.Compaction.MaxContextTokens = &tokens
	sm.markModified("compaction", "maxContextTokens")
	sm.save()
}

type CompactionResult struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
	MaxContextTokens *int
}

func (sm *SettingsManager) GetCompactionSettings() CompactionResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	r := CompactionResult{
		Enabled:          true,
		ReserveTokens:    16384,
		KeepRecentTokens: 20000,
	}
	if c := sm.settings.Compaction; c != nil {
		r.Enabled = boolDefault(c.Enabled, true)
		r.ReserveTokens = intDefault(c.ReserveTokens, 16384)
		r.KeepRecentTokens = intDefault(c.KeepRecentTokens, 20000)
		r.MaxContextTokens = c.MaxContextTokens
	}
	return r
}

type RetryResult struct {
	Enabled     bool
	MaxRetries  int
	BaseDelayMs int
	MaxDelayMs  int
}

// DebugLogResult holds resolved debug-log rotation settings.
type DebugLogResult struct {
	MaxSizeMB         int
	Keep              int
	Compress          bool
	CheckEveryWrites  int
	CheckEverySeconds int
}

// GetDebugLogSettings returns the resolved debug-log rotation settings,
// applying defaults for any unset keys.
func (sm *SettingsManager) GetDebugLogSettings() DebugLogResult {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	r := DebugLogResult{
		MaxSizeMB:         100,
		Keep:              20,
		Compress:          true,
		CheckEveryWrites:  1000,
		CheckEverySeconds: 30,
	}
	if d := sm.settings.DebugLog; d != nil {
		r.MaxSizeMB = intDefault(d.MaxSizeMB, r.MaxSizeMB)
		r.Keep = intDefault(d.Keep, r.Keep)
		r.Compress = boolDefault(d.Compress, r.Compress)
		r.CheckEveryWrites = intDefault(d.CheckEveryWrites, r.CheckEveryWrites)
		r.CheckEverySeconds = intDefault(d.CheckEverySeconds, r.CheckEverySeconds)
	}
	return r
}

func (sm *SettingsManager) SetHideThinkingBlock(hide bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.HideThinkingBlock = &hide
	sm.markModified("hideThinkingBlock")
	sm.save()
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

// GetMCPToolTimeout returns the wall-clock bound for a single model-dispatched
// MCP tool call. Precedence: FIR_MCP_TOOL_TIMEOUT env (seconds) > settings.json
// mcp.toolTimeoutSeconds > DefaultMCPToolTimeout. A configured value <= 0
// disables the bound and returns 0 (call runs until it finishes or the turn is
// cancelled).
func (sm *SettingsManager) GetMCPToolTimeout() time.Duration {
	if v := os.Getenv("FIR_MCP_TOOL_TIMEOUT"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil {
			if secs <= 0 {
				return 0
			}
			return time.Duration(secs * float64(time.Second))
		}
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.MCP != nil && sm.settings.MCP.ToolTimeoutSeconds != nil {
		secs := *sm.settings.MCP.ToolTimeoutSeconds
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	return DefaultMCPToolTimeout
}

func (sm *SettingsManager) GetEnabledExtensions() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Extensions))
	copy(out, sm.settings.Extensions)
	return out
}

func (sm *SettingsManager) GetThemePaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Themes))
	copy(out, sm.settings.Themes)
	return out
}

// GetExtensionPaths returns extra extension search directories from settings.
func (sm *SettingsManager) GetExtensionPaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.ExtensionPaths))
	copy(out, sm.settings.ExtensionPaths)
	return out
}

// GetSkillPaths returns extra skill directories/files from settings.
func (sm *SettingsManager) GetSkillPaths() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]string, len(sm.settings.Skills))
	copy(out, sm.settings.Skills)
	return out
}

func (sm *SettingsManager) GetEnableSkillCommands() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return boolDefault(sm.settings.EnableSkillCommands, true)
}

func (sm *SettingsManager) SetEnableSkillCommands(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.EnableSkillCommands = &enabled
	sm.markModified("enableSkillCommands")
	sm.save()
}

func (sm *SettingsManager) GetEnableSysExtensions() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return boolDefault(sm.settings.EnableSysExtensions, true)
}

func (sm *SettingsManager) SetEnableSysExtensions(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.EnableSysExtensions = &enabled
	sm.markModified("enableSysExtensions")
	sm.save()
}

func (sm *SettingsManager) GetThinkingBudgets() *ThinkingBudgetsSettings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.ThinkingBudgets
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

func (sm *SettingsManager) GetAutocompleteMaxVisible() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return intDefault(sm.settings.AutocompleteMaxVisible, 5)
}

func (sm *SettingsManager) GetShowHardwareCursor() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.settings.ShowHardwareCursor != nil {
		return *sm.settings.ShowHardwareCursor
	}
	return os.Getenv("FIR_HARDWARE_CURSOR") == "1"
}

// GetGlobalSettings returns a copy of global settings.
func (sm *SettingsManager) GetGlobalSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return deepCopySettings(sm.globalSettings)
}

// GetProjectSettings returns a copy of project settings.
func (sm *SettingsManager) GetProjectSettings() Settings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return deepCopySettings(sm.projectSettings)
}

// GetGlobalPackages returns a copy of the global packages list.
func (sm *SettingsManager) GetGlobalPackages() []any {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]any, len(sm.settings.Packages))
	copy(out, sm.settings.Packages)
	return out
}

// GetProjectPackages returns a copy of the project-scoped packages list.
func (sm *SettingsManager) GetProjectPackages() []any {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]any, len(sm.projectSettings.Packages))
	copy(out, sm.projectSettings.Packages)
	return out
}

// SetGlobalPackages sets the packages setting in the global settings file.
func (sm *SettingsManager) SetGlobalPackages(packages []any) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.Packages = packages
	sm.markModified("packages")
	sm.save()
}

// SetProjectPackages sets the packages setting in the project settings file.
func (sm *SettingsManager) SetProjectPackages(packages []any) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	projectSettings := deepCopySettings(sm.projectSettings)
	projectSettings.Packages = packages
	sm.markProjectModified("packages")
	sm.saveProjectSettings(projectSettings)
}

// SetProjectEnabledExtensions sets the extensions allowlist in the project settings file.
func (sm *SettingsManager) SetProjectEnabledExtensions(paths []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	projectSettings := deepCopySettings(sm.projectSettings)
	projectSettings.Extensions = paths
	sm.markProjectModified("extensions")
	sm.saveProjectSettings(projectSettings)
}

// SetProjectExtensionPaths sets the extensionPaths setting in the project settings file.
func (sm *SettingsManager) SetProjectExtensionPaths(paths []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	projectSettings := deepCopySettings(sm.projectSettings)
	projectSettings.ExtensionPaths = paths
	sm.markProjectModified("extensionPaths")
	sm.saveProjectSettings(projectSettings)
}

// SetProjectSkillPaths sets the skills setting in the project settings file.
func (sm *SettingsManager) SetProjectSkillPaths(paths []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	projectSettings := deepCopySettings(sm.projectSettings)
	projectSettings.Skills = paths
	sm.markProjectModified("skills")
	sm.saveProjectSettings(projectSettings)
}

// SetProjectThemePaths sets the themes setting in the project settings file.
func (sm *SettingsManager) SetProjectThemePaths(paths []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	projectSettings := deepCopySettings(sm.projectSettings)
	projectSettings.Themes = paths
	sm.markProjectModified("themes")
	sm.saveProjectSettings(projectSettings)
}

// GetServerTools returns the configured server-side tool types.
// Valid values: "web_search", "code_execution", "programmatic_tool_calling".
// For Anthropic: maps to native server tools (web_search, code_execution, etc.).
// For OpenAI Responses API: "code_execution" maps to the hosted shell tool.
func (sm *SettingsManager) GetServerTools() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.ServerTools
}

// SetServerTools sets the server-side tool types.
func (sm *SettingsManager) SetServerTools(tools []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.globalSettings.ServerTools = tools
	sm.markModified("serverTools")
	sm.save()
}

// GetServerCompaction returns the Anthropic server-side compaction settings.
func (sm *SettingsManager) GetServerCompaction() *ServerCompactionSettings {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.settings.ServerCompaction
}

// SetServerCompactionEnabled enables or disables Anthropic server-side compaction.
func (sm *SettingsManager) SetServerCompactionEnabled(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.globalSettings.ServerCompaction == nil {
		sm.globalSettings.ServerCompaction = &ServerCompactionSettings{}
	}
	sm.globalSettings.ServerCompaction.Enabled = &enabled
	sm.markModified("serverCompaction")
	sm.save()
}
