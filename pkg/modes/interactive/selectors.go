// selectors.go — selector overlays: model, thinking, theme, settings, session,
// OAuth, scoped-models, tree, fork, and user-message selectors.
package interactive

import (
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// ============================================================================
// Selector overlay pattern
// ============================================================================

// showSelector replaces the editor with a selector component, then restores editor when done.
func (m *InteractiveMode) showSelector(create func(done func()) (component tui.Component, focus tui.Component)) {
	done := func() {
		m.editorContainer.Clear()
		m.editorContainer.AddChild(m.editor)
		m.ui.SetFocus(m.editor)
		m.ui.RequestRender(false)
	}
	component, focus := create(done)
	m.editorContainer.Clear()
	m.editorContainer.AddChild(component)
	m.ui.SetFocus(focus)
	m.ui.RequestRender(true)
}

// ============================================================================
// Model selector
// ============================================================================

func (m *InteractiveMode) showModelSelector(initialSearch string) {
	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		// Convert ScopedModels to component format
		scopedModels := m.session.ScopedModelsRef()
		scopedItems := make([]components.ScopedModelItem, len(scopedModels))
		for i, sm := range scopedModels {
			scopedItems[i] = components.ScopedModelItem{
				Model:         sm.Model,
				ThinkingLevel: sm.ThinkingLevel,
			}
		}

		selector := components.NewModelSelectorComponent(
			m.session.Model(),
			m.settings,
			m.session.ModelRegistryRef(),
			scopedItems,
			func(model *ai.Model) {
				m.session.SetModel(model)
				m.footerComponent.Invalidate()
				m.updateEditorBorderColor()
				done()
				m.showStatus(fmt.Sprintf("Model: %s", model.ID))
			},
			func() {
				done()
			},
			initialSearch,
		)
		return selector, selector
	})
}

// ============================================================================
// Thinking selector
// ============================================================================

func (m *InteractiveMode) showThinkingSelector() {
	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		levels := m.session.GetAvailableThinkingLevels()
		currentLevel := agent.ThinkingLevel(m.session.ThinkingLevel())

		selector := components.NewThinkingSelectorComponent(
			currentLevel,
			levels,
			func(level agent.ThinkingLevel) {
				m.session.SetThinkingLevel(string(level))
				m.settings.SetDefaultThinkingLevel(string(level))
				m.footerComponent.Invalidate()
				done()
				m.showStatus(fmt.Sprintf("Thinking: %s", level))
			},
			func() {
				done()
			},
		)
		return selector, selector.GetSelectList()
	})
}

// ============================================================================
// Theme selector
// ============================================================================

func (m *InteractiveMode) showThemeSelector() {
	currentTheme := m.settings.GetTheme()
	if currentTheme == "" {
		currentTheme = "dark"
	}

	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		selector := components.NewThemeSelectorComponent(
			currentTheme,
			m.themeSearchDirs,
			func(themeName string) {
				_ = itheme.InitTheme(themeName, m.themeSearchDirs)
				m.settings.SetTheme(themeName)
				m.markdownTheme = itheme.GetMarkdownTheme()
				m.footerComponent.Invalidate()
				done()
				m.showStatus(fmt.Sprintf("Theme: %s", themeName))
			},
			func() {
				done()
			},
			func(themeName string) {
				// Live preview
				_ = itheme.InitTheme(themeName, m.themeSearchDirs)
				m.markdownTheme = itheme.GetMarkdownTheme()
				m.messageContainer.Invalidate()
				m.ui.RequestRender(true)
			},
		)
		return selector, selector.GetSelectList()
	})
}

// ============================================================================
// Settings selector
// ============================================================================

func (m *InteractiveMode) showSettingsSelector() {
	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		// Build available thinking levels from session
		availLevels := m.session.GetAvailableThinkingLevels()
		levelStrs := make([]string, len(availLevels))
		for i, l := range availLevels {
			levelStrs[i] = string(l)
		}

		config := components.SettingsConfig{
			AutoCompactMode:         m.autoCompactMode,
			MaxContextTokens:        m.settings.GetCompactionMaxContextTokens(),
			HideThinkingBlock:       m.hideThinking,
			ThinkingLevel:           m.session.ThinkingLevel(),
			AvailableThinkingLevels: levelStrs,
			CurrentTheme:            itheme.GetTheme().Name,
			AvailableThemes:         itheme.GetAvailableThemes(m.themeSearchDirs),
			SteeringMode:            m.settings.GetSteeringMode(),
			FollowUpMode:            m.settings.GetFollowUpMode(),
			Transport:               m.settings.GetTransport(),
			ServerToolWebSearch:     serverToolsHas(m.settings.GetServerTools(), "web_search"),
			ServerToolWebFetch:      serverToolsHas(m.settings.GetServerTools(), "web_fetch"),
			ServerToolCodeExec:      serverToolsHas(m.settings.GetServerTools(), "code_execution"),
			EnableSkillCommands:     m.settings.GetEnableSkillCommands(),
			AutocompleteMaxVisible:  10,
		}
		callbacks := components.SettingsCallbacks{
			OnAutoCompactModeChange: func(mode string) {
				m.autoCompactMode = mode
				switch mode {
				case "off":
					m.settings.SetCompactionEnabled(false)
					m.settings.SetServerCompactionEnabled(false)
					m.session.Agent.SetCompaction(nil)
				case "client":
					m.settings.SetCompactionEnabled(true)
					m.settings.SetServerCompactionEnabled(false)
					m.session.Agent.SetCompaction(nil)
				case "server":
					m.settings.SetCompactionEnabled(true) // fallback
					m.settings.SetServerCompactionEnabled(true)
					m.session.Agent.SetCompaction(&ai.AnthropicCompaction{Enabled: true})
				}
			},
			OnHideThinkingBlockChange: func(v bool) { m.hideThinking = v },
			OnEnableSkillCommandsChange: func(v bool) {
				m.settings.SetEnableSkillCommands(v)
				m.setupAutocomplete()
			},
			OnThinkingLevelChange: func(level string) {
				m.session.SetThinkingLevel(level)
				m.settings.SetDefaultThinkingLevel(level)
				m.footerComponent.Invalidate()
			},
			OnSteeringModeChange: func(mode string) {
				m.settings.SetSteeringMode(mode)
			},
			OnFollowUpModeChange: func(mode string) {
				m.settings.SetFollowUpMode(mode)
			},
			OnTransportChange: func(transport string) {
				m.settings.SetTransport(transport)
				m.session.Agent.SetTransport(ai.Transport(transport))
			},
			OnServerToolsChange: func(names []string) {
				m.settings.SetServerTools(names)
				m.session.Agent.SetServerTools(session.ResolveServerTools(names))
			},
			OnCancel: func() { done() },
			OnMaxContextTokensChange: func(tokens int) {
				m.settings.SetCompactionMaxContextTokens(tokens)
			},
		}
		selector := components.NewSettingsSelectorComponent(config, callbacks)
		return selector, selector
	})
}

// ============================================================================
// Server tools helpers
// ============================================================================

// serverToolsHas returns whether a tool name is in the configured list.
func serverToolsHas(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// ============================================================================
// Session selector
// ============================================================================

func (m *InteractiveMode) showSessionSelector() {
	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		var cwd, sessionDir string
		if m.session != nil && m.session.SessionManager != nil {
			cwd = m.session.SessionManager.GetCwd()
			sessionDir = m.session.SessionManager.GetSessionDir()
		}
		sessions, _ := store.ListSessions(cwd, sessionDir)
		selector := components.NewSessionSelectorComponent(
			sessions,
			components.SessionScopeCurrent,
			func() ([]store.SessionListInfo, error) {
				return store.ListAllSessions(session.DefaultAgentDir(), session.PiAgentDir())
			},
			func(sessionPath string) {
				done()
				go m.handleResumeSession(sessionPath)
			},
			func() {
				done()
			},
		)
		// Force a full redraw on scope toggle to prevent differential
		// rendering artifacts when content changes dramatically.
		selector.OnRequestRedraw = func() {
			m.ui.RequestRender(true)
		}
		return selector, selector
	})
}

func (m *InteractiveMode) handleResumeSession(sessionPath string) {
	// Stop loading animation
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
		m.loadingAnimation = nil
	}
	m.activityContainer.Clear()

	// Clear streaming state
	m.streamingComponent = nil
	m.pendingTools = make(map[string]*components.ToolExecutionComponent)

	// Switch session
	if err := m.session.SwitchSession(sessionPath); err != nil {
		m.showWarning(fmt.Sprintf("Failed to resume session: %s", err))
		return
	}

	// Rebuild the chat display
	m.rebuildChatFromMessages()
	m.footerComponent.Invalidate()
	m.showStatus("Resumed session")
}

// ============================================================================
// OAuth / login / logout
// ============================================================================

func (m *InteractiveMode) showOAuthSelector(mode string) {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}

	registry := m.session.ModelRegistryRef()
	if registry == nil {
		m.showWarning("Model registry not available")
		return
	}
	authStorage := registry.AuthStorage()
	if authStorage == nil {
		m.showWarning("Auth storage not available")
		return
	}

	if mode == "logout" {
		// Show only providers that are logged in via OAuth
		providers := authStorage.List()
		var loggedIn []string
		for _, p := range providers {
			if cred := authStorage.Get(p); cred != nil && cred.Type == auth.CredentialTypeOAuth {
				loggedIn = append(loggedIn, p)
			}
		}
		if len(loggedIn) == 0 {
			m.showStatus("No OAuth providers logged in. Use /login first.")
			return
		}

		selectItems := make([]tuicomp.SelectItem, len(loggedIn))
		for i, id := range loggedIn {
			name := id
			if p := oauth.GetProvider(id); p != nil {
				name = p.Name()
			}
			selectItems[i] = tuicomp.SelectItem{Label: name, Value: id, Description: "logged in"}
		}

		m.showSelector(func(done func()) (tui.Component, tui.Component) {
			list := tuicomp.NewSelectList(selectItems, 10, itheme.GetSelectListTheme())
			list.OnSelect = func(item tuicomp.SelectItem) {
				done()
				providerName := item.Value
				if p := oauth.GetProvider(item.Value); p != nil {
					providerName = p.Name()
				}
				if err := authStorage.Logout(item.Value); err != nil {
					m.showWarning(fmt.Sprintf("Logout failed: %v", err))
					return
				}
				registry.Refresh()
				m.showStatus(fmt.Sprintf("Logged out of %s", providerName))
			}
			list.OnCancel = func() { done() }
			return list, list
		})
		return
	}

	// Login mode — show all available OAuth providers
	oauthProviders := oauth.GetProviders()
	if len(oauthProviders) == 0 {
		m.showWarning("No OAuth providers available")
		return
	}

	selectItems := make([]tuicomp.SelectItem, len(oauthProviders))
	for i, p := range oauthProviders {
		desc := ""
		if cred := authStorage.Get(p.ID()); cred != nil && cred.Type == auth.CredentialTypeOAuth {
			desc = "logged in"
		}
		selectItems[i] = tuicomp.SelectItem{Label: p.Name(), Value: p.ID(), Description: desc}
	}

	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		list := tuicomp.NewSelectList(selectItems, 10, itheme.GetSelectListTheme())
		list.OnSelect = func(item tuicomp.SelectItem) {
			done()
			go m.performOAuthLogin(item.Value)
		}
		list.OnCancel = func() { done() }
		return list, list
	})
}

func (m *InteractiveMode) performOAuthLogin(providerID string) {
	providerName := providerID
	if p := oauth.GetProvider(providerID); p != nil {
		providerName = p.Name()
	}

	registry := m.session.ModelRegistryRef()
	if registry == nil {
		m.showWarning("Model registry not available")
		return
	}
	authStorage := registry.AuthStorage()
	if authStorage == nil {
		m.showWarning("Auth storage not available")
		return
	}

	promptUser := func(prompt oauth.Prompt) (string, error) {
		type promptResult struct {
			value string
			err   error
		}
		ch := make(chan promptResult, 1)

		// Show an input dialog in the editor container.
		// This must run on the "UI" path (modify TUI state then request render).
		done := func() {
			m.editorContainer.Clear()
			m.editorContainer.AddChild(m.editor)
			m.ui.SetFocus(m.editor)
			m.ui.RequestRender(false)
		}

		t := itheme.GetTheme()
		container := &tui.Container{}
		container.AddChild(components.NewDynamicBorder(nil))
		container.AddChild(tuicomp.NewText(t.Fg("warning", prompt.Message), 1, 0, nil))
		if prompt.Placeholder != "" {
			container.AddChild(tuicomp.NewText(t.Fg("muted", "(e.g. "+prompt.Placeholder+")"), 1, 0, nil))
		}
		if prompt.AllowEmpty {
			container.AddChild(tuicomp.NewText(t.Fg("muted", "Press Enter to skip"), 1, 0, nil))
		}

		input := tuicomp.NewInput()
		input.OnSubmit = func(value string) {
			if !prompt.AllowEmpty && strings.TrimSpace(value) == "" {
				return // don't accept empty when not allowed
			}
			done()
			ch <- promptResult{value: value}
		}
		input.OnEscape = func() {
			done()
			ch <- promptResult{err: fmt.Errorf("Login cancelled")}
		}
		container.AddChild(input)
		container.AddChild(tuicomp.NewSpacer(1))
		container.AddChild(components.NewDynamicBorder(nil))

		m.editorContainer.Clear()
		m.editorContainer.AddChild(container)
		m.ui.SetFocus(input)
		m.ui.RequestRender(true)

		// Block until user submits or cancels.
		result := <-ch
		return result.value, result.err
	}

	callbacks := oauth.LoginCallbacks{
		OnAuth: func(info oauth.AuthInfo) {
			// Try to auto-open the browser.
			browserOpened := session.OpenBrowser(info.URL) == nil

			// Show a clickable OSC 8 hyperlink so the URL stays on one line.
			link := session.Hyperlink(info.URL, info.URL)
			var msg string
			if browserOpened {
				msg = fmt.Sprintf("Opening browser… if it doesn't appear, visit:\n%s", link)
			} else {
				msg = fmt.Sprintf("Open this URL to authenticate:\n%s", link)
			}
			if info.Instructions != "" {
				msg += "\n" + info.Instructions
			}
			m.showMessage(msg)
		},
		OnPrompt: promptUser,
		OnManualCodeInput: func() (string, error) {
			return promptUser(oauth.Prompt{
				Message: "Paste the redirect URL or authorization code from your browser:",
			})
		},
		OnProgress: func(message string) {
			m.showStatus(message)
		},
	}

	err := authStorage.Login(providerID, callbacks)
	if err != nil {
		errMsg := err.Error()
		if errMsg != "Login cancelled" {
			m.showWarning(fmt.Sprintf("Failed to login to %s: %s", providerName, errMsg))
		}
		return
	}

	registry.Refresh()
	m.showStatus(fmt.Sprintf("Logged in to %s. Credentials saved.", providerName))
}

// ============================================================================
// Scoped models selector
// ============================================================================

func (m *InteractiveMode) showScopedModelsSelector() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	registry := m.session.ModelRegistryRef()
	if registry == nil {
		m.showWarning("Model registry not available")
		return
	}
	allModels := registry.GetAvailable()
	if len(allModels) == 0 {
		m.showStatus("No models available")
		return
	}

	// Build initial enabled-model set from session scoped models or settings.
	enabledModelIDs := make(map[string]bool)
	hasFilter := false
	sessionScopedModels := m.session.ScopedModelsRef()
	if len(sessionScopedModels) > 0 {
		for _, sm := range sessionScopedModels {
			enabledModelIDs[sm.Model.Provider+"/"+sm.Model.ID] = true
		}
		hasFilter = true
	} else if patterns := m.settings.GetEnabledModels(); len(patterns) > 0 {
		hasFilter = true
		for _, sm := range models.ResolveModelScope(patterns, registry) {
			enabledModelIDs[sm.Model.Provider+"/"+sm.Model.ID] = true
		}
	}

	// Working copies mutated by callbacks while the selector is open.
	currentEnabledIDs := make(map[string]bool, len(enabledModelIDs))
	for k := range enabledModelIDs {
		currentEnabledIDs[k] = true
	}
	currentHasFilter := hasFilter

	// applyToSession converts currentEnabledIDs back to scoped-model objects
	// and updates the live session (session-only, not persisted).
	applyToSession := func() {
		if !currentHasFilter || len(currentEnabledIDs) == 0 || len(currentEnabledIDs) >= len(allModels) {
			m.session.SetScopedModels(nil)
			m.ui.RequestRender(false)
			return
		}
		patterns := make([]string, 0, len(currentEnabledIDs))
		for id := range currentEnabledIDs {
			patterns = append(patterns, id)
		}
		resolved := models.ResolveModelScope(patterns, registry)
		currentThinkingLevel := m.session.ThinkingLevel()
		scoped := make([]models.ScopedModel, len(resolved))
		for i, sm := range resolved {
			level := sm.ThinkingLevel
			if level == "" {
				level = currentThinkingLevel
			}
			scoped[i] = models.ScopedModel{Model: sm.Model, ThinkingLevel: level}
		}
		m.session.SetScopedModels(scoped)
		m.ui.RequestRender(false)
	}

	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		selector := components.NewScopedModelsSelectorComponent(
			components.ScopedModelsConfig{
				AllModels:              allModels,
				EnabledModelIDs:        currentEnabledIDs,
				HasEnabledModelsFilter: currentHasFilter,
			},
			components.ScopedModelsCallbacks{
				OnModelToggle: func(modelID string, enabled bool) {
					if enabled {
						currentEnabledIDs[modelID] = true
					} else {
						delete(currentEnabledIDs, modelID)
					}
					currentHasFilter = true
					applyToSession()
				},
				OnEnableAll: func(allModelIDs []string) {
					for k := range currentEnabledIDs {
						delete(currentEnabledIDs, k)
					}
					for _, id := range allModelIDs {
						currentEnabledIDs[id] = true
					}
					currentHasFilter = false
					applyToSession()
				},
				OnClearAll: func() {
					for k := range currentEnabledIDs {
						delete(currentEnabledIDs, k)
					}
					currentHasFilter = true
					applyToSession()
				},
				OnToggleProvider: func(_ string, modelIDs []string, enabled bool) {
					for _, id := range modelIDs {
						if enabled {
							currentEnabledIDs[id] = true
						} else {
							delete(currentEnabledIDs, id)
						}
					}
					currentHasFilter = true
					applyToSession()
				},
				OnPersist: func(enabledIDs []string) {
					if len(enabledIDs) >= len(allModels) {
						m.settings.SetEnabledModels(nil) // all enabled = clear filter
					} else {
						m.settings.SetEnabledModels(enabledIDs)
					}
					m.showStatus("Model selection saved to settings")
				},
				OnCancel: func() {
					done()
					m.ui.RequestRender(false)
				},
			},
		)
		return selector, selector
	})
}

// ============================================================================
// Tree / fork / user message selectors
// ============================================================================

func (m *InteractiveMode) showTreeSelector() {
	m.showTreeSelectorAt("")
}

// showTreeSelectorAt shows the interactive session-tree selector, optionally
// pre-selecting a specific entry (used when re-opening after an action).
func (m *InteractiveMode) showTreeSelectorAt(initialSelectedID string) {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	tree := m.session.SessionManager.GetTree()
	if len(tree) == 0 {
		m.showStatus("No entries in session")
		return
	}

	leafID := m.session.SessionManager.GetLeafID()

	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		selector := components.NewTreeSelectorComponent(
			tree,
			leafID,
			func(entryID string) {
				done()
				if entryID == leafID {
					m.showStatus("Already at this point")
					return
				}
				go m.handleTreeNavigation(entryID)
			},
			func() { done() },
		)
		selector.SetOnLabelEdit(func(entryID, label string) {
			if m.session != nil {
				m.session.SessionManager.AppendLabelChange(entryID, label)
				m.ui.RequestRender(false)
			}
		})
		if initialSelectedID != "" {
			selector.SetInitialSelection(initialSelectedID)
		}
		return selector, selector
	})
}

// handleTreeNavigation navigates to entryID and refreshes the chat display.
func (m *InteractiveMode) handleTreeNavigation(entryID string) {
	result, err := m.session.NavigateTree(entryID, false, "")
	if err != nil {
		m.showWarning(fmt.Sprintf("Navigation failed: %s", err))
		return
	}
	if result.Cancelled {
		m.showStatus("Navigation cancelled")
		return
	}
	m.rebuildChatFromMessages()
	if m.editor != nil && result.EditorText != "" && strings.TrimSpace(m.editor.GetText()) == "" {
		m.editor.SetText(result.EditorText)
	}
	m.showStatus("Navigated to selected point")
}
