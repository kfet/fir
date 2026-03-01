// It manages the TUI lifecycle, renders messages, handles user input,
// and delegates business logic to AgentSession.
package interactive

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/core/tools"
	"github.com/kfet/fir/pkg/debug"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// version is set via SetVersion before Run.
var version = "dev"

// SetVersion sets the version string shown by /session.
func SetVersion(v string) { version = v }

// InteractiveMode manages the interactive TUI session.
type InteractiveMode struct {
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
	activityContainer      *tui.Container // spinners: "Working...", "Compacting..."
	commandStatusContainer *tui.Container // transient command result messages
	footerComponent    *components.FooterComponent
	footerDataProvider *core.FooterDataProvider
	markdownTheme      tuicomp.MarkdownTheme

	// Streaming state
	streamingComponent *components.AssistantMessageComponent
	pendingTools       map[string]*components.ToolExecutionComponent
	toolOutputExpanded bool

	// Bash execution state
	bashComponent atomic.Pointer[components.BashExecutionComponent]

	// Flags
	running         bool
	shutdownRequest bool
	hideThinking    bool
	autoCompact     bool
	isBashMode      atomic.Bool

	// Theme
	themeSearchDirs []string

	// Reexec state (set by /reexec, checked after Run returns)
	reexecBinary   string
	reexecArgs     []string
	lastEscapeTime time.Time

	// Loading animation
	loadingAnimation *tuicomp.Loader

	// Cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Compaction cancellation
	compactCancel atomic.Pointer[context.CancelFunc]

	// Event subscription
	unsubscribe func()

	// Extension runner (optional)
	extensionRunner *extension.Runner

	// Extension reload support
	extSetup          *extension.SetupResult
	cliExtensionNames []string // extension names from CLI args (merged with settings on reload)

	// updateCh receives a single update notice string (or "") when the
	// background version check completes. Shown in the TUI at startup.
	updateCh <-chan string
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

	autoCompact := true
	if settings != nil {
		autoCompact = settings.GetCompactionEnabled()
	}

	m := &InteractiveMode{
		session:            session,
		keybindings:        keybindings,
		settings:           settings,
		autoCompact:        autoCompact,
		footerDataProvider: core.NewFooterDataProvider(cwd),
		ctx:                ctx,
		cancel:             cancel,
		themeSearchDirs:    opts.ThemeSearchDirs,
	}

	m.markdownTheme = itheme.GetMarkdownTheme()

	return m
}

// SetExtensionRunner sets the extension runner for command/event handling.
func (m *InteractiveMode) SetExtensionRunner(runner *extension.Runner) {
	m.extensionRunner = runner
	if runner != nil {
		runner.SetUIContext(&modeUIContext{mode: m})
	}
}

// SetExtensionSetup stores the full extension setup result and CLI extension
// names so that /reload can tear down and rebuild extensions based on updated
// settings.json while preserving CLI-provided extension names.
func (m *InteractiveMode) SetExtensionSetup(setup *extension.SetupResult, cliExtNames []string) {
	m.extSetup = setup
	m.cliExtensionNames = cliExtNames
	if setup != nil {
		m.extensionRunner = setup.Runner
		// Wire the UI context so extensions can set footer statuses and
		// show notifications. Without this, all UI calls go to the noop
		// context and are silently discarded.
		setup.Runner.SetUIContext(&modeUIContext{mode: m})
	}
}

// SetUpdateChannel supplies a channel that delivers a single update notice
// string (or "") once the background version check completes.  When the
// notice is non-empty it is shown in the TUI message area at startup.
func (m *InteractiveMode) SetUpdateChannel(ch <-chan string) {
	m.updateCh = ch
}

// startUpdateNoticeWatcher spawns a goroutine that waits for the version
// check result on m.updateCh and shows a notice if available. The goroutine
// exits silently when the mode context is cancelled.
func (m *InteractiveMode) startUpdateNoticeWatcher() {
	go func() {
		select {
		case <-m.ctx.Done():
			return
		case notice := <-m.updateCh:
			if notice != "" {
				m.showMessage(notice)
			}
		}
	}()
}

// Init initializes the TUI and components.
func (m *InteractiveMode) Init() error {
	debug.Log("interactive: initializing TUI")
	// Create terminal and TUI
	term := tui.NewProcessTerminal()
	m.ui = tui.NewTUI(term, false)

	// Create header with keybinding hints
	t := itheme.GetTheme()
	hints := t.Fg("dim", fmt.Sprintf(
		"  %s submit  %s new line  %s interrupt  /help for commands",
		components.EditorKey(tuicomp.ActSubmit),
		components.EditorKey(tuicomp.ActNewLine),
		components.EditorKey(tuicomp.ActSelectCancel),
	))
	headerText := tuicomp.NewText(hints, 0, 0, nil)
	m.ui.AddChild(headerText)
	m.ui.AddChild(tuicomp.NewSpacer(1))

	// Create message container
	m.messageContainer = &tui.Container{}
	m.ui.AddChild(m.messageContainer)

	// Create activity container (for spinners: "Working...", "Compacting...")
	m.activityContainer = &tui.Container{}
	m.ui.AddChild(m.activityContainer)

	// Create command status container (for transient command result messages)
	m.commandStatusContainer = &tui.Container{}
	m.ui.AddChild(m.commandStatusContainer)

	// Create editor container (holds editor or selector overlays)
	m.editorContainer = &tui.Container{}
	editorTheme := itheme.GetEditorTheme()
	m.editor = components.NewCustomEditor(m.ui, editorTheme, m.keybindings)
	m.editor.Prompt = "⟩ "
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

	// Render existing session history (e.g. when --continue or --resume loads
	// a previous session). This must come after subscribeToAgent so that any
	// events emitted later are also handled.
	if m.session != nil {
		if state := m.session.State(); len(state.Messages) > 0 {
			m.rebuildChatFromMessages()
		}
	}

	// Show loaded resources and any diagnostics at startup
	m.showLoadedResources()

	// When a version check channel is available, show the update notice in the
	// TUI as soon as the background check completes.  The goroutine exits early
	// if the mode's context is cancelled (user quits before check finishes).
	if m.updateCh != nil {
		m.startUpdateNoticeWatcher()
	}

	return nil
}

// showLoadedResources displays skill/prompt diagnostics (collisions, parse errors) in the chat area.
func (m *InteractiveMode) showLoadedResources() {
	if m.session == nil {
		return
	}
	t := itheme.GetTheme()

	// Show skill diagnostics
	_, skillDiags := m.session.ResourceLoader().GetSkills()
	_, promptDiags := m.session.ResourceLoader().GetPrompts()

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

	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		g := groups[name]
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
				if err := m.session.Prompt(text); err != nil {
					m.showWarning(fmt.Sprintf("Failed to send message: %v", err))
				}
			}()
		}
	}

	// Escape handler with double-escape support
	m.editor.OnEscape = func() {
		// If compaction is in progress, cancel it
		compactCancel := m.compactCancel.Load()
		if compactCancel != nil {
			(*compactCancel)()
			return
		}

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
		inBash := m.isBashMode.Load()
		if inBash {
			m.editor.SetText("")
			m.isBashMode.Store(false)
			m.updateEditorBorderColor()
			return
		}

		// Clear command status on Escape (dismiss stale messages)
		m.commandStatusContainer.Clear()
		if m.ui != nil {
			m.ui.RequestRender(false)
		}

		// Double-escape with empty editor
		if strings.TrimSpace(m.editor.GetText()) == "" {
			action := "tree"
			if m.settings != nil {
				action = m.settings.GetDoubleEscapeAction()
			}
			if action != "none" {
				now := time.Now()
				if now.Sub(m.lastEscapeTime) < 500*time.Millisecond {
					if action == "tree" {
						m.showTreeSelector()
					} else {
						m.showUserMessageSelector()
					}
					m.lastEscapeTime = time.Time{}
				} else {
					m.lastEscapeTime = now
				}
			}
		}
	}

	// Ctrl+D handler
	m.editor.OnCtrlD = func() {
		m.Shutdown()
	}

	// Register app action handlers
	m.editor.OnAction(core.ActionSelectModel, func() {
		m.showModelSelector("")
	})
	m.editor.OnAction(core.ActionSelectThinking, func() {
		m.showThinkingSelector()
	})
	m.editor.OnAction(core.ActionExpandTools, func() {
		m.toggleToolOutputExpansion()
	})
	m.editor.OnAction(core.ActionToggleThinking, func() {
		m.toggleThinkingBlockVisibility()
	})
	m.editor.OnAction(core.ActionCycleThinkingLevel, func() {
		m.cycleThinkingLevel()
	})
	m.editor.OnAction(core.ActionCycleModelForward, func() {
		m.cycleModel("forward")
	})
	m.editor.OnAction(core.ActionCycleModelBackward, func() {
		m.cycleModel("backward")
	})
	m.editor.OnAction(core.ActionNewSession, func() {
		go m.handleClearCommand()
	})
	m.editor.OnAction(core.ActionTree, func() {
		m.showTreeSelector()
	})
	m.editor.OnAction(core.ActionFork, func() {
		m.showUserMessageSelector()
	})
	m.editor.OnAction(core.ActionResume, func() {
		m.showSessionSelector()
	})
	m.editor.OnAction(core.ActionClear, func() {
		m.handleCtrlC()
	})
	m.editor.OnAction(core.ActionSuspend, func() {
		m.handleCtrlZ()
	})
	m.editor.OnAction(core.ActionExternalEditor, func() {
		m.handleExternalEditor()
	})

	// Clipboard image paste (Ctrl+V inserts the image path into editor)
	m.editor.OnPasteImage = func() {
		go m.handleClipboardImagePaste()
	}
	m.editor.OnAction(core.ActionFollowUp, func() {
		text := strings.TrimSpace(m.editor.GetText())
		if text == "" {
			return
		}
		m.editor.AddToHistory(text)
		m.editor.SetText("")
		if m.session != nil {
			go func() {
				_ = m.session.Prompt(text, &core.PromptOptions{StreamingBehavior: "followUp"})
			}()
		}
	})
	m.editor.OnAction(core.ActionDequeue, func() {
		m.handleDequeue()
	})

	// Track bash mode on text change
	m.editor.OnChange = func(text string) {
		inBashMode := strings.HasPrefix(strings.TrimLeft(text, " \t"), "!")
		wasBashMode := m.isBashMode.Swap(inBashMode)
		changed := wasBashMode != inBashMode
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

	// Add skill commands and prompt templates from the resource loader
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
			if prompts, _ := rl.GetPrompts(); len(prompts) > 0 {
				for _, prompt := range prompts {
					commands = append(commands, SlashCommand{
						Name:        prompt.Name,
						Description: prompt.Description,
					})
				}
			}
		}
	}

	// Add extension commands
	if m.extensionRunner != nil {
		for name, cmd := range m.extensionRunner.GetCommands() {
			commands = append(commands, SlashCommand{
				Name:        name,
				Description: cmd.Description,
			})
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
	if !strings.HasPrefix(cmd, "/") {
		return false
	}
	if core.IsBuiltinSlashCommandName(cmd[1:]) {
		return true
	}
	// Check extension commands
	if m.extensionRunner != nil {
		extCmd := cmd[1:] // strip "/"
		if m.extensionRunner.GetCommand(extCmd) != nil {
			return true
		}
	}
	return false
}

// handleSlashCommand dispatches a builtin slash command.
// Every case in the switch below must have a corresponding entry in
// core.BuiltinSlashCommands (or builtinAliases for hidden aliases);
// TestInteractiveMode_IsBuiltinSlashCommand enforces this.
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
	case "/skills":
		m.handleSkillsCommand(parts[1:])
	case "/reexec":
		m.handleReexecCommand()
	case "/queue":
		m.handleQueueCommand()
	case "/dequeue":
		var arg string
		if len(parts) > 1 {
			arg = parts[1]
		}
		m.handleDequeueCommand(arg)
	case "/quit", "/exit":
		m.Shutdown()
	default:
		// Try extension commands (strip leading /)
		extCmd := cmd[1:] // remove "/"
		extArgs := ""
		if len(parts) > 1 {
			extArgs = strings.Join(parts[1:], " ")
		}
		if m.extensionRunner != nil {
			if found, err := m.extensionRunner.ExecuteCommand(extCmd, extArgs); found {
				if err != nil {
					m.showWarning(fmt.Sprintf("Extension command error: %v", err))
				}
				return
			}
		}
		// Fall through: not a builtin and not an extension command.
		// Check if it's a skill or prompt template command before declaring unknown.
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
			AutoCompact:             m.autoCompact,
			HideThinkingBlock:       m.hideThinking,
			ThinkingLevel:           m.session.ThinkingLevel(),
			AvailableThinkingLevels: levelStrs,
			CurrentTheme:            itheme.GetTheme().Name,
			AvailableThemes:         itheme.GetAvailableThemes(m.themeSearchDirs),
			SteeringMode:            m.settings.GetSteeringMode(),
			FollowUpMode:            m.settings.GetFollowUpMode(),
			Transport:               m.settings.GetTransport(),
			DoubleEscapeAction:      m.settings.GetDoubleEscapeAction(),
			AutocompleteMaxVisible:  10,
		}
		callbacks := components.SettingsCallbacks{
			OnAutoCompactChange: func(v bool) {
				m.autoCompact = v
				m.settings.SetCompactionEnabled(v)
			},
			OnHideThinkingBlockChange: func(v bool) { m.hideThinking = v },
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
			OnCancel: func() { done() },
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
		var cwd, sessionDir string
		if m.session != nil && m.session.SessionManager != nil {
			cwd = m.session.SessionManager.GetCwd()
			sessionDir = m.session.SessionManager.GetSessionDir()
		}
		sessions, _ := core.ListSessions(cwd, sessionDir)
		selector := components.NewSessionSelectorComponent(
			sessions,
			components.SessionScopeCurrent,
			func() ([]core.SessionListInfo, error) {
				return core.ListAllSessions(core.DefaultAgentDir(), core.PiAgentDir())
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

	m.executeCompaction(customInstructions)
}

func (m *InteractiveMode) executeCompaction(customInstructions string) {
	t := itheme.GetTheme()

	// Set up a cancellable context so ESC can abort the in-flight LLM call.
	ctx, cancel := context.WithCancel(context.Background())
	m.compactCancel.Store(&cancel)
	defer func() {
		m.compactCancel.Store(nil)
		cancel()
	}()

	// Fetch pre-run stats so we can show message/token counts immediately.
	info := m.session.GetCompactionStats()

	// Clear status and show initial compacting indicator with stats.
	m.activityContainer.Clear()
	loader := tuicomp.NewLoader(
		m.ui.AsRenderRequester(),
		func(spinner string) string { return t.Fg("accent", spinner) },
		func(text string) string { return t.Fg("muted", text) },
		m.compactionLoaderLabel(info, "(Esc to cancel)"),
	)
	m.activityContainer.AddChild(loader)
	m.ui.RequestRender(false)

	// Attach a streaming progress callback that updates the label as the LLM writes.
	var writtenChars int
	progressFn := func(phase, delta string) {
		writtenChars += len(delta)
		tokensWritten := writtenChars / 4
		label := m.compactionLoaderLabel(info, fmt.Sprintf("%s... %d tokens written (Esc to cancel)", phase, tokensWritten))
		loader.SetMessage(label)
	}
	ctx = core.WithCompactionProgress(ctx, progressFn)

	result, err := m.session.RunCompaction(ctx, customInstructions)
	loader.Stop()
	m.activityContainer.Clear()

	// If cancelled, just show it and stop
	if err != nil && ctx.Err() != nil {
		m.showStatus("Compaction cancelled")
		m.ui.RequestRender(false)
		return
	}

	// Show any error (but continue to check for pending work)
	if err != nil {
		m.showWarning(fmt.Sprintf("Compaction failed: %s", err))
	}

	// Rebuild chat from compacted session (whether success or failure)
	m.rebuildChatFromMessages()

	// If pending work, resume it; otherwise show completion status
	if m.session.HasPendingWork() {
		// Show "Working..." spinner and resume
		loader := tuicomp.NewLoader(
			m.ui.AsRenderRequester(),
			func(spinner string) string { return t.Fg("accent", spinner) },
			func(text string) string { return t.Fg("muted", text) },
			"Working...",
		)
		m.loadingAnimation = loader
		m.activityContainer.AddChild(loader)
		go func() { _ = m.session.Agent.Continue() }()
	} else if result != nil {
		// Compaction succeeded and no pending work - just show status
		m.showStatus(fmt.Sprintf("Compacted: %d tokens", result.TokensBefore))
	}

	m.ui.RequestRender(false)
}

// compactionLoaderLabel builds the loader message string shown during compaction.
// info may be nil (no stats known yet). suffix is appended after the stats.
func (m *InteractiveMode) compactionLoaderLabel(info *core.CompactionInfo, suffix string) string {
	if info == nil {
		if suffix != "" {
			return "Compacting context... " + suffix
		}
		return "Compacting context..."
	}
	t := itheme.GetTheme()
	stats := fmt.Sprintf("%s msgs, ~%s tokens",
		t.Fg("accent", fmt.Sprintf("%d", info.MessagesToSummarize)),
		t.Fg("accent", compactionFormatTokens(info.TokensBefore)),
	)
	if suffix != "" {
		return fmt.Sprintf("Compacting %s — %s", stats, suffix)
	}
	return fmt.Sprintf("Compacting %s", stats)
}

// compactionFormatTokens formats a token count for display (e.g. "95k").
func compactionFormatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}

// ============================================================================
// Bash execution
// ============================================================================

func (m *InteractiveMode) handleBashCommand(command string, excludeFromContext bool) {
	// Create UI component for display
	bashComp := components.NewBashExecutionComponent(command, m.ui, excludeFromContext)
	m.bashComponent.Store(bashComp)
	m.messageContainer.AddChild(bashComp)
	m.ui.RequestRender(false)

	if m.session != nil {
		result, err := m.session.ExecuteBashWithOptions(command, func(chunk string) {
			bc := m.bashComponent.Load()
			if bc != nil {
				bc.AppendOutput(chunk)
				m.ui.RequestRender(false)
			}
		}, excludeFromContext)

		bc := m.bashComponent.Load()
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

	m.bashComponent.Store(nil)
	m.isBashMode.Store(false)
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

// handleDequeue restores any queued follow-up messages to the editor.
func (m *InteractiveMode) handleDequeue() {
	if m.session == nil {
		return
	}
	queued := m.session.ClearFollowUpQueue()
	if len(queued) == 0 {
		m.showStatus("No queued messages to restore")
		return
	}
	current := strings.TrimSpace(m.editor.GetText())
	parts := make([]string, len(queued)+1)
	copy(parts, queued)
	parts[len(queued)] = current
	var nonEmpty []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	m.editor.SetText(strings.Join(nonEmpty, "\n\n"))
	m.ui.RequestRender(false)
	m.showStatus(fmt.Sprintf("Restored %d queued message(s) to editor", len(queued)))
}

// handleQueueCommand shows the current follow-up message queue as a status message.
func (m *InteractiveMode) handleQueueCommand() {
	if m.session == nil {
		return
	}
	texts := m.session.PeekFollowUpQueue()
	if len(texts) == 0 {
		m.showStatus("Queue is empty")
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Queue (%d message(s)):\n", len(texts))
	for i, t := range texts {
		preview := strings.ReplaceAll(t, "\n", " ")
		if runes := []rune(preview); len(runes) > 80 {
			preview = string(runes[:77]) + "…"
		}
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, preview)
	}
	m.showStatus(strings.TrimRight(sb.String(), "\n"))
}

// handleDequeueCommand is the slash-command version of handleDequeue.
// With no arg it behaves identically to Alt+Up (dequeue all).
// With a numeric arg it removes only that 1-based item and restores it to the editor.
func (m *InteractiveMode) handleDequeueCommand(arg string) {
	if m.session == nil {
		return
	}
	if arg == "" {
		m.handleDequeue()
		return
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		m.showWarning(fmt.Sprintf("/dequeue: invalid index %q (must be a positive integer)", arg))
		return
	}
	text, ok := m.session.RemoveFollowUp(n)
	if !ok {
		qlen := m.session.Agent.FollowUpQueueLen()
		m.showWarning(fmt.Sprintf("/dequeue: no message at index %d (queue has %d message(s))", n, qlen))
		return
	}
	current := strings.TrimSpace(m.editor.GetText())
	if current != "" && text != "" {
		m.editor.SetText(text + "\n\n" + current)
	} else {
		m.editor.SetText(text)
	}
	m.ui.RequestRender(false)
	m.showStatus(fmt.Sprintf("Restored message %d to editor", n))
}

func (m *InteractiveMode) handleCtrlZ() {
	// Send SIGTSTP to self (suspend)
	// On most systems, this suspends the process
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		_ = p.Signal(suspendSignal())
	}
}

func (m *InteractiveMode) handleClipboardImagePaste() {
	img := core.ReadClipboardImage()
	if img == nil {
		return // no image on clipboard, silently ignore
	}

	ext := core.ExtensionForImageMimeType(img.MimeType)
	if ext == "" {
		ext = "png"
	}
	tmpFile, err := os.CreateTemp("", "fir-clipboard-*."+ext)
	if err != nil {
		return // silently ignore clipboard errors
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(img.Bytes); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return
	}
	tmpFile.Close()

	m.editor.InsertTextAtCursor(tmpPath)
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) handleExternalEditor() {
	editorCmd := os.Getenv("VISUAL")
	if editorCmd == "" {
		editorCmd = os.Getenv("EDITOR")
	}
	if editorCmd == "" {
		m.showWarning("No editor configured. Set $VISUAL or $EDITOR environment variable.")
		return
	}

	currentText := m.editor.GetText()

	tmpFile, err := os.CreateTemp("", "fir-editor-*.md")
	if err != nil {
		m.showWarning(fmt.Sprintf("Failed to create temp file: %s", err))
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(currentText); err != nil {
		tmpFile.Close()
		m.showWarning(fmt.Sprintf("Failed to write temp file: %s", err))
		return
	}
	tmpFile.Close()

	// Stop TUI to release the terminal for the external editor.
	m.ui.Stop()

	// Use the shell to launch the editor so that paths with spaces and
	// shell metacharacters in $VISUAL/$EDITOR work correctly.
	// "sh -c 'editorCmd "$1"' -- path" passes the path as $1 without
	// any word-splitting or glob expansion on the path itself.
	cmd := exec.Command("sh", "-c", editorCmd+` "$1"`, "--", tmpPath) //nolint:gosec
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	exitErr := cmd.Run()

	// Restart TUI regardless of exit code.
	m.ui.Start()
	m.ui.RequestRender(true)

	if exitErr == nil {
		newContent, err := os.ReadFile(tmpPath)
		if err == nil {
			// Trim trailing newline to match editor convention.
			m.editor.SetText(strings.TrimRight(string(newContent), "\n"))
		}
	}
	// On non-zero exit keep the original text (no-op).
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
	if m.activityContainer != nil {
		m.activityContainer.Clear()
	}
	if m.commandStatusContainer != nil {
		m.commandStatusContainer.Clear()
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
		if cred := authStorage.Get(p.ID()); cred != nil && cred.Type == core.CredentialTypeOAuth {
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
			m.showMessage(msg)
			m.showStatus(msg)
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
		for _, sm := range core.ResolveModelScope(patterns, registry) {
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
		resolved := core.ResolveModelScope(patterns, registry)
		currentThinkingLevel := m.session.ThinkingLevel()
		scoped := make([]core.ScopedModel, len(resolved))
		for i, sm := range resolved {
			level := sm.ThinkingLevel
			if level == "" {
				level = currentThinkingLevel
			}
			scoped[i] = core.ScopedModel{Model: sm.Model, ThinkingLevel: level}
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

	items := make([]components.UserMessageItem, len(userMsgs))
	for i, msg := range userMsgs {
		items[i] = components.UserMessageItem{
			ID:   msg.EntryID,
			Text: msg.Text,
		}
	}

	m.showSelector(func(done func()) (tui.Component, tui.Component) {
		selector := components.NewUserMessageSelectorComponent(
			items,
			func(entryID string) {
				done()
				go m.handleFork(entryID)
			},
			func() { done() },
		)
		return selector, selector.GetMessageList()
	})
}

// handleFork creates a fork from entryID and refreshes the chat display.
func (m *InteractiveMode) handleFork(entryID string) {
	selectedText, cancelled, err := m.session.Fork(entryID)
	if err != nil {
		m.showWarning(fmt.Sprintf("Fork failed: %s", err))
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
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	// Parse optional output path: /export [path]
	var outputPath string
	parts := strings.Fields(text)
	if len(parts) >= 2 {
		outputPath = parts[1]
	}
	go func() {
		filePath, err := m.session.ExportToHTML(outputPath)
		if err != nil {
			m.showWarning(fmt.Sprintf("Failed to export session: %s", err))
			return
		}
		m.showStatus(fmt.Sprintf("Session exported to: %s", filePath))
	}()
}

func (m *InteractiveMode) handleShareCommand() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	go m.performShare()
}

func (m *InteractiveMode) performShare() {
	// Check that gh CLI is available and authenticated.
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		m.showWarning("GitHub CLI is not logged in. Run 'gh auth login' first.")
		return
	}

	// Export to a temp file.
	tmpPath, err := m.session.ExportToHTML("")
	if err != nil {
		m.showWarning(fmt.Sprintf("Failed to export session: %s", err))
		return
	}
	defer os.Remove(tmpPath)

	// Show a loader in the editor container while the gist is being created.
	t := itheme.GetTheme()
	loader := components.NewBorderedLoader(
		m.ui.AsRenderRequester(),
		t,
		"Creating gist...",
		nil,
	)

	var procPtr atomic.Pointer[exec.Cmd]
	loader.SetOnAbort(func() {
		if p := procPtr.Load(); p != nil && p.Process != nil {
			_ = p.Process.Kill()
		}
		m.editorContainer.Clear()
		m.editorContainer.AddChild(m.editor)
		m.ui.SetFocus(m.editor)
		m.ui.RequestRender(false)
		m.showStatus("Share cancelled")
	})

	m.editorContainer.Clear()
	m.editorContainer.AddChild(loader)
	m.ui.SetFocus(loader)
	m.ui.RequestRender(true)

	restoreEditor := func() {
		loader.Dispose()
		m.editorContainer.Clear()
		m.editorContainer.AddChild(m.editor)
		m.ui.SetFocus(m.editor)
		m.ui.RequestRender(false)
	}

	cmd := exec.Command("gh", "gist", "create", "--public=false", tmpPath)
	procPtr.Store(cmd)
	out, err := cmd.Output()
	restoreEditor()
	if err != nil {
		m.showWarning("Failed to create gist. Check that 'gh' is installed and authenticated.")
		return
	}
	gistURL := strings.TrimSpace(string(out))
	if gistURL == "" {
		m.showWarning("Gist created but no URL returned")
		return
	}
	link := core.Hyperlink(gistURL, gistURL)
	m.showStatus(fmt.Sprintf("Session shared: %s", link))
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
	lines = append(lines, t.Fg("dim", "Version: ")+version)
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

	// Extensions section
	if m.extensionRunner != nil {
		exts := m.extensionRunner.Extensions()
		if len(exts) > 0 {
			lines = append(lines, "")
			lines = append(lines, t.Bold("Extensions"))
			for _, ext := range exts {
				lines = append(lines, fmt.Sprintf("  %s", ext.Name))
			}
		}
		tools := m.extensionRunner.GetTools()
		if len(tools) > 0 {
			items := make(map[string]string, len(tools))
			for name, td := range tools {
				items[name] = td.Label
			}
			lines = append(lines, formatSortedSection(t, "Extension Tools", "", items)...)
		}
		cmds := m.extensionRunner.GetCommands()
		if len(cmds) > 0 {
			items := make(map[string]string, len(cmds))
			for name, cmd := range cmds {
				items[name] = cmd.Description
			}
			lines = append(lines, formatSortedSection(t, "Extension Commands", "/", items)...)
		}
		shortcuts := m.extensionRunner.GetShortcuts()
		if len(shortcuts) > 0 {
			items := make(map[string]string, len(shortcuts))
			for key, sh := range shortcuts {
				items[key] = sh.Description
			}
			lines = append(lines, formatSortedSection(t, "Extension Shortcuts", "", items)...)
		}
	}

	m.showMessage(strings.Join(lines, "\n"))
}

// formatSortedSection formats a titled section with sorted key-description pairs.
// The prefix is prepended to each key (e.g. "/" for commands).
func formatSortedSection(t *itheme.Theme, title, prefix string, items map[string]string) []string {
	lines := []string{"", t.Bold(title)}
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if desc := items[k]; desc != "" {
			lines = append(lines, fmt.Sprintf("  %s %s", t.Fg("dim", prefix+k), desc))
		} else {
			lines = append(lines, fmt.Sprintf("  %s", prefix+k))
		}
	}
	return lines
}

func (m *InteractiveMode) handleChangelogCommand() {
	entries := core.GetChangelogEntries()
	if len(entries) == 0 {
		m.showMessage("No changelog entries found.")
		return
	}

	// Entries come newest-first from the changelog file.
	// Display oldest-first so the newest version appears at the bottom of the terminal
	// where the user's eyes are.
	t := itheme.GetTheme()
	border := t.Fg("dim", "───")
	var lines []string
	lines = append(lines, border+" "+t.Fg("muted", "Changelog")+" "+border)
	lines = append(lines, "")
	for i := len(entries) - 1; i >= 0; i-- {
		lines = append(lines, formatChangelogEntry(t, entries[i])...)
		if i > 0 {
			lines = append(lines, "")
		}
	}
	lines = append(lines, "")
	lines = append(lines, border+"────────"+border)
	m.showMessage(strings.Join(lines, "\n"))
}

// formatChangelogEntry renders a single changelog entry with theme colors.
func formatChangelogEntry(t *itheme.Theme, entry core.ChangelogEntry) []string {
	var out []string
	for _, line := range strings.Split(entry.Content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "):
			// Version header: e.g. "## [0.5.0] - 2026-02-24"
			// Extract just "v0.5.0" and optional date
			header := strings.TrimPrefix(trimmed, "## ")
			out = append(out, t.Bold(t.Fg("mdHeading", "  "+header)))
		case strings.HasPrefix(trimmed, "### "):
			// Subsection: Added, Fixed, Changed, Removed
			section := strings.TrimPrefix(trimmed, "### ")
			var color string
			switch section {
			case "Added":
				color = "success"
			case "Fixed":
				color = "accent"
			case "Changed":
				color = "warning"
			case "Removed":
				color = "error"
			default:
				color = "muted"
			}
			out = append(out, "    "+t.Bold(t.Fg(color, section)))
		case strings.HasPrefix(trimmed, "- "):
			// Bullet item
			bullet := t.Fg("mdListBullet", "•")
			text := strings.TrimPrefix(trimmed, "- ")
			out = append(out, "      "+bullet+" "+text)
		case trimmed == "":
			// skip blank lines between sections
		default:
			out = append(out, "      "+trimmed)
		}
	}
	return out
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

	// Reload session (re-reads settings.json, skills, prompts, system prompt).
	if err := m.session.Reload(); err != nil {
		m.showWarning(fmt.Sprintf("Reload failed: %v", err))
		return
	}

	// Reload extensions if setup is available.
	if m.extSetup != nil {
		enabledNames := m.resolveEnabledExtensions()
		if err := m.extSetup.Reload(enabledNames); err != nil {
			m.showWarning(fmt.Sprintf("Extension reload failed: %v", err))
			// Continue — skills/prompts were already reloaded successfully.
		}
	}

	m.setupAutocomplete()
	m.rebuildChatFromMessages()
	m.showStatus("Reloaded extensions, skills, prompts, themes")
}

func (m *InteractiveMode) handleSkillsCommand(args []string) {
	if len(args) == 0 || args[0] == "list" {
		m.handleSkillsList()
		return
	}
	if args[0] == "install" {
		if len(args) < 2 {
			m.showWarning("Usage: /skills install <name>")
			return
		}
		m.handleSkillsInstall(args[1])
		return
	}
	m.showWarning(fmt.Sprintf("Unknown skills subcommand: %s. Usage: /skills [list | install <name>]", args[0]))
}

func (m *InteractiveMode) handleSkillsList() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}

	skills, _ := m.session.ResourceLoader().GetSkills()
	if len(skills) == 0 {
		m.showStatus("No skills loaded.")
		return
	}

	// Sort by name
	sorted := make([]core.Skill, len(skills))
	copy(sorted, skills)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	nameW := 4
	sourceW := 6
	for _, s := range sorted {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
		if len(s.Source) > sourceW {
			sourceW = len(s.Source)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-*s  %-*s  %s\n", nameW, "NAME", sourceW, "SOURCE", "DESCRIPTION"))
	for _, s := range sorted {
		desc := s.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		sb.WriteString(fmt.Sprintf("%-*s  %-*s  %s\n", nameW, s.Name, sourceW, s.Source, desc))
	}

	m.showStatus(strings.TrimRight(sb.String(), "\n"))
}

func (m *InteractiveMode) handleSkillsInstall(name string) {
	builtins := core.LoadBuiltinSkills()
	var found bool
	for _, s := range builtins.Skills {
		if s.Name == name {
			found = true
			break
		}
	}
	if !found {
		available := make([]string, 0, len(builtins.Skills))
		for _, s := range builtins.Skills {
			available = append(available, s.Name)
		}
		sort.Strings(available)
		m.showWarning(fmt.Sprintf("Unknown builtin skill %q. Available: %s", name, strings.Join(available, ", ")))
		return
	}

	cwd, _ := os.Getwd()
	targetDir := filepath.Join(cwd, ".fir", "skills", name)

	if _, err := os.Stat(targetDir); err == nil {
		m.showWarning(fmt.Sprintf("Skill %q already exists at %s", name, targetDir))
		return
	}

	prefix := "builtin_skills/" + name
	err := fs.WalkDir(core.BuiltinSkillsFS, prefix, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := strings.TrimPrefix(path, prefix)
		if rel == "" {
			return nil
		}
		rel = strings.TrimPrefix(rel, "/")
		target := filepath.Join(targetDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, readErr := core.BuiltinSkillsFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		m.showWarning(fmt.Sprintf("Failed to install skill %q: %v", name, err))
		return
	}

	// Reload so the newly installed skill is picked up
	if m.session != nil {
		_ = m.session.Reload()
		m.setupAutocomplete()
	}

	m.showStatus(fmt.Sprintf("Installed skill %q to %s (project)", name, targetDir))
}

func (m *InteractiveMode) handleReexecCommand() {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	if m.session.IsStreaming() {
		m.showWarning("Wait for the current response to finish.")
		return
	}

	binary, err := os.Executable()
	if err != nil {
		m.showWarning(fmt.Sprintf("Cannot determine executable path: %v", err))
		return
	}

	sessionFile := m.session.SessionManager.GetSessionFile()
	sessionDir := m.session.SessionManager.GetSessionDir()
	if sessionFile == "" {
		m.showWarning("No persisted session to resume after reexec")
		return
	}

	sessionBase := filepath.Base(sessionFile)

	// Store reexec intent — the actual exec happens after Run() returns.
	m.reexecBinary = binary
	m.reexecArgs = []string{binary, "--session-dir", sessionDir, "--session", sessionBase}
	m.Shutdown()
}

// ReexecIfRequested performs the syscall.Exec if /reexec was invoked.
// Call this after Run() returns. It never returns on success.
func (m *InteractiveMode) ReexecIfRequested() {
	if m.reexecBinary == "" {
		return
	}
	if err := syscall.Exec(m.reexecBinary, m.reexecArgs, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "reexec failed: exec %s: %v\n", m.reexecBinary, err)
		os.Exit(1)
	}
}

// resolveEnabledExtensions merges extension names from the (freshly reloaded)
// settings with the CLI-provided extension names.
func (m *InteractiveMode) resolveEnabledExtensions() []string {
	names := m.settings.GetEnabledExtensions()

	if len(m.cliExtensionNames) > 0 {
		seen := make(map[string]bool, len(names))
		for _, n := range names {
			seen[n] = true
		}
		for _, n := range m.cliExtensionNames {
			if !seen[n] {
				names = append(names, n)
				seen[n] = true
			}
		}
	}

	return names
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
	m.settings.SetDefaultThinkingLevel(string(next))
	m.footerComponent.Invalidate()
	m.showStatus(fmt.Sprintf("Thinking: %s", next))
}

func (m *InteractiveMode) cycleModel(direction string) {
	// When scoped models are configured, cycle only within that set.
	scopedModels := m.session.ScopedModelsRef()
	if len(scopedModels) > 0 {
		registry := m.session.ModelRegistryRef()
		// Filter to models that have API keys available.
		availableSet := make(map[string]bool)
		for _, model := range registry.GetAvailable() {
			availableSet[model.Provider+"/"+model.ID] = true
		}
		var available []*ai.Model
		for _, sm := range scopedModels {
			if availableSet[sm.Model.Provider+"/"+sm.Model.ID] {
				available = append(available, sm.Model)
			}
		}
		if len(available) <= 1 {
			m.showStatus("Only one model in scope")
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
		var nextIdx int
		if direction == "forward" {
			nextIdx = (idx + 1) % len(available)
		} else {
			nextIdx = (idx - 1 + len(available)) % len(available)
		}
		m.session.SetModel(available[nextIdx])
		m.footerComponent.Invalidate()
		m.updateEditorBorderColor()
		m.showStatus(fmt.Sprintf("Model: %s", available[nextIdx].ID))
		return
	}

	// Default: cycle through all available models.
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
	// Update all expandable components in the message container
	for _, child := range m.messageContainer.ChildrenSnapshot() {
		if ec, ok := child.(components.Expandable); ok {
			ec.SetExpanded(m.toolOutputExpanded)
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
	return m.isBashMode.Load()
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
	if m.commandStatusContainer == nil {
		return
	}
	t := itheme.GetTheme()
	m.commandStatusContainer.Clear()
	m.commandStatusContainer.AddChild(tuicomp.NewText(t.Fg("success", message), 1, 0, nil))
	if m.ui != nil {
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) showWarning(message string) {
	if m.commandStatusContainer == nil {
		return
	}
	t := itheme.GetTheme()
	m.commandStatusContainer.Clear()
	m.commandStatusContainer.AddChild(tuicomp.NewText(t.Fg("warning", message), 1, 0, nil))
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
  /skills         - List loaded skills (or /skills install <name>)
  /reexec        - Re-exec into the current binary, preserving the session
  /quit           - Quit fir

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

	// Context usage estimate (uses last assistant message's usage, not accumulated totals)
	if cu := m.session.GetContextUsage(); cu != nil {
		data.ContextPercent = cu.Percent
		data.ContextTokens = cu.Tokens
	}

	// Git branch from footer data provider
	if m.footerDataProvider != nil {
		data.GitBranch = m.footerDataProvider.GetGitBranch()
		data.ExtensionStatuses = m.footerDataProvider.GetExtensionStatuses()
		data.MultipleProviders = m.footerDataProvider.GetAvailableProviderCount() > 1
	}

	// Session name
	data.SessionName = m.session.SessionManager.GetSessionName()

	// Queued follow-up messages
	data.QueuedMessages = m.session.Agent.FollowUpQueueLen()

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
	m.activityContainer.Clear()
	m.commandStatusContainer.Clear()

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
			t := itheme.GetTheme()
			// Build an initial label that shows message/token counts if available.
			initialLabel := m.compactionLoaderLabel(event.CompactionInfo, "(auto)")
			loader := tuicomp.NewLoader(
				m.ui.AsRenderRequester(),
				func(spinner string) string { return t.Fg("accent", spinner) },
				func(text string) string { return t.Fg("muted", text) },
				initialLabel,
			)
			m.activityContainer.Clear()
			m.activityContainer.AddChild(loader)
			m.loadingAnimation = loader
			m.ui.RequestRender(false)

			// Provide a progress callback so the loader text updates during streaming.
			var writtenChars int
			m.session.SetAutoCompactionProgress(func(phase, delta string) {
				writtenChars += len(delta)
				tokensWritten := writtenChars / 4
				label := m.compactionLoaderLabel(event.CompactionInfo,
					fmt.Sprintf("%s... %d tokens written (auto)", phase, tokensWritten))
				loader.SetMessage(label)
			})

		case "auto_compaction_end":
			if m.loadingAnimation != nil {
				m.loadingAnimation.Stop()
				m.loadingAnimation = nil
			}
			// Show any error
			if event.ErrorMessage != "" {
				m.showWarning("Compaction failed: " + event.ErrorMessage)
			}
			// Rebuild chat from compacted session
			m.rebuildChatFromMessages()

			// If no pending work and successful, show completion status
			if event.ErrorMessage == "" && event.CompactionResult != nil && !m.session.HasPendingWork() {
				m.showStatus(fmt.Sprintf("Auto-compacted: %d tokens", event.CompactionResult.TokensBefore))
				// Notify extensions that the auto-compaction phase is done
				if m.extensionRunner != nil {
					_ = m.extensionRunner.EmitAgentEnd(nil)
				}
			}
			// If pending work, agent will resume naturally via EventAgentStart (no notification needed here)
			m.ui.RequestRender(false)
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
	m.activityContainer.Clear()
	t := itheme.GetTheme()
	loader := tuicomp.NewLoader(
		m.ui.AsRenderRequester(),
		func(spinner string) string { return t.Fg("accent", spinner) },
		func(text string) string { return t.Fg("muted", text) },
		"Working...",
	)
	m.loadingAnimation = loader
	m.activityContainer.AddChild(loader)
	m.streamingComponent = nil
	m.ui.RequestRender(false)

	// Notify extensions that work is starting
	if m.extensionRunner != nil {
		_ = m.extensionRunner.EmitAgentStart()
	}
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
	// Spinner keeps running in activityContainer (stopped at agent_end)
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
	m.activityContainer.Clear()
	m.streamingComponent = nil
	m.pendingTools = make(map[string]*components.ToolExecutionComponent)
	m.ui.RequestRender(false)

	// Notify extensions that agent work is ending
	if m.extensionRunner != nil {
		_ = m.extensionRunner.EmitAgentEnd(nil)
	}
}

// ============================================================================
// Extension UIContext bridge
// ============================================================================

// modeUIContext bridges the extension.UIContext interface to the interactive
// mode's TUI and FooterDataProvider. Without this bridge, extensions fall
// back to the noop UI context and their SetStatus/Notify calls are silently
// discarded.
type modeUIContext struct {
	mode *InteractiveMode
}

var _ extension.UIContext = (*modeUIContext)(nil)

func (u *modeUIContext) Select(title string, options []string) (string, error) {
	// Not implemented for extension UI context — extensions shouldn't block
	// with interactive selectors during event handlers.
	return "", nil
}

func (u *modeUIContext) Confirm(title string, message string) (bool, error) {
	return false, nil
}

func (u *modeUIContext) Input(title string, placeholder string) (string, error) {
	return "", nil
}

func (u *modeUIContext) Notify(message string, level string) {
	switch level {
	case "error":
		u.mode.showWarning(message)
	case "warning":
		u.mode.showWarning(message)
	default:
		u.mode.showMessage(message)
	}
}

func (u *modeUIContext) SetStatus(key string, text string) {
	if u.mode.footerDataProvider != nil {
		u.mode.footerDataProvider.SetExtensionStatus(key, text)
		if u.mode.ui != nil {
			u.mode.ui.RequestRender(false)
		}
	}
}

func (u *modeUIContext) SetWidget(key string, lines []string) {
	// Widget support not yet implemented for interactive mode extensions.
}

func (u *modeUIContext) ClearWidget(key string) {
	// Widget support not yet implemented for interactive mode extensions.
}
