// Ported from: packages/coding-agent/src/modes/interactive/interactive-mode.ts
// Upstream hash: 1caadb2e
//
// This is the main interactive mode for the coding agent TUI.
// It manages the TUI lifecycle, renders messages, handles user input,
// and delegates business logic to AgentSession.
package interactive

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/modes/interactive/components"
	itheme "github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// InteractiveMode manages the interactive TUI session.
type InteractiveMode struct {
	mu sync.Mutex

	// Core dependencies
	session     *core.AgentSession
	keybindings *core.KeybindingsManager
	settings    *core.SettingsManager

	// TUI
	ui              *tui.TUI
	editor          *components.CustomEditor
	editorContainer *tui.Container // holds editor or selector overlay

	// State
	messageContainer *tui.Container
	statusContainer  *tui.Container
	footerComponent  *components.FooterComponent
	markdownTheme    tuicomp.MarkdownTheme

	// Streaming state
	streamingComponent *components.AssistantMessageComponent
	pendingTools       map[string]*components.ToolExecutionComponent
	toolOutputExpanded bool

	// Flags
	running         bool
	shutdownRequest bool
	hideThinking    bool
	autoCompact     bool
	isBashMode      bool

	// Double-escape tracking
	lastEscapeTime time.Time

	// Loading animation
	loadingAnimation *tuicomp.Loader

	// Cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Event subscription
	unsubscribe func()
}

// InteractiveModeOptions configures the interactive mode.
type InteractiveModeOptions struct {
	// InitialPrompt is sent as the first message if non-empty.
	InitialPrompt string
	// ThemeName is "dark", "light", or a custom theme path.
	ThemeName string
	// ThemeSearchDirs are directories to search for custom theme JSON files.
	ThemeSearchDirs []string
}

// NewInteractiveMode creates a new interactive mode.
func NewInteractiveMode(
	session *core.AgentSession,
	keybindings *core.KeybindingsManager,
	settings *core.SettingsManager,
	opts InteractiveModeOptions,
) *InteractiveMode {
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize theme
	_ = itheme.InitTheme(opts.ThemeName, opts.ThemeSearchDirs)

	m := &InteractiveMode{
		session:     session,
		keybindings: keybindings,
		settings:    settings,
		autoCompact: true,
		ctx:         ctx,
		cancel:      cancel,
	}

	m.markdownTheme = itheme.GetMarkdownTheme()

	return m
}

// Init initializes the TUI and components.
func (m *InteractiveMode) Init() error {
	// Create terminal and TUI
	term := tui.NewProcessTerminal()
	m.ui = tui.NewTUI(term, false)

	// Create message container
	m.messageContainer = &tui.Container{}
	m.ui.AddChild(m.messageContainer)

	// Create status container (for loading indicators, compaction status)
	m.statusContainer = &tui.Container{}
	m.ui.AddChild(m.statusContainer)

	// Create footer
	m.footerComponent = components.NewFooterComponent(func() components.FooterData {
		return m.getFooterData()
	})
	m.ui.AddChild(m.footerComponent)

	// Create editor container (holds editor or selector overlays)
	m.editorContainer = &tui.Container{}
	editorTheme := itheme.GetEditorTheme()
	m.editor = components.NewCustomEditor(m.ui, editorTheme, m.keybindings)
	m.setupEditorHandlers()
	m.editorContainer.AddChild(m.editor)
	m.ui.AddChild(m.editorContainer)

	// Subscribe to agent events
	m.subscribeToAgent()

	return nil
}

// Run starts the main event loop.
func (m *InteractiveMode) Run(opts InteractiveModeOptions) error {
	m.running = true

	// Handle SIGINT/SIGTERM for clean shutdown (e.g. kill from another terminal).
	// Note: in raw mode Ctrl+C is handled as input (\x03), not as SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			m.Shutdown()
		case <-m.ctx.Done():
		}
		signal.Stop(sigCh)
	}()

	// Send initial prompt if provided
	if opts.InitialPrompt != "" {
		go func() {
			_ = m.session.Prompt(opts.InitialPrompt)
		}()
	}

	// Start TUI (sets up terminal input/resize handlers)
	m.ui.Start()

	// Main loop: wait for shutdown
	<-m.ctx.Done()

	m.shutdown()
	return nil
}

// Shutdown cleanly stops the interactive mode.
func (m *InteractiveMode) Shutdown() {
	m.cancel()
}

func (m *InteractiveMode) shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return
	}
	m.running = false

	if m.unsubscribe != nil {
		m.unsubscribe()
		m.unsubscribe = nil
	}

	if m.ui != nil {
		m.ui.Stop()
	}
}

// ============================================================================
// Editor handlers
// ============================================================================

func (m *InteractiveMode) setupEditorHandlers() {
	// Submit handler
	m.editor.OnSubmit = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}

		// Handle slash commands
		if strings.HasPrefix(text, "/") {
			m.editor.SetText("")
			m.handleSlashCommand(text)
			return
		}

		// Handle bash mode (text starting with !)
		if strings.HasPrefix(strings.TrimSpace(text), "!") {
			cmd := strings.TrimSpace(text)[1:]
			m.editor.AddToHistory(text)
			m.editor.SetText("")
			go m.handleBashCommand(cmd)
			return
		}

		// Add to history and clear
		m.editor.AddToHistory(text)
		m.editor.SetText("")

		// Send message
		go func() {
			_ = m.session.Prompt(text)
		}()
	}

	// Escape handler with double-escape support
	m.editor.OnEscape = func() {
		// If streaming, interrupt/abort
		if m.session.IsStreaming() {
			m.session.Agent.Abort()
			return
		}

		// If in bash mode, exit bash mode
		if m.isBashMode {
			m.editor.SetText("")
			m.isBashMode = false
			m.updateEditorBorderColor()
			return
		}

		// Double-escape with empty editor
		if strings.TrimSpace(m.editor.GetText()) == "" {
			now := time.Now()
			if now.Sub(m.lastEscapeTime) < 500*time.Millisecond {
				m.showSessionSelector()
				m.lastEscapeTime = time.Time{}
			} else {
				m.lastEscapeTime = now
			}
		}
	}

	// Ctrl+D handler
	m.editor.OnCtrlD = func() {
		m.Shutdown()
	}

	// Register app action handlers
	m.editor.OnAction("selectModel", func() {
		m.showModelSelector("")
	})
	m.editor.OnAction("selectThinking", func() {
		m.showThinkingSelector()
	})
	m.editor.OnAction("expandTools", func() {
		m.toggleToolOutputExpansion()
	})
	m.editor.OnAction("toggleThinking", func() {
		m.toggleThinkingBlockVisibility()
	})
	m.editor.OnAction("cycleThinkingLevel", func() {
		m.cycleThinkingLevel()
	})
	m.editor.OnAction("cycleModelForward", func() {
		m.cycleModel("forward")
	})
	m.editor.OnAction("cycleModelBackward", func() {
		m.cycleModel("backward")
	})
	m.editor.OnAction("newSession", func() {
		go m.handleClearCommand()
	})
	m.editor.OnAction("resume", func() {
		m.showSessionSelector()
	})
	m.editor.OnAction("clear", func() {
		m.handleCtrlC()
	})
	m.editor.OnAction("suspend", func() {
		m.handleCtrlZ()
	})

	// Track bash mode on text change
	m.editor.OnChange = func(text string) {
		wasBashMode := m.isBashMode
		m.isBashMode = strings.HasPrefix(strings.TrimLeft(text, " \t"), "!")
		if wasBashMode != m.isBashMode {
			m.updateEditorBorderColor()
		}
	}
}

// ============================================================================
// Slash commands
// ============================================================================

func (m *InteractiveMode) handleSlashCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	cmd := parts[0]

	switch cmd {
	case "/help", "/hotkeys":
		m.showHelp()
	case "/clear":
		go m.handleClearCommand()
	case "/compact":
		var instructions string
		if len(parts) > 1 {
			instructions = strings.Join(parts[1:], " ")
		}
		go m.handleCompactCommand(instructions)
	case "/model":
		var searchTerm string
		if len(parts) > 1 {
			searchTerm = strings.Join(parts[1:], " ")
		}
		m.showModelSelector(searchTerm)
	case "/thinking":
		m.showThinkingSelector()
	case "/theme":
		m.showThemeSelector()
	case "/settings":
		m.showSettingsSelector()
	case "/session":
		m.showSessionSelector()
	case "/quit", "/exit":
		m.Shutdown()
	default:
		m.showWarning(fmt.Sprintf("Unknown command: %s. Type /help for available commands.", cmd))
	}
}

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
			nil, // use default search dirs
			func(themeName string) {
				_ = itheme.InitTheme(themeName, nil)
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
				_ = itheme.InitTheme(themeName, nil)
				m.markdownTheme = itheme.GetMarkdownTheme()
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
		config := components.SettingsConfig{
			AutoCompact:             m.autoCompact,
			HideThinkingBlock:       m.hideThinking,
			ThinkingLevel:           "medium",
			AvailableThinkingLevels: []string{"off", "minimal", "low", "medium", "high"},
			CurrentTheme:            "dark",
			AvailableThemes:         []string{"dark", "light"},
			SteeringMode:            "one-at-a-time",
			FollowUpMode:            "one-at-a-time",
			DoubleEscapeAction:      "tree",
			AutocompleteMaxVisible:  10,
		}
		callbacks := components.SettingsCallbacks{
			OnAutoCompactChange:       func(v bool) { m.autoCompact = v },
			OnHideThinkingBlockChange: func(v bool) { m.hideThinking = v },
			OnCancel:                  func() { done() },
		}
		selector := components.NewSettingsSelectorComponent(config, callbacks)
		return selector, selector
	})
}

// ============================================================================
// Session selector
// ============================================================================

func (m *InteractiveMode) showSessionSelector() {
	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		sessions, _ := core.ListSessions("", "")
		selector := components.NewSessionSelectorComponent(
			sessions,
			components.SessionScopeCurrent,
			func(sessionPath string) {
				done()
				go m.handleResumeSession(sessionPath)
			},
			func() {
				done()
			},
		)
		return selector, selector
	})
}

func (m *InteractiveMode) handleResumeSession(sessionPath string) {
	// Stop loading animation
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
		m.loadingAnimation = nil
	}
	m.statusContainer.Clear()

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
// Compaction
// ============================================================================

func (m *InteractiveMode) handleCompactCommand(customInstructions string) {
	entries := m.session.SessionManager.GetEntries()
	messageCount := 0
	for _, e := range entries {
		if e.Type == "message" {
			messageCount++
		}
	}
	if messageCount < 2 {
		m.showWarning("Nothing to compact (no messages yet)")
		return
	}

	m.executeCompaction(customInstructions, false)
}

func (m *InteractiveMode) executeCompaction(customInstructions string, isAuto bool) {
	t := itheme.GetTheme()

	// Clear status and show compacting indicator
	m.statusContainer.Clear()
	label := "Compacting context..."
	if isAuto {
		label = "Auto-compacting context..."
	}
	loader := tuicomp.NewLoader(
		m.ui.AsRenderRequester(),
		func(spinner string) string { return t.Fg("accent", spinner) },
		func(text string) string { return t.Fg("muted", text) },
		label,
	)
	m.statusContainer.AddChild(loader)
	m.ui.RequestRender(false)

	result, err := m.session.RunCompaction()
	loader.Stop()
	m.statusContainer.Clear()

	if err != nil {
		m.showWarning(fmt.Sprintf("Compaction failed: %s", err))
		m.ui.RequestRender(false)
		return
	}

	if result != nil {
		m.rebuildChatFromMessages()

		// Show compaction summary
		summary := fmt.Sprintf("Compacted: %d tokens", result.TokensBefore)
		m.showStatus(summary)
	}

	m.ui.RequestRender(false)
}

// ============================================================================
// Bash execution
// ============================================================================

func (m *InteractiveMode) handleBashCommand(command string) {
	// Show user message for the bash command
	m.AddUserMessage("!" + command)

	// Execute via the session's prompt mechanism with bash prefix
	_ = m.session.Prompt("!" + command)
}

// ============================================================================
// Ctrl+C / Ctrl+Z / clear command
// ============================================================================

func (m *InteractiveMode) handleCtrlC() {
	if m.session.IsStreaming() {
		m.session.Agent.Abort()
		return
	}
	// Clear editor
	m.editor.SetText("")
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) handleCtrlZ() {
	// Send SIGTSTP to self (suspend)
	// On most systems, this suspends the process
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		_ = p.Signal(suspendSignal())
	}
}

func (m *InteractiveMode) handleClearCommand() {
	if m.session != nil {
		_, err := m.session.NewSessionCmd()
		if err != nil {
			m.showWarning(fmt.Sprintf("Failed to create new session: %s", err))
			return
		}
	}
	if m.messageContainer != nil {
		m.messageContainer.Clear()
	}
	if m.statusContainer != nil {
		m.statusContainer.Clear()
	}
	if m.footerComponent != nil {
		m.footerComponent.Invalidate()
	}
	if m.ui != nil {
		m.ui.RequestRender(true)
	}
	m.showStatus("New session started")
}

// ============================================================================
// Thinking/model cycling
// ============================================================================

func (m *InteractiveMode) cycleThinkingLevel() {
	levels := m.session.GetAvailableThinkingLevels()
	if len(levels) <= 1 {
		return
	}
	current := agent.ThinkingLevel(m.session.ThinkingLevel())
	idx := 0
	for i, l := range levels {
		if l == current {
			idx = i
			break
		}
	}
	next := levels[(idx+1)%len(levels)]
	m.session.SetThinkingLevel(string(next))
	m.footerComponent.Invalidate()
	m.showStatus(fmt.Sprintf("Thinking: %s", next))
}

func (m *InteractiveMode) cycleModel(direction string) {
	registry := m.session.ModelRegistryRef()
	registry.Refresh()
	available := registry.GetAvailable()
	if len(available) == 0 {
		return
	}
	current := m.session.Model()
	idx := 0
	for i, model := range available {
		if ai.ModelsAreEqual(current, model) {
			idx = i
			break
		}
	}
	var next int
	if direction == "forward" {
		next = (idx + 1) % len(available)
	} else {
		next = (idx - 1 + len(available)) % len(available)
	}
	m.session.SetModel(available[next])
	m.footerComponent.Invalidate()
	m.updateEditorBorderColor()
	m.showStatus(fmt.Sprintf("Model: %s", available[next].ID))
}

// ============================================================================
// Tool output / thinking visibility
// ============================================================================

func (m *InteractiveMode) toggleToolOutputExpansion() {
	m.toolOutputExpanded = !m.toolOutputExpanded
	// Update all tool components in the message container
	for _, child := range m.messageContainer.Children {
		if tc, ok := child.(*components.ToolExecutionComponent); ok {
			tc.SetExpanded(m.toolOutputExpanded)
		}
	}
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) toggleThinkingBlockVisibility() {
	m.hideThinking = !m.hideThinking
	for _, child := range m.messageContainer.Children {
		if ac, ok := child.(*components.AssistantMessageComponent); ok {
			ac.SetHideThinkingBlock(m.hideThinking)
		}
	}
	m.ui.RequestRender(false)
	if m.hideThinking {
		m.showStatus("Thinking blocks hidden")
	} else {
		m.showStatus("Thinking blocks visible")
	}
}

func (m *InteractiveMode) updateEditorBorderColor() {
	// Could update editor border based on model provider or bash mode
	m.ui.RequestRender(false)
}

// ============================================================================
// Display helpers
// ============================================================================

func (m *InteractiveMode) showMessage(text string) {
	if m.messageContainer == nil {
		return
	}
	t := itheme.GetTheme()
	m.messageContainer.AddChild(tuicomp.NewSpacer(1))
	m.messageContainer.AddChild(tuicomp.NewText(t.Fg("muted", text), 1, 0, nil))
	if m.ui != nil {
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) showStatus(message string) {
	if m.statusContainer == nil {
		return
	}
	t := itheme.GetTheme()
	m.statusContainer.Clear()
	m.statusContainer.AddChild(tuicomp.NewText(t.Fg("success", message), 1, 0, nil))
	if m.ui != nil {
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) showWarning(message string) {
	if m.statusContainer == nil {
		return
	}
	t := itheme.GetTheme()
	m.statusContainer.Clear()
	m.statusContainer.AddChild(tuicomp.NewText(t.Fg("warning", message), 1, 0, nil))
	if m.ui != nil {
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) showHelp() {
	helpText := `Available commands:
  /help       - Show this help
  /clear      - Start new session
  /compact    - Compact conversation context
  /model      - Select model (or /model <search>)
  /thinking   - Select thinking level
  /theme      - Select theme
  /settings   - Open settings
  /session    - Resume a previous session
  /quit       - Exit

Keyboard shortcuts:
  Ctrl+D      - Exit
  Ctrl+C      - Abort streaming / clear editor
  Escape      - Abort streaming / double-tap for sessions
  !<command>  - Execute bash command`
	m.showMessage(helpText)
}

func (m *InteractiveMode) getFooterData() components.FooterData {
	pwd, _ := os.Getwd()
	modelID := "unknown"
	if m.session != nil {
		if model := m.session.Model(); model != nil {
			modelID = model.ID
		}
	}
	return components.FooterData{
		Pwd:         pwd,
		ModelID:     modelID,
		AutoCompact: m.autoCompact,
	}
}

// ============================================================================
// Chat message management
// ============================================================================

// AddUserMessage adds a user message to the display.
func (m *InteractiveMode) AddUserMessage(text string) {
	m.messageContainer.AddChild(components.NewUserMessageComponent(text, nil))
	m.ui.RequestRender(false)
}

// AddAssistantMessage adds an assistant message to the display.
func (m *InteractiveMode) AddAssistantMessage(msg *ai.AssistantMessage) {
	comp := components.NewAssistantMessageComponent(msg, m.hideThinking, nil)
	m.messageContainer.AddChild(comp)
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) rebuildChatFromMessages() {
	m.messageContainer.Clear()
	m.statusContainer.Clear()

	state := m.session.State()
	for _, agentMsg := range state.Messages {
		if u := agentMsg.Message.AsUser(); u != nil {
			if txt, ok := u.Content.(string); ok {
				m.messageContainer.AddChild(components.NewUserMessageComponent(txt, nil))
			}
		} else if a := agentMsg.Message.AsAssistant(); a != nil {
			comp := components.NewAssistantMessageComponent(a, m.hideThinking, nil)
			m.messageContainer.AddChild(comp)
		}
	}
	m.ui.RequestRender(true)
}

// ============================================================================
// Agent event subscription
// ============================================================================

func (m *InteractiveMode) subscribeToAgent() {
	m.pendingTools = make(map[string]*components.ToolExecutionComponent)
	m.unsubscribe = m.session.Subscribe(func(event core.AgentSessionEvent) {
		m.handleEvent(event)
	})
}

func (m *InteractiveMode) handleEvent(event core.AgentSessionEvent) {
	if event.AgentEvent == nil {
		// Handle session-level events
		switch event.Type {
		case "auto_compaction_start":
			m.showStatus("Compacting context...")
		case "auto_compaction_end":
			if event.ErrorMessage != "" {
				m.showWarning("Compaction failed: " + event.ErrorMessage)
			} else {
				m.statusContainer.Clear()
				m.ui.RequestRender(false)
			}
		}
		return
	}

	ae := event.AgentEvent
	switch ae.Type {
	case agent.EventAgentStart:
		m.onAgentStart()
	case agent.EventMessageStart:
		m.onMessageStart(ae)
	case agent.EventMessageUpdate:
		m.onMessageUpdate(ae)
	case agent.EventMessageEnd:
		m.onMessageEnd(ae)
	case agent.EventToolExecutionStart:
		m.onToolExecStart(ae)
	case agent.EventToolExecutionUpdate:
		m.onToolExecUpdate(ae)
	case agent.EventToolExecutionEnd:
		m.onToolExecEnd(ae)
	case agent.EventAgentEnd:
		m.onAgentEnd()
	}
}

func (m *InteractiveMode) onAgentStart() {
	// Clear status and show working indicator
	m.statusContainer.Clear()
	t := itheme.GetTheme()
	loader := tuicomp.NewLoader(
		m.ui.AsRenderRequester(),
		func(spinner string) string { return t.Fg("accent", spinner) },
		func(text string) string { return t.Fg("muted", text) },
		"Working...",
	)
	m.loadingAnimation = loader
	m.statusContainer.AddChild(loader)
	m.streamingComponent = nil
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onMessageStart(ae *agent.AgentEvent) {
	// Stop loading animation
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
		m.loadingAnimation = nil
		m.statusContainer.Clear()
	}

	if ae.Message == nil {
		return
	}
	msg := ae.Message.AsAssistant()
	if msg != nil {
		m.streamingComponent = components.NewAssistantMessageComponent(msg, m.hideThinking, nil)
		m.messageContainer.AddChild(m.streamingComponent)
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) onMessageUpdate(ae *agent.AgentEvent) {
	if m.streamingComponent == nil || ae.Message == nil {
		return
	}
	msg := ae.Message.AsAssistant()
	if msg != nil {
		m.streamingComponent.UpdateContent(msg)
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) onMessageEnd(ae *agent.AgentEvent) {
	if m.streamingComponent == nil || ae.Message == nil {
		return
	}
	msg := ae.Message.AsAssistant()
	if msg != nil {
		m.streamingComponent.UpdateContent(msg)
		m.streamingComponent = nil
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) onToolExecStart(ae *agent.AgentEvent) {
	// Stop loading animation
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
		m.loadingAnimation = nil
		m.statusContainer.Clear()
	}

	args := make(map[string]any)
	if ae.Args != nil {
		if argMap, ok := ae.Args.(map[string]any); ok {
			args = argMap
		}
	}
	comp := components.NewToolExecutionComponent(ae.ToolName, args, nil)
	if m.toolOutputExpanded {
		comp.SetExpanded(true)
	}
	m.pendingTools[ae.ToolCallID] = comp
	m.messageContainer.AddChild(comp)
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onToolExecUpdate(ae *agent.AgentEvent) {
	comp, ok := m.pendingTools[ae.ToolCallID]
	if !ok {
		return
	}
	if ae.Args != nil {
		if args, ok := ae.Args.(map[string]any); ok {
			comp.UpdateArgs(args)
		}
	}
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onToolExecEnd(ae *agent.AgentEvent) {
	comp, ok := m.pendingTools[ae.ToolCallID]
	if !ok {
		return
	}
	delete(m.pendingTools, ae.ToolCallID)

	if ae.Result != nil {
		if result, ok := ae.Result.(*agent.AgentToolResult); ok {
			resultData := &components.ToolResultData{
				IsError: ae.IsError,
				Details: make(map[string]any),
			}
			for _, c := range result.Content {
				resultData.Content = append(resultData.Content, components.ToolContentBlock{
					Type:     c.Type,
					Text:     c.Text,
					Data:     c.Data,
					MimeType: c.MimeType,
				})
			}
			comp.UpdateResult(resultData, m.toolOutputExpanded)
		}
	}
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onAgentEnd() {
	// Stop loading animation
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
		m.loadingAnimation = nil
	}
	m.statusContainer.Clear()
	m.streamingComponent = nil
	m.pendingTools = make(map[string]*components.ToolExecutionComponent)
	m.ui.RequestRender(false)
}
