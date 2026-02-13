// Ported from: packages/coding-agent/src/modes/interactive/interactive-mode.ts
// Upstream hash: 1caadb2e
//
// This is the main interactive mode for the coding agent TUI.
// It manages the TUI lifecycle, renders messages, handles user input,
// and delegates business logic to AgentSession.
package interactive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/ai/oauth"
	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/core/tools"
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
	messageContainer   *tui.Container
	statusContainer    *tui.Container
	footerComponent    *components.FooterComponent
	footerDataProvider *core.FooterDataProvider
	markdownTheme      tuicomp.MarkdownTheme

	// Streaming state
	streamingComponent *components.AssistantMessageComponent
	pendingTools       map[string]*components.ToolExecutionComponent
	toolOutputExpanded bool

	// Bash execution state
	bashComponent *components.BashExecutionComponent

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

	cwd, _ := os.Getwd()

	m := &InteractiveMode{
		session:            session,
		keybindings:        keybindings,
		settings:           settings,
		autoCompact:        true,
		footerDataProvider: core.NewFooterDataProvider(cwd),
		ctx:                ctx,
		cancel:             cancel,
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

	// Create editor container (holds editor or selector overlays)
	m.editorContainer = &tui.Container{}
	editorTheme := itheme.GetEditorTheme()
	m.editor = components.NewCustomEditor(m.ui, editorTheme, m.keybindings)
	m.setupEditorHandlers()
	m.editorContainer.AddChild(m.editor)
	m.ui.AddChild(m.editorContainer)

	// Create footer (below the input box)
	m.footerComponent = components.NewFooterComponent(func() components.FooterData {
		return m.getFooterData()
	})
	m.ui.AddChild(m.footerComponent)

	// Focus the editor so it receives keyboard input
	m.ui.SetFocus(m.editor)

	// Set up autocomplete with slash commands
	m.setupAutocomplete()

	// Subscribe to agent events
	m.subscribeToAgent()

	// Show loaded resources and any diagnostics at startup
	m.showLoadedResources()

	return nil
}

// showLoadedResources displays skill/prompt diagnostics (collisions, parse errors) in the chat area.
func (m *InteractiveMode) showLoadedResources() {
	if m.session == nil {
		return
	}
	t := itheme.GetTheme()

	// Show skill diagnostics
	skills, skillDiags := m.session.ResourceLoader().GetSkills()
	prompts, promptDiags := m.session.ResourceLoader().GetPrompts()
	_ = skills
	_ = prompts

	if len(skillDiags) > 0 {
		lines := formatDiagnostics(t, "Skill conflicts", skillDiags)
		m.messageContainer.AddChild(tuicomp.NewText(lines, 0, 0, nil))
		m.messageContainer.AddChild(tuicomp.NewSpacer(1))
	}

	if len(promptDiags) > 0 {
		lines := formatDiagnostics(t, "Prompt conflicts", promptDiags)
		m.messageContainer.AddChild(tuicomp.NewText(lines, 0, 0, nil))
		m.messageContainer.AddChild(tuicomp.NewSpacer(1))
	}
}

// formatDiagnostics formats resource diagnostics for display.
func formatDiagnostics(t *itheme.Theme, header string, diags []core.ResourceDiagnostic) string {
	var lines []string
	lines = append(lines, t.Fg("warning", "["+header+"]"))

	// Group collision diagnostics by name
	type collisionGroup struct {
		name   string
		winner string
		losers []string
	}
	groups := make(map[string]*collisionGroup)
	var otherDiags []core.ResourceDiagnostic

	for _, d := range diags {
		if d.Type == "collision" && d.Collision != nil {
			g, ok := groups[d.Collision.Name]
			if !ok {
				g = &collisionGroup{
					name:   d.Collision.Name,
					winner: d.Collision.WinnerPath,
				}
				groups[d.Collision.Name] = g
			}
			g.losers = append(g.losers, d.Collision.LoserPath)
		} else {
			otherDiags = append(otherDiags, d)
		}
	}

	for _, g := range groups {
		lines = append(lines, t.Fg("warning", fmt.Sprintf("  \"%s\" collision:", g.name)))
		lines = append(lines, t.Fg("dim", fmt.Sprintf("    %s %s", t.Fg("success", "✓"), g.winner)))
		for _, loser := range g.losers {
			lines = append(lines, t.Fg("dim", fmt.Sprintf("    %s %s (skipped)", t.Fg("warning", "✗"), loser)))
		}
	}

	for _, d := range otherDiags {
		color := "warning"
		if d.Type == "error" {
			color = "error"
		}
		if d.Path != "" {
			lines = append(lines, t.Fg(color, fmt.Sprintf("  %s", d.Path)))
			lines = append(lines, t.Fg(color, fmt.Sprintf("    %s", d.Message)))
		} else {
			lines = append(lines, t.Fg(color, fmt.Sprintf("  %s", d.Message)))
		}
	}

	return strings.Join(lines, "\n")
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

	if m.footerDataProvider != nil {
		m.footerDataProvider.Dispose()
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

		// Handle slash commands — known builtins are dispatched locally;
		// everything else (skill commands, prompt templates) falls through
		// to session.Prompt which expands them.
		if strings.HasPrefix(text, "/") && m.isBuiltinSlashCommand(text) {
			m.editor.SetText("")
			m.handleSlashCommand(text)
			return
		}

		// Handle bash command (! for normal, !! for excluded from context)
		if strings.HasPrefix(strings.TrimSpace(text), "!") {
			trimmed := strings.TrimSpace(text)
			isExcluded := strings.HasPrefix(trimmed, "!!")
			var cmd string
			if isExcluded {
				cmd = strings.TrimSpace(trimmed[2:])
			} else {
				cmd = strings.TrimSpace(trimmed[1:])
			}
			if cmd != "" {
				if m.session != nil && m.session.IsBashRunning() {
					m.showWarning("A bash command is already running. Press Esc to cancel it first.")
					m.editor.SetText(text)
					return
				}
				m.editor.AddToHistory(text)
				m.editor.SetText("")
				go m.handleBashCommand(cmd, isExcluded)
				return
			}
		}

		// Add to history and clear
		m.editor.AddToHistory(text)
		m.editor.SetText("")

		// Send message
		if m.session != nil {
			go func() {
				_ = m.session.Prompt(text)
			}()
		}
	}

	// Escape handler with double-escape support
	m.editor.OnEscape = func() {
		// If streaming, interrupt/abort
		if m.session != nil && m.session.IsStreaming() {
			m.session.Agent.Abort()
			return
		}

		// If bash is running, abort it
		if m.session != nil && m.session.IsBashRunning() {
			m.session.AbortBash()
			return
		}

		// If in bash mode, exit bash mode
		m.mu.Lock()
		inBash := m.isBashMode
		m.mu.Unlock()
		if inBash {
			m.editor.SetText("")
			m.mu.Lock()
			m.isBashMode = false
			m.mu.Unlock()
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
		m.mu.Lock()
		wasBashMode := m.isBashMode
		m.isBashMode = strings.HasPrefix(strings.TrimLeft(text, " \t"), "!")
		changed := wasBashMode != m.isBashMode
		m.mu.Unlock()
		if changed {
			m.updateEditorBorderColor()
		}
	}
}

// ============================================================================
// Autocomplete setup
// ============================================================================

func (m *InteractiveMode) setupAutocomplete() {
	// Build slash command list from builtins
	var commands []SlashCommand
	for _, cmd := range core.BuiltinSlashCommands {
		commands = append(commands, SlashCommand{
			Name:        cmd.Name,
			Description: cmd.Description,
		})
	}

	// Add skill commands (skill:<name>) from the resource loader
	if m.session != nil {
		rl := m.session.ResourceLoader()
		if rl != nil {
			if skills, _ := rl.GetSkills(); len(skills) > 0 {
				for _, skill := range skills {
					commands = append(commands, SlashCommand{
						Name:        "skill:" + skill.Name,
						Description: skill.Description,
					})
				}
			}
		}
	}

	basePath, _ := os.Getwd()
	provider := NewCombinedAutocompleteProvider(commands, basePath)
	m.editor.SetAutocompleteProvider(provider)
}

// ============================================================================
// Slash commands
// ============================================================================

// isBuiltinSlashCommand checks if text is a known builtin slash command.
// Returns false for skill commands (/skill:*), prompt templates, and unknowns
// so they can be sent to session.Prompt() for expansion.
func (m *InteractiveMode) isBuiltinSlashCommand(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	cmd := parts[0]
	switch cmd {
	case "/help", "/hotkeys", "/clear", "/new", "/compact", "/model",
		"/thinking", "/theme", "/settings", "/session", "/resume",
		"/login", "/logout", "/scoped-models", "/tree", "/fork",
		"/export", "/share", "/copy", "/name", "/changelog",
		"/reload", "/quit", "/exit":
		return true
	}
	return false
}

func (m *InteractiveMode) handleSlashCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	cmd := parts[0]

	switch cmd {
	case "/help", "/hotkeys":
		m.showHelp()
	case "/clear", "/new":
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
		m.handleSessionCommand()
	case "/resume":
		m.showSessionSelector()
	case "/login":
		m.showOAuthSelector("login")
	case "/logout":
		m.showOAuthSelector("logout")
	case "/scoped-models":
		m.showScopedModelsSelector()
	case "/tree":
		m.showTreeSelector()
	case "/fork":
		if len(parts) > 1 {
			m.handleForkByNumber(parts[1])
		} else {
			m.showUserMessageSelector()
		}
	case "/export":
		m.handleExportCommand(text)
	case "/share":
		m.handleShareCommand()
	case "/copy":
		m.handleCopyCommand()
	case "/name":
		m.handleNameCommand(text)
	case "/changelog":
		m.handleChangelogCommand()
	case "/reload":
		m.handleReloadCommand()
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
			OnAutoCompactChange:       func(v bool) { 
				m.autoCompact = v 
				m.settings.SetCompactionEnabled(v)
			},
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

func (m *InteractiveMode) handleBashCommand(command string, excludeFromContext bool) {
	// Create UI component for display
	bashComp := components.NewBashExecutionComponent(command, m.ui, excludeFromContext)
	m.mu.Lock()
	m.bashComponent = bashComp
	m.mu.Unlock()
	m.messageContainer.AddChild(bashComp)
	m.ui.RequestRender(false)

	if m.session != nil {
		result, err := m.session.ExecuteBashWithOptions(command, func(chunk string) {
			m.mu.Lock()
			bc := m.bashComponent
			m.mu.Unlock()
			if bc != nil {
				bc.AppendOutput(chunk)
				m.ui.RequestRender(false)
			}
		}, excludeFromContext)

		m.mu.Lock()
		bc := m.bashComponent
		m.mu.Unlock()
		if err != nil {
			if bc != nil {
				bc.SetComplete(nil, false, nil, "")
			}
			m.showWarning(fmt.Sprintf("Bash command failed: %v", err))
		} else if bc != nil {
			exitCode := result.ExitCode
			var truncResult *tools.TruncationResult
			if result.Truncated {
				truncResult = &tools.TruncationResult{Truncated: true, Content: result.Output}
			}
			bc.SetComplete(&exitCode, result.Cancelled, truncResult, result.FullOutputPath)
		}
	}

	m.mu.Lock()
	m.bashComponent = nil
	m.isBashMode = false
	m.mu.Unlock()
	m.updateEditorBorderColor()
	m.ui.RequestRender(false)
}

// ============================================================================
// Ctrl+C / Ctrl+Z / clear command
// ============================================================================

func (m *InteractiveMode) handleCtrlC() {
	if m.session != nil && m.session.IsStreaming() {
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
			if cred := authStorage.Get(p); cred != nil && cred.Type == core.CredentialTypeOAuth {
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
			selectItems[i] = tuicomp.SelectItem{Label: name, Value: id}
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
		selectItems[i] = tuicomp.SelectItem{Label: p.Name(), Value: p.ID()}
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

	callbacks := oauth.LoginCallbacks{
		OnAuth: func(info oauth.AuthInfo) {
			// Try to auto-open the browser.
			browserOpened := core.OpenBrowser(info.URL) == nil

			// Show a clickable OSC 8 hyperlink so the URL stays on one line.
			link := core.Hyperlink(info.URL, info.URL)
			var msg string
			if browserOpened {
				msg = fmt.Sprintf("Opening browser… if it doesn't appear, visit:\n%s", link)
			} else {
				msg = fmt.Sprintf("Open this URL to authenticate:\n%s", link)
			}
			if info.Instructions != "" {
				msg += "\n" + info.Instructions
			}
			m.showStatus(msg)
		},
		OnPrompt: func(prompt oauth.Prompt) (string, error) {
			// For now, show a status message — full prompt input requires
			// a dialog component which is not yet implemented.
			m.showWarning(fmt.Sprintf("Login requires input: %s (use browser flow)", prompt.Message))
			return "", fmt.Errorf("interactive prompt not yet implemented")
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
	m.showWarning("/scoped-models is not yet implemented")
}

// ============================================================================
// Tree / fork / user message selectors
// ============================================================================

func (m *InteractiveMode) showTreeSelector() {
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

	// Build a text-based tree display
	t := itheme.GetTheme()
	var lines []string
	lines = append(lines, t.Bold("Session Tree"))
	lines = append(lines, "")
	var renderNodes func(nodes []*core.SessionTreeNode, depth int)
	renderNodes = func(nodes []*core.SessionTreeNode, depth int) {
		for _, node := range nodes {
			prefix := strings.Repeat("  ", depth)
			label := node.Entry.Type
			if node.Entry.Type == "message" {
				text := extractEntryText(node.Entry)
				if len(text) > 60 {
					text = text[:60] + "..."
				}
				label = text
			}
			marker := "  "
			if node.Entry.ID == leafID {
				marker = t.Fg("accent", "▸ ")
			}
			sessionLabel := ""
			if node.Label != "" {
				sessionLabel = t.Fg("dim", " ["+node.Label+"]")
			}
			lines = append(lines, prefix+marker+label+sessionLabel)
			if len(node.Children) > 0 {
				renderNodes(node.Children, depth+1)
			}
		}
	}
	renderNodes(tree, 0)

	m.showMessage(strings.Join(lines, "\n"))
}

func (m *InteractiveMode) showUserMessageSelector() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	userMsgs := m.session.GetUserMessagesForForking()
	if len(userMsgs) == 0 {
		m.showStatus("No messages to fork from")
		return
	}

	// Build a list of user messages for selection
	t := itheme.GetTheme()
	var lines []string
	lines = append(lines, t.Bold("Select a message to fork from:"))
	lines = append(lines, "")
	for i, msg := range userMsgs {
		text := msg.Text
		if len(text) > 80 {
			text = text[:80] + "..."
		}
		// Replace newlines for display
		text = strings.ReplaceAll(text, "\n", " ")
		lines = append(lines, fmt.Sprintf("  %d. %s", i+1, text))
	}
	lines = append(lines, "")
	lines = append(lines, t.Fg("dim", "Use /fork <number> to fork from a specific message"))

	m.showMessage(strings.Join(lines, "\n"))
}

// ============================================================================
// Export / share / copy / name / changelog / session info / reload
// ============================================================================

// extractEntryText extracts display text from a session entry.
func extractEntryText(entry *core.SessionEntry) string {
	if entry.Type != "message" || len(entry.RawMessage) == 0 {
		return entry.Type
	}
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(entry.RawMessage, &msg) != nil {
		return entry.Type
	}

	// Try string content
	var s string
	if json.Unmarshal(msg.Content, &s) == nil {
		return strings.ReplaceAll(s, "\n", " ")
	}

	// Try array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(msg.Content, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return strings.ReplaceAll(b.Text, "\n", " ")
			}
		}
	}

	return fmt.Sprintf("[%s message]", msg.Role)
}

func (m *InteractiveMode) handleExportCommand(text string) {
	m.showWarning("/export is not yet implemented")
}

func (m *InteractiveMode) handleShareCommand() {
	m.showWarning("/share is not yet implemented")
}

func (m *InteractiveMode) handleForkByNumber(numStr string) {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	n, err := strconv.Atoi(numStr)
	if err != nil || n < 1 {
		m.showWarning("Usage: /fork <number> — use /fork to see available messages")
		return
	}

	userMsgs := m.session.GetUserMessagesForForking()
	if n > len(userMsgs) {
		m.showWarning(fmt.Sprintf("Message %d not found. Only %d user messages available.", n, len(userMsgs)))
		return
	}

	entry := userMsgs[n-1]
	selectedText, cancelled, forkErr := m.session.Fork(entry.EntryID)
	if forkErr != nil {
		m.showWarning(fmt.Sprintf("Fork failed: %s", forkErr))
		return
	}
	if cancelled {
		return
	}

	m.rebuildChatFromMessages()
	if m.editor != nil && selectedText != "" {
		m.editor.SetText(selectedText)
	}
	m.showStatus("Branched to new session")
}

func (m *InteractiveMode) handleCopyCommand() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	text := m.session.GetLastAssistantText()
	if text == "" {
		m.showWarning("No agent messages to copy yet.")
		return
	}
	core.CopyToClipboard(text)
	m.showStatus("Copied last agent message to clipboard")
}

func (m *InteractiveMode) handleNameCommand(text string) {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	name := strings.TrimSpace(strings.TrimPrefix(text, "/name"))
	if name == "" {
		currentName := m.session.SessionManager.GetSessionName()
		if currentName != "" {
			t := itheme.GetTheme()
			m.showMessage(t.Fg("dim", "Session name: "+currentName))
		} else {
			m.showWarning("Usage: /name <name>")
		}
		return
	}
	m.session.SetSessionName(name)
	t := itheme.GetTheme()
	m.showMessage(t.Fg("dim", "Session name set: "+name))
}

func (m *InteractiveMode) handleSessionCommand() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	stats := m.session.GetSessionStats()
	sessionName := m.session.SessionManager.GetSessionName()
	t := itheme.GetTheme()

	var lines []string
	lines = append(lines, t.Bold("Session Info"))
	lines = append(lines, "")
	if sessionName != "" {
		lines = append(lines, t.Fg("dim", "Name: ")+sessionName)
	}
	if stats.SessionFile != "" {
		lines = append(lines, t.Fg("dim", "File: ")+stats.SessionFile)
	} else {
		lines = append(lines, t.Fg("dim", "File: ")+"In-memory")
	}
	lines = append(lines, t.Fg("dim", "ID: ")+stats.SessionID)
	lines = append(lines, "")
	lines = append(lines, t.Bold("Messages"))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "User:"), stats.UserMessages))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Assistant:"), stats.AssistantMessages))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Tool Calls:"), stats.ToolCalls))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Tool Results:"), stats.ToolResults))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Total:"), stats.TotalMessages))
	lines = append(lines, "")
	lines = append(lines, t.Bold("Tokens"))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Input:"), stats.Tokens.Input))
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Output:"), stats.Tokens.Output))
	if stats.Tokens.CacheRead > 0 {
		lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Cache Read:"), stats.Tokens.CacheRead))
	}
	if stats.Tokens.CacheWrite > 0 {
		lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Cache Write:"), stats.Tokens.CacheWrite))
	}
	lines = append(lines, fmt.Sprintf("%s %d", t.Fg("dim", "Total:"), stats.Tokens.Total))

	if stats.Cost > 0 {
		lines = append(lines, "")
		lines = append(lines, t.Bold("Cost"))
		lines = append(lines, fmt.Sprintf("%s %.4f", t.Fg("dim", "Total:"), stats.Cost))
	}

	m.showMessage(strings.Join(lines, "\n"))
}

func (m *InteractiveMode) handleChangelogCommand() {
	// Look for CHANGELOG.md next to the binary
	execPath, err := os.Executable()
	if err != nil {
		m.showWarning("Could not determine executable path")
		return
	}
	changelogPath := filepath.Join(filepath.Dir(execPath), "CHANGELOG.md")
	entries := core.ParseChangelog(changelogPath)
	if len(entries) == 0 {
		m.showMessage("No changelog entries found.")
		return
	}

	// Show newest first
	t := itheme.GetTheme()
	var lines []string
	lines = append(lines, t.Bold(t.Fg("accent", "What's New")))
	lines = append(lines, "")
	for i := len(entries) - 1; i >= 0; i-- {
		lines = append(lines, entries[i].Content)
		if i > 0 {
			lines = append(lines, "")
		}
	}
	m.showMessage(strings.Join(lines, "\n"))
}

func (m *InteractiveMode) handleReloadCommand() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	if m.session.IsStreaming() {
		m.showWarning("Wait for the current response to finish before reloading.")
		return
	}

	m.showStatus("Reloading extensions, skills, prompts, and themes...")
	if err := m.session.Reload(); err != nil {
		m.showWarning(fmt.Sprintf("Reload failed: %v", err))
		return
	}
	m.rebuildChatFromMessages()
	m.showStatus("Reloaded extensions, skills, prompts, themes")
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
	for _, child := range m.messageContainer.ChildrenSnapshot() {
		if tc, ok := child.(*components.ToolExecutionComponent); ok {
			tc.SetExpanded(m.toolOutputExpanded)
		}
	}
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) toggleThinkingBlockVisibility() {
	m.hideThinking = !m.hideThinking
	for _, child := range m.messageContainer.ChildrenSnapshot() {
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

// IsBashMode returns true if the editor is in bash mode (thread-safe).
func (m *InteractiveMode) IsBashMode() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isBashMode
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
  /help           - Show this help / keyboard shortcuts
  /model          - Select model (or /model <search>)
  /thinking       - Select thinking level
  /settings       - Open settings menu
  /theme          - Select theme
  /new            - Start a new session
  /compact        - Compact conversation context
  /resume         - Resume a different session
  /session        - Show session info and stats
  /name <name>    - Set session display name
  /login          - Login with OAuth provider
  /logout         - Logout from OAuth provider
  /scoped-models  - Enable/disable models for Ctrl+P cycling
  /tree           - Navigate session tree (switch branches)
  /fork           - Create a new fork from a previous message
  /export         - Export session to HTML file
  /share          - Share session as a secret GitHub gist
  /copy           - Copy last agent message to clipboard
  /changelog      - Show changelog entries
  /reload         - Reload extensions, skills, prompts, and themes
  /quit           - Quit pi

Keyboard shortcuts:
  Enter           - Send message
  Shift+Enter     - New line
  Ctrl+D          - Exit (when editor is empty)
  Ctrl+C          - Cancel autocomplete / abort streaming / clear editor
  Escape          - Abort streaming / double-tap for sessions
  Tab             - Path completion / accept autocomplete
  Shift+Tab       - Cycle thinking level
  Ctrl+P          - Cycle models
  Ctrl+L          - Open model selector
  Ctrl+O          - Toggle tool output expansion
  Ctrl+T          - Toggle thinking block visibility
  Ctrl+Z          - Suspend to background
  Ctrl+V          - Paste image from clipboard
  /               - Slash commands
  !<command>      - Run bash command`
	m.showMessage(helpText)
}

func (m *InteractiveMode) getFooterData() components.FooterData {
	pwd, _ := os.Getwd()

	data := components.FooterData{
		Pwd:         pwd,
		AutoCompact: m.autoCompact,
	}

	if m.session == nil {
		return data
	}

	// Model info
	if model := m.session.Model(); model != nil {
		data.ModelID = model.ID
		data.ModelProvider = model.Provider
		data.ModelReasoning = model.Reasoning
		data.ContextWindow = model.ContextWindow
	}

	// Thinking level
	data.ThinkingLevel = m.session.ThinkingLevel()

	// Token stats from session stats
	stats := m.session.GetSessionStats()
	data.TotalInput = stats.Tokens.Input
	data.TotalOutput = stats.Tokens.Output
	data.TotalCacheRead = stats.Tokens.CacheRead
	data.TotalCacheWrite = stats.Tokens.CacheWrite
	data.TotalCost = stats.Cost

	// Git branch from footer data provider
	if m.footerDataProvider != nil {
		data.GitBranch = m.footerDataProvider.GetGitBranch()
		data.ExtensionStatuses = m.footerDataProvider.GetExtensionStatuses()
		data.MultipleProviders = m.footerDataProvider.GetAvailableProviderCount() > 1
	}

	// Session name
	data.SessionName = m.session.SessionManager.GetSessionName()

	return data
}

// ============================================================================
// Chat message management
// ============================================================================

// AddUserMessage adds a user message to the display.
func (m *InteractiveMode) AddUserMessage(text string) {
	m.addUserMessageToChat(text)
	m.ui.RequestRender(false)
}

// addUserMessageToChat adds a user message, rendering skill blocks specially.
func (m *InteractiveMode) addUserMessageToChat(text string) {
	skillBlock := core.ParseSkillBlock(text)
	if skillBlock != nil {
		m.messageContainer.AddChild(tuicomp.NewSpacer(1))
		skillComp := components.NewSkillInvocationMessageComponent(skillBlock, &m.markdownTheme)
		skillComp.SetExpanded(m.toolOutputExpanded)
		m.messageContainer.AddChild(skillComp)
		if skillBlock.UserMessage != "" {
			m.messageContainer.AddChild(components.NewUserMessageComponent(skillBlock.UserMessage, nil))
		}
	} else {
		m.messageContainer.AddChild(components.NewUserMessageComponent(text, nil))
	}
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
				m.addUserMessageToChat(txt)
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
	// Invalidate footer on every event (token counts, etc. may have changed)
	if m.footerComponent != nil {
		m.footerComponent.Invalidate()
	}

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
	// Stop any existing loading animation before creating a new one
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
	}
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
	if ae.Message == nil {
		return
	}

	// User messages: add to chat display immediately
	if u := ae.Message.AsUser(); u != nil {
		if txt, ok := u.Content.(string); ok {
			m.addUserMessageToChat(txt)
		}
		m.ui.RequestRender(false)
		return
	}

	// Assistant messages: start streaming component (spinner keeps running until agent_end)
	if msg := ae.Message.AsAssistant(); msg != nil {
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
	// Spinner keeps running in statusContainer (stopped at agent_end)
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
