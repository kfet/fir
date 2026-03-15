// It manages the TUI lifecycle, renders messages, handles user input,
// and delegates business logic to AgentSession.
package interactive

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/extension"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/resources/clipboard"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
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
	session     *session.AgentSession
	keybindings *tui.KeybindingsManager
	settings    *config.SettingsManager

	// TUI
	ui              *tui.TUI
	editor          *components.CustomEditor
	editorContainer *tui.Container // holds editor or selector overlay

	// State
	messageContainer       *tui.Container
	activityContainer      *tui.Container // spinners: "Working...", "Compacting..."
	commandStatusContainer *tui.Container // transient command result messages
	planContainer          *tui.Container // plan visualization
	planComponent          *components.PlanComponent
	planHidden             bool // true = plan widget collapsed (footer still shows progress)
	planInContainer        bool // true = planComponent is currently a child of planContainer
	footerComponent        *components.FooterComponent
	footerDataProvider     *FooterDataProvider
	markdownTheme          tuicomp.MarkdownTheme

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
	autoCompactMode string // "off", "client", "server"
	isBashMode      atomic.Bool

	// Theme
	themeSearchDirs []string

	// Reexec state (set by /reexec, checked after Run returns)
	reexecBinary   string
	reexecArgs     []string
	lastEscapeTime time.Time

	// reexecSidecar is the full sidecar read during Init(); consumed by
	// restoreReexecSidecar in Run().  ReexecExtData() exposes the extension
	// data portion so app.go can pass it to EmitSessionStart.
	reexecSidecar *store.ReexecSidecar

	// Loading animation
	loadingAnimation *tuicomp.Loader

	// Cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// Compaction cancellation
	compactCancel atomic.Pointer[context.CancelFunc]

	// Event subscription
	unsubscribe func()

	// Extproc extension setup (optional)
	extSetup *extension.SetupResult

	// beforeExtensionReload runs immediately before extSetup.Reload during /reload.
	// Used by callers to refresh extension-specific configuration (like allowlists)
	// from settings that were just reloaded by session.Reload().
	beforeExtensionReload func() error

	// updateCh receives a single update notice string (or "") when the
	// background version check completes. Shown in the TUI at startup.
	updateCh <-chan string

	// clipboardReader reads an image from the system clipboard.
	// Defaults to clipboard.ReadClipboardImage; can be replaced in tests.
	clipboardReader func() *clipboard.ClipboardImage
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
	asession *session.AgentSession,
	keybindings *tui.KeybindingsManager,
	settings *config.SettingsManager,
	opts InteractiveModeOptions,
) *InteractiveMode {
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize theme
	_ = itheme.InitTheme(opts.ThemeName, opts.ThemeSearchDirs)

	cwd, _ := os.Getwd()

	autoCompactMode := "client"
	if settings != nil {
		if !settings.GetCompactionEnabled() {
			autoCompactMode = "off"
		}
		if sc := settings.GetServerCompaction(); sc != nil && sc.Enabled != nil && *sc.Enabled {
			autoCompactMode = "server"
		}
	}

	m := &InteractiveMode{
		session:            asession,
		keybindings:        keybindings,
		settings:           settings,
		autoCompactMode:    autoCompactMode,
		footerDataProvider: NewFooterDataProvider(cwd),
		ctx:                ctx,
		cancel:             cancel,
		themeSearchDirs:    opts.ThemeSearchDirs,
		clipboardReader:    clipboard.ReadClipboardImage,
	}

	m.markdownTheme = itheme.GetMarkdownTheme()

	return m
}

// SetExtensionSetup stores the extension setup so that /reload can
// restart external process extensions.
func (m *InteractiveMode) SetExtensionSetup(setup *extension.SetupResult) {
	m.extSetup = setup
	if setup != nil && setup.Manager != nil {
		// Wire UI callbacks so extensions can set footer status and
		// show notifications.
		setup.Manager.SetNotifyFn(func(level, message string) {
			switch level {
			case "error", "warning":
				m.showWarning(message)
			default:
				m.showMessage(message)
			}
		})
		setup.Manager.SetSetStatusFn(func(name, status string) {
			if m.footerDataProvider != nil {
				m.footerDataProvider.SetExtensionStatus(name, status)
				if m.ui != nil {
					m.ui.RequestRender(false)
				}
			}
		})
	}
}

// SetBeforeExtensionReload installs a hook executed during /reload after
// session.Reload() and before extension manager reload.
func (m *InteractiveMode) SetBeforeExtensionReload(fn func() error) {
	m.beforeExtensionReload = fn
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
	firlog.Debug("interactive: initializing TUI")
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

	// Create plan container (shows plan entries above the editor)
	m.planContainer = &tui.Container{}
	m.planHidden = true // start collapsed; Ctrl+R or /plan to expand
	m.ui.AddChild(m.planContainer)

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

	// Pre-read the reexec sidecar so that extension data is available to
	// EmitSessionStart (called by the caller after Init returns).  The full
	// sidecar restore (queued messages, editor text) still happens inside Run().
	m.preloadReexecSidecar()

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
func formatDiagnostics(t *itheme.Theme, header string, diags []resources.ResourceDiagnostic) string {
	var lines []string
	lines = append(lines, t.Fg("warning", "["+header+"]"))

	// Group collision diagnostics by name
	type collisionGroup struct {
		name   string
		winner string
		losers []string
	}
	groups := make(map[string]*collisionGroup)
	var otherDiags []resources.ResourceDiagnostic

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

	// Restore sidecar state from a previous reexec (queued messages, editor text).
	m.restoreReexecSidecar()

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

		// Clear any lingering command status (e.g. from /queue, /session, etc.)
		if m.commandStatusContainer != nil {
			m.commandStatusContainer.Clear()
			if m.ui != nil {
				m.ui.RequestRender(false)
			}
		}

		// Handle slash commands — known builtins are dispatched locally;
		// everything else (skill commands, prompt templates) falls through
		// to session.Prompt which expands them.
		if strings.HasPrefix(text, "/") && m.isBuiltinSlashCommand(text) {
			m.editor.SetText("")
			m.handleSlashCommand(text)
			return
		}

		// Handle extension slash commands — dispatched to the owning extension.
		if strings.HasPrefix(text, "/") && m.isExtensionSlashCommand(text) {
			m.editor.AddToHistory(text)
			m.editor.SetText("")
			go m.handleExtensionSlashCommand(text)
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
			now := time.Now()
			if now.Sub(m.lastEscapeTime) < 500*time.Millisecond {
				m.showTreeSelector()
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
	m.editor.OnAction(tui.ActionSelectModel, func() {
		m.showModelSelector("")
	})
	m.editor.OnAction(tui.ActionSelectThinking, func() {
		m.showThinkingSelector()
	})
	m.editor.OnAction(tui.ActionExpandTools, func() {
		m.toggleToolOutputExpansion()
	})
	m.editor.OnAction(tui.ActionToggleThinking, func() {
		m.toggleThinkingBlockVisibility()
	})
	m.editor.OnAction(tui.ActionCycleThinkingLevel, func() {
		m.cycleThinkingLevel()
	})
	m.editor.OnAction(tui.ActionCycleModelForward, func() {
		m.cycleModel("forward")
	})
	m.editor.OnAction(tui.ActionCycleModelBackward, func() {
		m.cycleModel("backward")
	})
	m.editor.OnAction(tui.ActionNewSession, func() {
		go m.handleClearCommand("")
	})
	m.editor.OnAction(tui.ActionTree, func() {
		m.showTreeSelector()
	})
	m.editor.OnAction(tui.ActionResume, func() {
		m.showSessionSelector()
	})
	m.editor.OnAction(tui.ActionTogglePlan, func() {
		m.togglePlanVisibility()
	})
	m.editor.OnAction(tui.ActionClear, func() {
		m.handleCtrlC()
	})
	m.editor.OnAction(tui.ActionSuspend, func() {
		m.handleCtrlZ()
	})
	m.editor.OnAction(tui.ActionExternalEditor, func() {
		m.handleExternalEditor()
	})

	// Clipboard image paste (Ctrl+V inserts the image path into editor)
	m.editor.OnPasteImage = func() {
		go m.handleClipboardImagePaste()
	}
	m.editor.OnAction(tui.ActionFollowUp, func() {
		text := strings.TrimSpace(m.editor.GetText())
		if text == "" {
			return
		}
		m.editor.AddToHistory(text)
		m.editor.SetText("")
		if m.session != nil {
			go func() {
				_ = m.session.Prompt(text, &session.PromptOptions{StreamingBehavior: "followUp"})
			}()
		}
	})
	m.editor.OnAction(tui.ActionDequeue, func() {
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
	reexecBin := "(current binary)"
	if bin, err := os.Executable(); err == nil {
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(bin, home+"/") {
			bin = "~/" + bin[len(home)+1:]
		}
		reexecBin = bin
	}
	for _, cmd := range resources.BuiltinSlashCommands {
		desc := cmd.Description
		if cmd.Name == "reexec" {
			desc = fmt.Sprintf("Re-exec into %s (or a specified binary), preserving the session", reexecBin)
		}
		commands = append(commands, SlashCommand{
			Name:        cmd.Name,
			Description: desc,
		})
	}

	// Add skill commands and prompt templates from the resource loader
	if m.session != nil {
		rl := m.session.ResourceLoader()
		if rl != nil {
			if m.settings == nil || m.settings.GetEnableSkillCommands() {
				if skills, _ := rl.GetSkills(); len(skills) > 0 {
					for _, skill := range skills {
						commands = append(commands, SlashCommand{
							Name:        "skill:" + skill.Name,
							Description: skill.Description,
						})
					}
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
	if m.extSetup != nil && m.extSetup.Manager != nil {
		for _, ec := range m.extSetup.Manager.GetCommands() {
			commands = append(commands, SlashCommand{
				Name:        ec.Spec.Name,
				Description: ec.Spec.Description,
			})
		}
	}

	basePath, _ := os.Getwd()
	provider := NewCombinedAutocompleteProvider(commands, basePath)
	m.editor.SetAutocompleteProvider(provider)
}
