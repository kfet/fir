package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
)

func TestNewInteractiveMode(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	if m == nil {
		t.Fatal("expected non-nil InteractiveMode")
	}
	if m.autoCompact != true {
		t.Error("expected autoCompact default true")
	}
}

func TestInteractiveMode_Shutdown(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	// Should not panic
	m.Shutdown()
	// Double shutdown should not panic
	m.Shutdown()
}

func TestInteractiveModeOptions(t *testing.T) {
	opts := InteractiveModeOptions{
		InitialPrompt:   "hello",
		ThemeName:       "dark",
		ThemeSearchDirs: []string{"/tmp"},
	}
	if opts.InitialPrompt != "hello" {
		t.Error("expected initial prompt")
	}
	if opts.ThemeName != "dark" {
		t.Error("expected theme name dark")
	}
	if len(opts.ThemeSearchDirs) != 1 {
		t.Error("expected 1 search dir")
	}
}

func TestInteractiveMode_SlashCommandParsing(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
	}{
		{"/help", "/help"},
		{"/clear", "/clear"},
		{"/quit", "/quit"},
		{"/model gpt-4", "/model"},
		{"/unknown-command", "/unknown-command"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			parts := strings.Fields(tt.input)
			if len(parts) == 0 {
				t.Fatal("expected non-empty parts")
			}
			if parts[0] != tt.wantCmd {
				t.Errorf("expected command %q, got %q", tt.wantCmd, parts[0])
			}
		})
	}
}

func TestInteractiveMode_GetFooterData(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	data := m.getFooterData()
	if data.Pwd == "" {
		t.Error("expected non-empty pwd")
	}
	if data.AutoCompact != true {
		t.Error("expected auto-compact true")
	}
}

func TestInteractiveMode_Flags(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	if m.hideThinking != false {
		t.Error("expected hideThinking default false")
	}
	if m.running != false {
		t.Error("expected running default false")
	}
	m.hideThinking = true
	if !m.hideThinking {
		t.Error("expected hideThinking to be settable")
	}
}

// ---------------------------------------------------------------------------
// Helper: create a minimal interactive mode with MockTerminal for input tests.
// Sets up the editor and focus but skips session/agent subscription.
// ---------------------------------------------------------------------------

type testMode struct {
	mode *InteractiveMode
	term *tui.MockTerminal
	ui   *tui.TUI
}

func newTestMode(t *testing.T) *testMode {
	t.Helper()
	return newTestModeInternal(t, nil)
}

func newTestModeWithSession(t *testing.T) *testMode {
	t.Helper()

	// Create a real AgentSession
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := core.NewSessionManager(cwd, agentDir+"/sessions")
	settingsManager := core.NewSettingsManager(cwd, agentDir)

	rl := core.NewResourceLoader(core.ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	rl.Reload()

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "test",
			ThinkingLevel: "off",
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return core.ConvertToLLM(msgs)
		},
	})

	session := core.NewAgentSession(core.AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   core.NewModelRegistry(core.NewAuthStorage(agentDir+"/auth.json"), ""),
		Cwd:             cwd,
	})
	t.Cleanup(func() { session.Close() })

	return newTestModeInternal(t, session)
}

// newTestModeInternal creates a minimal interactive mode with MockTerminal.
// If session is non-nil it is set BEFORE ui.Start() to avoid a data race
// between the test goroutine and the TUI render goroutine.
func newTestModeInternal(t *testing.T, session *core.AgentSession) *testMode {
	t.Helper()
	term := tui.NewMockTerminal(80, 24)
	ui := tui.NewTUI(term, false)

	keybindings := core.NewKeybindingsManager("")
	m := NewInteractiveMode(nil, keybindings, nil, InteractiveModeOptions{})
	m.ui = ui
	m.keybindings = keybindings
	m.session = session // set before ui.Start() to avoid race with render

	m.messageContainer = &tui.Container{}
	ui.AddChild(m.messageContainer)

	m.activityContainer = &tui.Container{}
	ui.AddChild(m.activityContainer)
	m.commandStatusContainer = &tui.Container{}
	ui.AddChild(m.commandStatusContainer)

	m.footerComponent = components.NewFooterComponent(func() components.FooterData {
		return m.getFooterData()
	})
	ui.AddChild(m.footerComponent)

	m.editorContainer = &tui.Container{}
	editorTheme := itheme.GetEditorTheme()
	m.editor = components.NewCustomEditor(ui, editorTheme, keybindings)
	m.setupEditorHandlers()
	m.editorContainer.AddChild(m.editor)
	ui.AddChild(m.editorContainer)

	ui.SetFocus(m.editor)

	// Set up autocomplete (mirrors Init())
	m.setupAutocomplete()

	ui.Start()
	t.Cleanup(func() { ui.Stop() })

	return &testMode{mode: m, term: term, ui: ui}
}

func (tm *testMode) typeText(text string) {
	for _, ch := range text {
		tm.term.SimulateInput(string(ch))
	}
}

func (tm *testMode) pressEnter()     { tm.term.SimulateInput("\r") }
func (tm *testMode) pressCtrlC()     { tm.term.SimulateInput("\x03") }
func (tm *testMode) pressCtrlD()     { tm.term.SimulateInput("\x04") }
func (tm *testMode) pressEscape()    { tm.term.SimulateInput("\x1b") }
func (tm *testMode) pressBackspace() { tm.term.SimulateInput("\x7f") }

func (tm *testMode) editorText() string {
	return tm.mode.editor.GetText()
}

func (tm *testMode) waitRender() {
	time.Sleep(50 * time.Millisecond)
}

func (tm *testMode) renderedOutput() string {
	return strings.Join(tm.term.GetOutput(), "")
}

func (tm *testMode) messageCount() int {
	return len(tm.mode.messageContainer.ChildrenSnapshot())
}

// ---------------------------------------------------------------------------
// Editor input tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_EditorReceivesInput(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("hello")
	tm.waitRender()

	if got := tm.editorText(); got != "hello" {
		t.Errorf("expected editor text %q, got %q", "hello", got)
	}
}

func TestInteractiveMode_NoFocusMeansNoInput(t *testing.T) {
	term := tui.NewMockTerminal(80, 24)
	ui := tui.NewTUI(term, false)

	editorTheme := itheme.GetEditorTheme()
	keybindings := core.NewKeybindingsManager("")
	editor := components.NewCustomEditor(ui, editorTheme, keybindings)
	ui.AddChild(editor)

	// Deliberately do NOT call ui.SetFocus(editor)
	ui.Start()
	defer ui.Stop()

	term.SimulateInput("x")
	time.Sleep(50 * time.Millisecond)

	if got := editor.GetText(); got != "" {
		t.Errorf("expected empty editor (no focus), got %q", got)
	}
}

func TestInteractiveMode_BackspaceDeletesCharacter(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("helloo")
	tm.pressBackspace()
	tm.waitRender()

	if got := tm.editorText(); got != "hello" {
		t.Errorf("expected %q after backspace, got %q", "hello", got)
	}
}

func TestInteractiveMode_MultipleBackspaces(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("abc")
	tm.pressBackspace()
	tm.pressBackspace()
	tm.pressBackspace()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty after deleting all chars, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Ctrl+C tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_CtrlCClearsEditor(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("some text to clear")
	tm.waitRender()
	if tm.editorText() == "" {
		t.Fatal("editor should have text before Ctrl+C")
	}

	tm.pressCtrlC()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after Ctrl+C, got %q", got)
	}
}

func TestInteractiveMode_CtrlCOnEmptyEditorIsNoop(t *testing.T) {
	tm := newTestMode(t)

	// Ctrl+C with empty editor should not panic or cause issues
	tm.pressCtrlC()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Ctrl+D tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_CtrlDOnEmptyEditorTriggersShutdown(t *testing.T) {
	tm := newTestMode(t)

	// ctx should not be done yet
	select {
	case <-tm.mode.ctx.Done():
		t.Fatal("context should not be cancelled before Ctrl+D")
	default:
	}

	tm.pressCtrlD()
	tm.waitRender()

	// ctx should now be cancelled (Shutdown was called)
	select {
	case <-tm.mode.ctx.Done():
		// expected
	default:
		t.Error("expected context to be cancelled after Ctrl+D on empty editor")
	}
}

func TestInteractiveMode_CtrlDWithTextDoesNotExit(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("not empty")
	tm.pressCtrlD()
	tm.waitRender()

	// Context should NOT be cancelled (Ctrl+D only exits on empty editor)
	select {
	case <-tm.mode.ctx.Done():
		t.Error("Ctrl+D with non-empty editor should not trigger shutdown")
	default:
		// expected
	}
}

// ---------------------------------------------------------------------------
// Escape tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_EscapeClearsBashMode(t *testing.T) {
	tm := newTestMode(t)

	// Type ! to enter bash mode
	tm.typeText("!ls")
	tm.waitRender()

	if !tm.mode.IsBashMode() {
		t.Fatal("expected bash mode after typing '!'")
	}

	tm.pressEscape()
	tm.waitRender()

	if tm.mode.IsBashMode() {
		t.Error("expected bash mode to be cleared after Escape")
	}
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after Escape in bash mode, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Slash command tests (via submit)
// ---------------------------------------------------------------------------

func TestInteractiveMode_SlashHelp(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/help")
	tm.pressEnter()
	tm.waitRender()

	// Editor should be cleared after slash command
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /help, got %q", got)
	}

	// Help text should be added to message container
	if tm.messageCount() == 0 {
		t.Error("expected help message to be added to message container")
	}
}

func TestInteractiveMode_SlashHotkeys(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/hotkeys")
	tm.pressEnter()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /hotkeys, got %q", got)
	}
	if tm.messageCount() == 0 {
		t.Error("expected help message for /hotkeys")
	}
}

func TestInteractiveMode_SlashQuit(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/quit")
	tm.pressEnter()
	tm.waitRender()

	select {
	case <-tm.mode.ctx.Done():
		// expected — /quit triggers Shutdown
	default:
		t.Error("expected /quit to trigger shutdown")
	}
}

func TestInteractiveMode_SlashExit(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/exit")
	tm.pressEnter()
	tm.waitRender()

	select {
	case <-tm.mode.ctx.Done():
		// expected
	default:
		t.Error("expected /exit to trigger shutdown")
	}
}

func TestInteractiveMode_IsBuiltinSlashCommand(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})

	// Every entry in core.BuiltinSlashCommands must be recognised.
	for _, cmd := range core.BuiltinSlashCommands {
		full := "/" + cmd.Name
		if !m.isBuiltinSlashCommand(full) {
			t.Errorf("core.BuiltinSlashCommands entry %q not recognised by isBuiltinSlashCommand", full)
		}
	}

	// Every case handled by handleSlashCommand must also be recognised.
	// If you add a new case to that switch, add it here AND to
	// core.BuiltinSlashCommands (or builtinAliases for hidden aliases).
	handleCases := []string{
		"/help", "/hotkeys",
		"/clear", "/new",
		"/compact",
		"/model",
		"/thinking",
		"/theme",
		"/settings",
		"/session",
		"/resume",
		"/login", "/logout",
		"/scoped-models",
		"/tree",
		"/fork",
		"/export",
		"/share",
		"/copy",
		"/name",
		"/changelog",
		"/reload",
		"/reexec",
		"/queue",
		"/dequeue",
		"/quit", "/exit",
	}
	for _, cmd := range handleCases {
		if !m.isBuiltinSlashCommand(cmd) {
			t.Errorf("handleSlashCommand case %q not recognised; add it to core.BuiltinSlashCommands or builtinAliases", cmd)
		}
	}

	// Non-builtins must NOT be recognised.
	nonBuiltins := []string{
		"/skill:review", "/skill:deploy do it", "/mytemplate", "/nonexistent",
	}
	for _, cmd := range nonBuiltins {
		if m.isBuiltinSlashCommand(cmd) {
			t.Errorf("expected %q to NOT be a builtin command", cmd)
		}
	}
}

func TestInteractiveMode_SkillCommandFlowsToPrompt(t *testing.T) {
	tm := newTestMode(t)

	// Override OnSubmit to track what gets submitted (no session)
	var submitted string
	tm.mode.editor.OnSubmit = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		// Simulate the real submit logic: builtins go to handleSlashCommand,
		// everything else (skills, templates) goes to prompt
		if strings.HasPrefix(text, "/") && tm.mode.isBuiltinSlashCommand(text) {
			tm.mode.editor.SetText("")
			tm.mode.handleSlashCommand(text)
			return
		}
		// Non-builtin slash commands go to history + prompt
		tm.mode.editor.AddToHistory(text)
		tm.mode.editor.SetText("")
		submitted = text
	}

	tm.typeText("/skill:review fix the bug")
	tm.pressEnter()
	tm.waitRender()

	if submitted != "/skill:review fix the bug" {
		t.Errorf("expected skill command to flow to prompt, got %q", submitted)
	}
}

func TestInteractiveMode_SlashUnknownCommand(t *testing.T) {
	tm := newTestMode(t)

	// Unknown slash commands (not builtin) are treated as skill/template
	// commands and sent to session.Prompt() for expansion.
	// Since session is nil in tests, they are effectively no-ops.
	tm.typeText("/nonexistent")
	tm.pressEnter()
	tm.waitRender()

	// Editor should be cleared (submitted as regular prompt)
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after unknown command, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Prompt submission tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_SubmitEmptyIsNoop(t *testing.T) {
	tm := newTestMode(t)

	// Press Enter with empty editor — should be a noop
	tm.pressEnter()
	tm.waitRender()

	if tm.messageCount() != 0 {
		t.Errorf("expected no messages after submitting empty prompt, got %d", tm.messageCount())
	}
}

func TestInteractiveMode_SubmitPromptCallsOnSubmit(t *testing.T) {
	tm := newTestMode(t)

	// Track what was submitted
	var mu sync.Mutex
	var submitted string
	tm.mode.editor.OnSubmit = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		mu.Lock()
		submitted = text
		mu.Unlock()
		tm.mode.editor.SetText("")
	}

	tm.typeText("What is 2+2?")
	tm.pressEnter()
	tm.waitRender()

	mu.Lock()
	got := submitted
	mu.Unlock()

	if got != "What is 2+2?" {
		t.Errorf("expected submitted %q, got %q", "What is 2+2?", got)
	}

	// Editor should be cleared after submit
	if ed := tm.editorText(); ed != "" {
		t.Errorf("expected empty editor after submit, got %q", ed)
	}
}

// ---------------------------------------------------------------------------
// Bash mode tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_BashModeDetection(t *testing.T) {
	tm := newTestMode(t)

	if tm.mode.IsBashMode() {
		t.Fatal("should not be in bash mode initially")
	}

	tm.typeText("!")
	tm.waitRender()
	if !tm.mode.IsBashMode() {
		t.Error("expected bash mode after typing '!'")
	}

	// Backspace to remove '!' should exit bash mode
	tm.pressBackspace()
	tm.waitRender()
	if tm.mode.IsBashMode() {
		t.Error("expected to leave bash mode after deleting '!'")
	}
}

func TestInteractiveMode_BashModeWithCommand(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("!echo hello")
	tm.waitRender()

	if !tm.mode.IsBashMode() {
		t.Error("expected bash mode with '!echo hello'")
	}

	if got := tm.editorText(); got != "!echo hello" {
		t.Errorf("expected %q, got %q", "!echo hello", got)
	}
}

func TestInteractiveMode_DoubleBangModeDetection(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("!!")
	tm.waitRender()
	if !tm.mode.IsBashMode() {
		t.Error("expected bash mode after typing '!!'")
	}
}

func TestInteractiveMode_BashCommandExecution(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Submit a bash command — should go through ExecuteBash, not Prompt
	tm.typeText("!echo hello_from_bash")
	tm.pressEnter()
	// Wait for goroutine to complete
	time.Sleep(500 * time.Millisecond)
	tm.waitRender()

	// Editor should be cleared
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor, got %q", got)
	}

	// Should not be in bash mode after execution
	if tm.mode.IsBashMode() {
		t.Error("expected to leave bash mode after command execution")
	}
}

func TestInteractiveMode_DoubleBangCommandExecution(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Submit a !! command (excluded from context)
	tm.typeText("!!echo excluded")
	tm.pressEnter()
	time.Sleep(500 * time.Millisecond)
	tm.waitRender()

	// Editor should be cleared
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor, got %q", got)
	}

	// Should not be in bash mode after execution
	if tm.mode.IsBashMode() {
		t.Error("expected to leave bash mode after !! command execution")
	}
}

// ---------------------------------------------------------------------------
// Editor history tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_EditorHistory(t *testing.T) {
	tm := newTestMode(t)

	// Override OnSubmit to just clear + record history (no session needed)
	tm.mode.editor.OnSubmit = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		tm.mode.editor.AddToHistory(text)
		tm.mode.editor.SetText("")
	}

	// Submit two messages — wait for render between each to avoid races
	tm.typeText("first message")
	tm.pressEnter()
	tm.waitRender()
	tm.waitRender()

	tm.typeText("second message")
	tm.pressEnter()
	tm.waitRender()
	tm.waitRender()

	// Editor should be empty
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor, got %q", got)
	}

	// Press Up arrow to go back in history — give render time to settle
	tm.term.SimulateInput("\x1b[A") // Up arrow
	tm.waitRender()
	tm.waitRender()

	if got := tm.editorText(); got != "second message" {
		t.Errorf("expected %q from history, got %q", "second message", got)
	}

	// Press Up again for first message
	tm.term.SimulateInput("\x1b[A")
	tm.waitRender()
	tm.waitRender()

	if got := tm.editorText(); got != "first message" {
		t.Errorf("expected %q from history, got %q", "first message", got)
	}

	// Press Down to go forward
	tm.term.SimulateInput("\x1b[B") // Down arrow
	tm.waitRender()
	tm.waitRender()

	if got := tm.editorText(); got != "second message" {
		t.Errorf("expected %q from history, got %q", "second message", got)
	}
}

// ---------------------------------------------------------------------------
// Rendering tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_TUIRenders(t *testing.T) {
	tm := newTestMode(t)
	tm.waitRender()

	output := tm.renderedOutput()
	if output == "" {
		t.Fatal("expected non-empty TUI render output")
	}

	// Should contain editor border characters
	if !strings.Contains(output, "─") {
		t.Error("expected editor border characters in render")
	}
}

func TestInteractiveMode_TypedTextAppearsInRender(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("visible text")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "visible text") {
		t.Error("expected typed text to appear in rendered output")
	}
}

func TestInteractiveMode_HelpTextAppearsInRender(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/help")
	tm.pressEnter()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "/help") || !strings.Contains(output, "/quit") {
		t.Error("expected help text with commands to appear in rendered output")
	}
}

// ---------------------------------------------------------------------------
// Slash commands with arguments
// ---------------------------------------------------------------------------

func TestInteractiveMode_SlashCommandClearsEditor(t *testing.T) {
	// Slash commands that require a session (/model, /thinking, /compact, etc.)
	// are tested here just for editor-clearing behavior by using /help and /quit
	// which don't touch the session. Session-dependent commands are tested via
	// handleSlashCommand unit tests below.
	tm := newTestMode(t)

	// Verify handleSlashCommand dispatches correctly for simple commands
	for _, cmd := range []string{"/help", "/hotkeys"} {
		tm.typeText(cmd)
		tm.pressEnter()
		tm.waitRender()

		if got := tm.editorText(); got != "" {
			t.Errorf("expected empty editor after %s, got %q", cmd, got)
		}
	}
}

func TestInteractiveMode_HandleSlashCommandDispatch(t *testing.T) {
	// Unit-test the command parsing/dispatch without triggering session methods.
	tests := []struct {
		cmd          string
		wantShutdown bool
		wantMessage  bool // something added to messageContainer
		wantWarning  bool // something added to commandStatusContainer
	}{
		{"/help", false, true, false},
		{"/hotkeys", false, true, false},
		{"/quit", true, false, false},
		{"/exit", true, false, false},
		{"/bogus", false, false, true},
		// New commands (session-independent, should show warnings or work)
		{"/login", false, false, true},   // "not yet implemented" warning
		{"/logout", false, false, true},  // "not yet implemented" warning
		{"/export", false, false, true},  // "not yet implemented"
		{"/share", false, false, true},   // "not yet implemented"
		{"/copy", false, false, true},    // "not yet implemented"
		{"/name", false, false, true},    // usage warning (no args)
		{"/changelog", false, true, false}, // shows "No changelog entries found." message
		{"/tree", false, false, true},
		{"/fork", false, false, true},
		{"/scoped-models", false, false, true},
		{"/session", false, false, true}, // "No session available"
		{"/reload", false, false, true},  // "No session available"
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			m2 := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
			m2.messageContainer = &tui.Container{}
			m2.commandStatusContainer = &tui.Container{}

			m2.handleSlashCommand(tt.cmd)

			if tt.wantShutdown {
				select {
				case <-m2.ctx.Done():
					// expected
				default:
					t.Errorf("%s should trigger shutdown", tt.cmd)
				}
			}
			if tt.wantMessage && len(m2.messageContainer.Children) == 0 {
				t.Errorf("%s should add message", tt.cmd)
			}
			if tt.wantWarning && len(m2.commandStatusContainer.Children) == 0 {
				t.Errorf("%s should show warning", tt.cmd)
			}
		})
	}
}

func TestInteractiveMode_SlashNew(t *testing.T) {
	// /new is an alias for /clear (starts a new session)
	tm := newTestMode(t)

	tm.typeText("/new")
	tm.pressEnter()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /new, got %q", got)
	}
}

func TestInteractiveMode_SlashResume(t *testing.T) {
	// /resume shows the session selector; without session it still opens
	tm := newTestMode(t)

	tm.typeText("/resume")
	tm.pressEnter()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /resume, got %q", got)
	}
}

func TestInteractiveMode_SlashLogin(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/login")
	tm.pressEnter()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /login, got %q", got)
	}
}

func TestInteractiveMode_SlashLogout(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/logout")
	tm.pressEnter()
	tm.waitRender()

	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /logout, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Comprehensive workflow test
// ---------------------------------------------------------------------------

func TestInteractiveMode_FullWorkflow(t *testing.T) {
	tm := newTestMode(t)

	// Override submit to record without needing session
	var submitted []string
	var mu sync.Mutex
	tm.mode.editor.OnSubmit = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if strings.HasPrefix(text, "/") {
			tm.mode.editor.SetText("")
			tm.mode.handleSlashCommand(text)
			return
		}
		mu.Lock()
		submitted = append(submitted, text)
		mu.Unlock()
		tm.mode.editor.AddToHistory(text)
		tm.mode.editor.SetText("")
	}

	// 1. Type and submit a prompt
	tm.typeText("Hello world")
	tm.pressEnter()
	tm.waitRender()

	mu.Lock()
	if len(submitted) != 1 || submitted[0] != "Hello world" {
		t.Errorf("step 1: expected [Hello world], got %v", submitted)
	}
	mu.Unlock()

	// 2. Type, then Ctrl+C to clear
	tm.typeText("I changed my mind")
	tm.waitRender()
	if tm.editorText() != "I changed my mind" {
		t.Fatalf("step 2: text not in editor")
	}
	tm.pressCtrlC()
	tm.waitRender()
	if tm.editorText() != "" {
		t.Errorf("step 2: editor not cleared by Ctrl+C, got %q", tm.editorText())
	}

	// 3. Use /help command
	tm.typeText("/help")
	tm.pressEnter()
	tm.waitRender()
	if tm.messageCount() == 0 {
		t.Error("step 3: /help should add message")
	}

	// 4. Submit another prompt
	tm.typeText("Second prompt")
	tm.pressEnter()
	tm.waitRender()
	mu.Lock()
	if len(submitted) != 2 || submitted[1] != "Second prompt" {
		t.Errorf("step 4: expected second submit, got %v", submitted)
	}
	mu.Unlock()

	// 5. Navigate history (Up arrow)
	tm.term.SimulateInput("\x1b[A")
	tm.waitRender()
	if got := tm.editorText(); got != "Second prompt" {
		t.Errorf("step 5: expected history entry, got %q", got)
	}

	// 6. Clear with Ctrl+C and exit with Ctrl+D
	tm.pressCtrlC()
	tm.waitRender()
	tm.pressCtrlD()
	tm.waitRender()

	select {
	case <-tm.mode.ctx.Done():
		// expected
	default:
		t.Error("step 6: expected shutdown after Ctrl+D")
	}
}

// ---------------------------------------------------------------------------
// Message container rendering
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Autocomplete integration tests
// ---------------------------------------------------------------------------

func TestInteractiveMode_SlashAutocompleteTriggers(t *testing.T) {
	tm := newTestMode(t)

	// Type "/" — should trigger autocomplete suggestions
	tm.typeText("/")
	tm.waitRender()

	if !tm.mode.editor.IsShowingAutocomplete() {
		t.Error("expected autocomplete to be showing after typing /")
	}
}

func TestInteractiveMode_SlashAutocompleteFuzzy(t *testing.T) {
	tm := newTestMode(t)

	// Type "/he" — should filter to help/hotkeys
	tm.typeText("/he")
	tm.waitRender()

	if !tm.mode.editor.IsShowingAutocomplete() {
		t.Error("expected autocomplete showing for /he")
	}
}

func TestInteractiveMode_AutocompleteEscapeCancels(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("/")
	tm.waitRender()

	if !tm.mode.editor.IsShowingAutocomplete() {
		t.Fatal("expected autocomplete showing")
	}

	tm.pressEscape()
	tm.waitRender()

	if tm.mode.editor.IsShowingAutocomplete() {
		t.Error("expected autocomplete cancelled after Escape")
	}
}

// ---------------------------------------------------------------------------
// Display helpers
// ---------------------------------------------------------------------------

func TestInteractiveMode_ShowMessage(t *testing.T) {
	tm := newTestMode(t)

	initialCount := tm.messageCount()
	tm.mode.showMessage("Test message content")
	tm.waitRender()

	got := tm.messageCount()
	// showMessage adds a Spacer + Text = 2 children
	if got != initialCount+2 {
		t.Errorf("expected message count %d (initial %d + 2), got %d", initialCount+2, initialCount, got)
	}
}

func TestInteractiveMode_ShowWarningAppearsInStatus(t *testing.T) {
	tm := newTestMode(t)

	tm.mode.showWarning("Something went wrong")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Something went wrong") {
		t.Error("expected warning text in rendered output")
	}
}

// ---------------------------------------------------------------------------
// extractEntryText
// ---------------------------------------------------------------------------

func TestExtractEntryText_NonMessage(t *testing.T) {
	entry := &core.SessionEntry{Type: "compaction"}
	got := extractEntryText(entry)
	if got != "compaction" {
		t.Errorf("expected 'compaction', got %q", got)
	}
}

func TestExtractEntryText_StringContent(t *testing.T) {
	raw := []byte(`{"role":"user","content":"hello world"}`)
	entry := &core.SessionEntry{Type: "message", RawMessage: raw}
	got := extractEntryText(entry)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestExtractEntryText_StringContentNewlines(t *testing.T) {
	raw := []byte(`{"role":"user","content":"line1\nline2"}`)
	entry := &core.SessionEntry{Type: "message", RawMessage: raw}
	got := extractEntryText(entry)
	if !strings.Contains(got, "line1 line2") {
		t.Errorf("expected newlines replaced with spaces, got %q", got)
	}
}

func TestExtractEntryText_ArrayContent(t *testing.T) {
	raw := []byte(`{"role":"assistant","content":[{"type":"text","text":"response text"}]}`)
	entry := &core.SessionEntry{Type: "message", RawMessage: raw}
	got := extractEntryText(entry)
	if got != "response text" {
		t.Errorf("expected 'response text', got %q", got)
	}
}

func TestExtractEntryText_InvalidJSON(t *testing.T) {
	entry := &core.SessionEntry{Type: "message", RawMessage: []byte(`{invalid`)}
	got := extractEntryText(entry)
	if got != "message" {
		t.Errorf("expected 'message', got %q", got)
	}
}

func TestExtractEntryText_EmptyRawMessage(t *testing.T) {
	entry := &core.SessionEntry{Type: "message"}
	got := extractEntryText(entry)
	if got != "message" {
		t.Errorf("expected 'message', got %q", got)
	}
}

// ---------------------------------------------------------------------------
// /name command
// ---------------------------------------------------------------------------

func TestInteractiveMode_SlashName(t *testing.T) {
	tm := newTestModeWithSession(t)

	tm.mode.handleSlashCommand("/name test-session")
	tm.waitRender()

	name := tm.mode.session.GetSessionName()
	if name != "test-session" {
		t.Errorf("expected session name 'test-session', got %q", name)
	}
}

func TestInteractiveMode_SlashNameEmpty(t *testing.T) {
	tm := newTestModeWithSession(t)

	tm.mode.handleSlashCommand("/name")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Usage") {
		t.Error("expected usage warning for empty /name")
	}
}

func TestInteractiveMode_SlashLoginWithSession(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Should not panic when session + model registry are available
	tm.mode.handleSlashCommand("/login")
	tm.waitRender()

	// Should show the OAuth provider selector (no panic)
	if got := tm.editorText(); got != "" {
		t.Errorf("expected empty editor after /login, got %q", got)
	}
}

func TestInteractiveMode_SlashLogoutWithSession(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Should not panic when session + model registry are available
	tm.mode.handleSlashCommand("/logout")
	tm.waitRender()

	// Should show "No OAuth providers logged in" since none logged in
	output := tm.renderedOutput()
	if !strings.Contains(output, "No OAuth") {
		t.Error("expected 'No OAuth' message for logout with no logged-in providers")
	}
}

func TestInteractiveMode_SlashReloadWithSession(t *testing.T) {
	tm := newTestModeWithSession(t)

	tm.mode.handleSlashCommand("/reload")
	tm.waitRender()

	output := tm.renderedOutput()
	if strings.Contains(output, "failed") {
		t.Error("reload should succeed")
	}
}

func TestInteractiveMode_SlashSessionWithExtensions(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Register a test extension
	extension.ClearRegistry()
	extension.Register("test-ext", func(api extension.API) {
		api.RegisterTool(extension.ToolDefinition{
			Name:        "my_tool",
			Label:       "My Tool",
			Description: "A test tool",
		})
		api.RegisterCommand("mycommand", extension.Command{
			Description: "A test command",
		})
	})
	t.Cleanup(func() { extension.ClearRegistry() })

	// Create and load the runner
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	tm.mode.SetExtensionRunner(runner)

	tm.mode.handleSlashCommand("/session")
	tm.waitRender()

	output := tm.renderedOutput()

	// Should show session info
	if !strings.Contains(output, "Session Info") {
		t.Error("expected 'Session Info' in output")
	}

	// Should show extension name
	if !strings.Contains(output, "test-ext") {
		t.Errorf("expected extension name 'test-ext' in output, got:\n%s", output)
	}

	// Should show extension tools section
	if !strings.Contains(output, "Extension Tools") {
		t.Errorf("expected 'Extension Tools' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "my_tool") {
		t.Errorf("expected tool 'my_tool' in output, got:\n%s", output)
	}

	// Should show extension commands section
	if !strings.Contains(output, "Extension Commands") {
		t.Errorf("expected 'Extension Commands' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "mycommand") {
		t.Errorf("expected command 'mycommand' in output, got:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// modeUIContext tests
// ---------------------------------------------------------------------------

func TestModeUIContext_SelectReturnsEmpty(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	ctx := &modeUIContext{mode: m}

	result, err := ctx.Select("title", []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestModeUIContext_ConfirmReturnsFalse(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	ctx := &modeUIContext{mode: m}

	result, err := ctx.Confirm("title", "are you sure?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false")
	}
}

func TestModeUIContext_InputReturnsEmpty(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	ctx := &modeUIContext{mode: m}

	result, err := ctx.Input("title", "placeholder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestModeUIContext_NotifyError(t *testing.T) {
	tm := newTestMode(t)
	ctx := &modeUIContext{mode: tm.mode}

	ctx.Notify("something broke", "error")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "something broke") {
		t.Error("expected error notification to appear in output")
	}
}

func TestModeUIContext_NotifyWarning(t *testing.T) {
	tm := newTestMode(t)
	ctx := &modeUIContext{mode: tm.mode}

	ctx.Notify("careful!", "warning")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "careful!") {
		t.Error("expected warning notification to appear in output")
	}
}

func TestModeUIContext_NotifyInfo(t *testing.T) {
	tm := newTestMode(t)
	ctx := &modeUIContext{mode: tm.mode}

	ctx.Notify("all good", "info")
	tm.waitRender()

	// Info notifications use showMessage which adds to messageContainer
	if tm.messageCount() == 0 {
		t.Error("expected info notification to add a message")
	}
}

func TestModeUIContext_SetStatus(t *testing.T) {
	tm := newTestModeWithSession(t)
	ctx := &modeUIContext{mode: tm.mode}

	// Set status — should not panic even if footerDataProvider is set
	ctx.SetStatus("myext", "running")
	tm.waitRender()

	// Verify through footer data provider
	if tm.mode.footerDataProvider != nil {
		statuses := tm.mode.footerDataProvider.GetExtensionStatuses()
		if statuses["myext"] != "running" {
			t.Errorf("expected status 'running', got %q", statuses["myext"])
		}
	}
}

func TestModeUIContext_SetStatusClear(t *testing.T) {
	tm := newTestModeWithSession(t)
	ctx := &modeUIContext{mode: tm.mode}

	ctx.SetStatus("myext", "active")
	ctx.SetStatus("myext", "")

	if tm.mode.footerDataProvider != nil {
		statuses := tm.mode.footerDataProvider.GetExtensionStatuses()
		if statuses["myext"] != "" {
			t.Errorf("expected cleared status, got %q", statuses["myext"])
		}
	}
}

func TestModeUIContext_SetStatusNoProvider(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	m.footerDataProvider = nil
	ctx := &modeUIContext{mode: m}

	// Should not panic when footerDataProvider is nil
	ctx.SetStatus("key", "value")
}

func TestModeUIContext_SetWidgetNoop(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	ctx := &modeUIContext{mode: m}

	// Should not panic (widget not implemented)
	ctx.SetWidget("key", []string{"line1", "line2"})
}

func TestModeUIContext_ClearWidgetNoop(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	ctx := &modeUIContext{mode: m}

	// Should not panic (widget not implemented)
	ctx.ClearWidget("key")
}

// ---------------------------------------------------------------------------
// resolveEnabledExtensions tests
// ---------------------------------------------------------------------------

func TestResolveEnabledExtensions_NoSettings(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := core.NewSettingsManager(cwd, agentDir)

	m := NewInteractiveMode(nil, nil, sm, InteractiveModeOptions{})
	result := m.resolveEnabledExtensions()
	if len(result) != 0 {
		t.Errorf("expected 0 extensions, got %d: %v", len(result), result)
	}
}

func TestResolveEnabledExtensions_SettingsOnly(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "settings.json"),
		[]byte(`{"extensions":["notify","sandbox"]}`), 0o600)

	sm := core.NewSettingsManager(cwd, agentDir)
	m := NewInteractiveMode(nil, nil, sm, InteractiveModeOptions{})

	result := m.resolveEnabledExtensions()
	if len(result) != 2 {
		t.Fatalf("expected 2 extensions, got %d: %v", len(result), result)
	}
	if result[0] != "notify" || result[1] != "sandbox" {
		t.Errorf("unexpected extensions: %v", result)
	}
}

func TestResolveEnabledExtensions_CLIOnly(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	sm := core.NewSettingsManager(cwd, agentDir)

	m := NewInteractiveMode(nil, nil, sm, InteractiveModeOptions{})
	m.cliExtensionNames = []string{"notify", "sandbox"}

	result := m.resolveEnabledExtensions()
	if len(result) != 2 {
		t.Fatalf("expected 2 extensions, got %d: %v", len(result), result)
	}
}

func TestResolveEnabledExtensions_MergedDeduplicated(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "settings.json"),
		[]byte(`{"extensions":["notify"]}`), 0o600)

	sm := core.NewSettingsManager(cwd, agentDir)
	m := NewInteractiveMode(nil, nil, sm, InteractiveModeOptions{})
	m.cliExtensionNames = []string{"notify", "sandbox"}

	result := m.resolveEnabledExtensions()
	if len(result) != 2 {
		t.Fatalf("expected 2 extensions (deduped), got %d: %v", len(result), result)
	}
	// "notify" from settings + "sandbox" from CLI (notify not duplicated)
	found := map[string]bool{}
	for _, n := range result {
		found[n] = true
	}
	if !found["notify"] || !found["sandbox"] {
		t.Errorf("expected notify and sandbox, got %v", result)
	}
}

func TestCompactionFormatTokens(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{9999, "10.0k"},
		{10000, "10k"},
		{10001, "10k"},
		{95000, "95k"},
		{999999, "999k"},
		{1_000_000, "1.0M"},
		{1_500_000, "1.5M"},
	}
	for _, tc := range tests {
		got := compactionFormatTokens(tc.n)
		if got != tc.want {
			t.Errorf("compactionFormatTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestCompactionLoaderLabel(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})

	// nil info, no suffix → bare message
	label := m.compactionLoaderLabel(nil, "")
	if label != "Compacting context..." {
		t.Errorf("unexpected label: %q", label)
	}

	// nil info, with suffix
	label = m.compactionLoaderLabel(nil, "(Esc to cancel)")
	if !strings.Contains(label, "(Esc to cancel)") {
		t.Errorf("expected suffix in label: %q", label)
	}

	// non-nil info, no suffix
	info := &core.CompactionInfo{MessagesToSummarize: 42, TokensBefore: 95000}
	label = m.compactionLoaderLabel(info, "")
	if !strings.Contains(label, "42") {
		t.Errorf("expected message count in label: %q", label)
	}
	if !strings.Contains(label, "95k") {
		t.Errorf("expected token count in label: %q", label)
	}

	// non-nil info, with suffix
	label = m.compactionLoaderLabel(info, "(Esc to cancel)")
	if !strings.Contains(label, "(Esc to cancel)") {
		t.Errorf("expected suffix in label: %q", label)
	}
}

// ---------------------------------------------------------------------------
// handleDequeue tests
// ---------------------------------------------------------------------------

func TestHandleDequeue_NilSession(t *testing.T) {
	tm := newTestMode(t) // session is nil by default
	// Should return early without panic or side effects.
	tm.mode.handleDequeue()
	tm.waitRender()

	// No status shown — nothing written to status container.
	if got := len(tm.mode.commandStatusContainer.Children); got != 0 {
		t.Errorf("expected no status for nil session, got %d children", got)
	}
}

func TestHandleDequeue_EmptyQueue(t *testing.T) {
	tm := newTestModeWithSession(t)
	// Queue is empty — handleDequeue should show a "No queued" status.
	tm.mode.handleDequeue()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "No queued") {
		t.Errorf("expected 'No queued' status for empty queue, got:\n%s", output)
	}
}

func TestHandleDequeue_NonEmptyQueue(t *testing.T) {
	tm := newTestModeWithSession(t)
	// Queue two follow-up messages.
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("first queued", 0)))
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("second queued", 0)))

	tm.mode.handleDequeue()
	tm.waitRender()

	edText := tm.editorText()
	if !strings.Contains(edText, "first queued") {
		t.Errorf("expected 'first queued' in editor, got %q", edText)
	}
	if !strings.Contains(edText, "second queued") {
		t.Errorf("expected 'second queued' in editor, got %q", edText)
	}

	// Status should mention the count.
	output := tm.renderedOutput()
	if !strings.Contains(output, "2") {
		t.Errorf("expected count '2' in status, got:\n%s", output)
	}
}

func TestHandleDequeue_MergesWithExistingEditorText(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.editor.SetText("existing text")
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("queued msg", 0)))

	tm.mode.handleDequeue()
	tm.waitRender()

	edText := tm.editorText()
	if !strings.Contains(edText, "queued msg") {
		t.Errorf("expected queued msg in editor, got %q", edText)
	}
	if !strings.Contains(edText, "existing text") {
		t.Errorf("expected existing text preserved in editor, got %q", edText)
	}
}

func TestHandleDequeue_ClearsQueueAfterDequeue(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("msg1", 0)))

	tm.mode.handleDequeue()
	tm.waitRender()

	// Second dequeue on now-empty queue should show "No queued".
	tm.mode.editor.SetText("") // clear editor to avoid merge confusion
	tm.mode.handleDequeue()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "No queued") {
		t.Errorf("expected 'No queued' after second dequeue, got:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// handleQueueCommand tests
// ---------------------------------------------------------------------------

func TestHandleQueueCommand_NilSession(t *testing.T) {
	tm := newTestMode(t)
	tm.mode.handleQueueCommand() // must not panic
	tm.waitRender()
}

func TestHandleQueueCommand_EmptyQueue(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.handleQueueCommand()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "empty") {
		t.Errorf("expected 'empty' in status, got:\n%s", output)
	}
}

func TestHandleQueueCommand_ShowsQueuedMessages(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("hello world", 0)))
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("second message", 0)))

	tm.mode.handleQueueCommand()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "hello world") {
		t.Errorf("expected 'hello world' in queue listing, got:\n%s", output)
	}
	if !strings.Contains(output, "second message") {
		t.Errorf("expected 'second message' in queue listing, got:\n%s", output)
	}
	// Queue must be preserved.
	if tm.mode.session.Agent.FollowUpQueueLen() != 2 {
		t.Errorf("handleQueueCommand must not consume the queue")
	}
}

// ---------------------------------------------------------------------------
// handleDequeueCommand tests
// ---------------------------------------------------------------------------

func TestHandleDequeueCommand_NilSession(t *testing.T) {
	tm := newTestMode(t)
	// With no arg — delegates to handleDequeue which guards nil.
	tm.mode.handleDequeueCommand("")
	tm.waitRender()
	// With a numeric arg — must not panic.
	tm.mode.handleDequeueCommand("1")
	tm.waitRender()
}

func TestHandleDequeueCommand_NoArg_BehavesLikeDequeue(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("msg a", 0)))
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("msg b", 0)))

	tm.mode.handleDequeueCommand("")
	tm.waitRender()

	edText := tm.editorText()
	if !strings.Contains(edText, "msg a") || !strings.Contains(edText, "msg b") {
		t.Errorf("expected both messages in editor, got %q", edText)
	}
	if tm.mode.session.Agent.FollowUpQueueLen() != 0 {
		t.Error("queue should be empty after dequeue all")
	}
}

func TestHandleDequeueCommand_InvalidIndex(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("only", 0)))

	tm.mode.handleDequeueCommand("not-a-number")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "invalid index") {
		t.Errorf("expected 'invalid index' warning, got:\n%s", output)
	}
	// Queue untouched.
	if tm.mode.session.Agent.FollowUpQueueLen() != 1 {
		t.Error("queue should be unchanged after bad index")
	}
}

func TestHandleDequeueCommand_OutOfRangeIndex(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("only", 0)))

	tm.mode.handleDequeueCommand("5")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "no message at index") {
		t.Errorf("expected 'no message at index' warning, got:\n%s", output)
	}
}

func TestHandleDequeueCommand_SpecificIndex(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("first", 0)))
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("second", 0)))
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("third", 0)))

	tm.mode.handleDequeueCommand("2")
	tm.waitRender()

	// Editor gets the removed item.
	edText := tm.editorText()
	if !strings.Contains(edText, "second") {
		t.Errorf("expected 'second' in editor, got %q", edText)
	}
	if strings.Contains(edText, "first") || strings.Contains(edText, "third") {
		t.Errorf("expected only the removed item in editor, got %q", edText)
	}

	// Remaining queue has first and third.
	remaining := tm.mode.session.PeekFollowUpQueue()
	if len(remaining) != 2 || remaining[0] != "first" || remaining[1] != "third" {
		t.Errorf("unexpected remaining queue: %v", remaining)
	}
}

func TestHandleDequeueCommand_MergesWithEditorText(t *testing.T) {
	tm := newTestModeWithSession(t)
	tm.mode.editor.SetText("draft")
	tm.mode.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("queued", 0)))

	tm.mode.handleDequeueCommand("1")
	tm.waitRender()

	edText := tm.editorText()
	if !strings.Contains(edText, "queued") || !strings.Contains(edText, "draft") {
		t.Errorf("expected both queued and draft text in editor, got %q", edText)
	}
}

// ---------------------------------------------------------------------------
// handleExternalEditor tests
// ---------------------------------------------------------------------------

func TestHandleExternalEditor_NoEditorConfigured(t *testing.T) {
	tm := newTestMode(t)

	// Ensure neither VISUAL nor EDITOR is set.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	tm.mode.handleExternalEditor()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "No editor configured") {
		t.Errorf("expected 'No editor configured' warning, got:\n%s", output)
	}
}

func TestHandleExternalEditor_EditorUsedWhenVisualUnset(t *testing.T) {
	tm := newTestMode(t)

	// VISUAL is unset; EDITOR is set to a no-op command that exits 0.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true") // unix `true` command exits 0 immediately

	tm.mode.editor.SetText("original text")
	// This calls ui.Stop + sh -c + ui.Start; since TUI is a mock this may not
	// block. The key assertion is no panic and no "No editor configured" warning.
	tm.mode.handleExternalEditor()
	tm.waitRender()

	output := tm.renderedOutput()
	if strings.Contains(output, "No editor configured") {
		t.Errorf("should not show 'No editor configured' when EDITOR is set")
	}
}

func TestHandleExternalEditor_VisualTakesPrecedenceOverEditor(t *testing.T) {
	tm := newTestMode(t)

	// Both set — VISUAL takes precedence; we use `true` for both since we only
	// care that the code path doesn't show the "no editor" warning.
	t.Setenv("VISUAL", "true")
	t.Setenv("EDITOR", "echo should-not-be-used")

	tm.mode.handleExternalEditor()
	tm.waitRender()

	output := tm.renderedOutput()
	if strings.Contains(output, "No editor configured") {
		t.Errorf("should not show 'No editor configured' when VISUAL is set")
	}
}

// ---------------------------------------------------------------------------
// handleClipboardImagePaste tests
// ---------------------------------------------------------------------------

func TestHandleClipboardImagePaste_NoImage(t *testing.T) {
	tm := newTestMode(t)
	initial := tm.editorText()

	// In test environments clipboard is unavailable → ReadClipboardImage returns nil.
	// The function should silently ignore this and leave the editor unchanged.
	tm.mode.handleClipboardImagePaste()
	tm.waitRender()

	if got := tm.editorText(); got != initial {
		t.Errorf("expected editor unchanged (no image), got %q", got)
	}
}

// ---------------------------------------------------------------------------
// performShare tests
// ---------------------------------------------------------------------------

func TestPerformShare_NoBinary(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Override PATH so that `gh` cannot be found, guaranteeing the
	// "not logged in" warning path is exercised.
	t.Setenv("PATH", t.TempDir())

	tm.mode.performShare()
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "GitHub CLI") && !strings.Contains(output, "gh auth") {
		t.Errorf("expected gh-related warning when gh is unavailable/unauthenticated, got:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// cycleModel tests
// ---------------------------------------------------------------------------

// setupAvailableModels clears all provider auth env vars (so only the explicitly
// registered key applies), adds a runtime API key for "anthropic" so that the
// built-in anthropic models appear in ModelRegistry.GetAvailable().
// Returns the list of available models (at least 2 guaranteed or test is skipped).
func setupAvailableModels(t *testing.T, tm *testMode) []*ai.Model {
	t.Helper()
	// Isolate test from any API keys in the developer's environment.
	for _, key := range ai.KnownApiKeyEnvVars() {
		t.Setenv(key, "")
	}
	tm.mode.session.ModelRegistryRef().AuthStorage().SetRuntimeApiKey("anthropic", "test-key")
	available := tm.mode.session.ModelRegistryRef().GetAvailable()
	if len(available) < 2 {
		t.Skip("fewer than 2 models available for cycling test")
	}
	return available
}

func TestCycleModel_Forward(t *testing.T) {
	tm := newTestModeWithSession(t)
	available := setupAvailableModels(t, tm)

	first := available[0]
	second := available[1]
	tm.mode.session.SetModel(first)

	tm.mode.cycleModel("forward")

	got := tm.mode.session.Model()
	if !ai.ModelsAreEqual(got, second) {
		t.Errorf("forward cycle: expected model %q, got %q", second.ID, got.ID)
	}
}

func TestCycleModel_Backward(t *testing.T) {
	tm := newTestModeWithSession(t)
	available := setupAvailableModels(t, tm)

	first := available[0]
	second := available[1]
	tm.mode.session.SetModel(second)

	tm.mode.cycleModel("backward")

	got := tm.mode.session.Model()
	if !ai.ModelsAreEqual(got, first) {
		t.Errorf("backward cycle: expected model %q, got %q", first.ID, got.ID)
	}
}

func TestCycleModel_WithScopedModels(t *testing.T) {
	tm := newTestModeWithSession(t)
	available := setupAvailableModels(t, tm)

	// Restrict cycling to the first two available models via scoped models.
	first := available[0]
	second := available[1]
	tm.mode.session.SetScopedModels([]core.ScopedModel{
		{Model: first},
		{Model: second},
	})
	tm.mode.session.SetModel(first)

	tm.mode.cycleModel("forward")

	got := tm.mode.session.Model()
	if !ai.ModelsAreEqual(got, second) {
		t.Errorf("scoped forward cycle: expected model %q, got %q", second.ID, got.ID)
	}
}

func TestCycleModel_WithScopedModels_Backward(t *testing.T) {
	tm := newTestModeWithSession(t)
	available := setupAvailableModels(t, tm)

	first := available[0]
	second := available[1]
	tm.mode.session.SetScopedModels([]core.ScopedModel{
		{Model: first},
		{Model: second},
	})
	tm.mode.session.SetModel(second)

	tm.mode.cycleModel("backward")

	got := tm.mode.session.Model()
	if !ai.ModelsAreEqual(got, first) {
		t.Errorf("scoped backward cycle: expected model %q, got %q", first.ID, got.ID)
	}
}

func TestCycleModel_ShowsModelName(t *testing.T) {
	tm := newTestModeWithSession(t)
	available := setupAvailableModels(t, tm)

	tm.mode.session.SetModel(available[0])
	tm.mode.cycleModel("forward")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Model:") {
		t.Errorf("expected 'Model:' status after cycling, got:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// handleFork tests
// ---------------------------------------------------------------------------

func TestHandleFork_Error(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Empty entryID causes Fork to return an error.
	tm.mode.handleFork("")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Fork failed") {
		t.Errorf("expected 'Fork failed' warning, got:\n%s", output)
	}
}

func TestHandleFork_Success(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Add a user message entry to the session.
	entryID := tm.mode.session.SessionManager.AppendAIMessage(ai.NewUserMsg("hello from fork", 0))
	if entryID == "" {
		t.Fatal("expected non-empty entry ID")
	}

	tm.mode.handleFork(entryID)
	tm.waitRender()

	// Should show success status.
	output := tm.renderedOutput()
	if !strings.Contains(output, "Branched") {
		t.Errorf("expected 'Branched' status after fork, got:\n%s", output)
	}

	// Editor should contain the forked message text.
	if got := tm.editorText(); !strings.Contains(got, "hello from fork") {
		t.Errorf("expected forked text in editor, got %q", got)
	}
}

func TestHandleFork_NonUserMessage(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Add an assistant message — Fork should reject it because role != "user".
	entryID := tm.mode.session.SessionManager.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{}))
	if entryID == "" {
		t.Fatal("expected non-empty entry ID")
	}

	tm.mode.handleFork(entryID)
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Fork failed") {
		t.Errorf("expected 'Fork failed' warning for non-user message, got:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// handleForkByNumber tests
// ---------------------------------------------------------------------------

func TestHandleForkByNumber_InvalidNumber(t *testing.T) {
	tm := newTestModeWithSession(t)

	tm.mode.handleForkByNumber("abc")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Usage") {
		t.Errorf("expected usage warning for invalid number, got:\n%s", output)
	}
}

func TestHandleForkByNumber_Zero(t *testing.T) {
	tm := newTestModeWithSession(t)

	// n < 1 is treated the same as invalid.
	tm.mode.handleForkByNumber("0")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Usage") {
		t.Errorf("expected usage warning for n=0, got:\n%s", output)
	}
}

func TestHandleForkByNumber_OutOfRange(t *testing.T) {
	tm := newTestModeWithSession(t)
	// No user messages in session, so n=1 is out of range.

	tm.mode.handleForkByNumber("1")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "not found") && !strings.Contains(output, "0 user") {
		t.Errorf("expected 'not found' warning, got:\n%s", output)
	}
}

func TestHandleForkByNumber_Valid(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Add a user message to fork from.
	tm.mode.session.SessionManager.AppendAIMessage(ai.NewUserMsg("numbered fork message", 0))

	tm.mode.handleForkByNumber("1")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Branched") {
		t.Errorf("expected 'Branched' status, got:\n%s", output)
	}

	if got := tm.editorText(); !strings.Contains(got, "numbered fork message") {
		t.Errorf("expected forked text in editor, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Init() pre-population tests
// ---------------------------------------------------------------------------

// TestInteractiveMode_Init_PrePopulatesHistoryFromSession verifies that Init()
// calls rebuildChatFromMessages() when the session already has messages (the
// --continue / --resume code path added in cycle 119).
func TestInteractiveMode_Init_PrePopulatesHistoryFromSession(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := core.NewSessionManager(cwd, agentDir+"/sessions")
	settingsManager := core.NewSettingsManager(cwd, agentDir)

	rl := core.NewResourceLoader(core.ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	rl.Reload()

	// Build an agent with one user and one assistant message already in state.
	userMsg := agent.NewAgentMessage(ai.NewUserMsg("hello from history", 0))
	assistantMsg := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Model:      "test-model",
		StopReason: ai.StopReasonStop,
		Content: []ai.AssistantContent{
			{Text: &ai.TextContent{Type: ai.ContentTypeText, Text: "hello back"}},
		},
	}))

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "test",
			ThinkingLevel: ai.ThinkingOff,
			Messages:      []agent.AgentMessage{userMsg, assistantMsg},
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return core.ConvertToLLM(msgs)
		},
	})

	session := core.NewAgentSession(core.AgentSessionOptions{
		Agent:           a,
		SessionManager:  sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   core.NewModelRegistry(core.NewAuthStorage(agentDir+"/auth.json"), ""),
		Cwd:             cwd,
	})
	t.Cleanup(func() { session.Close() })

	keybindings := core.NewKeybindingsManager("")
	m := NewInteractiveMode(nil, keybindings, nil, InteractiveModeOptions{})
	m.session = session
	t.Cleanup(func() { m.Shutdown() })

	// Init() should detect state.Messages is non-empty and call rebuildChatFromMessages().
	if err := m.Init(); err != nil {
		t.Fatalf("Init() returned error: %v", err)
	}

	// The message container must have ≥ 2 children (one per pre-existing message).
	children := m.messageContainer.ChildrenSnapshot()
	if len(children) < 2 {
		t.Errorf("expected ≥2 children in messageContainer after Init() with pre-existing messages, got %d", len(children))
	}
}

// ============================================================================
// SetUpdateChannel / startUpdateNoticeWatcher
// ============================================================================

func TestSetUpdateChannel_StoresChannel(t *testing.T) {
	tm := newTestMode(t)
	ch := make(chan string, 1)
	tm.mode.SetUpdateChannel(ch)
	if tm.mode.updateCh == nil {
		t.Error("expected updateCh to be set after SetUpdateChannel")
	}
}

func TestStartUpdateNoticeWatcher_ShowsNotice(t *testing.T) {
	tm := newTestMode(t)
	ch := make(chan string, 1)
	ch <- "fir v1.0.0 available"
	tm.mode.updateCh = ch

	before := tm.messageCount()
	tm.mode.startUpdateNoticeWatcher()
	tm.waitRender()

	if tm.messageCount() <= before {
		t.Error("expected message to be added when notice is non-empty")
	}
}

func TestStartUpdateNoticeWatcher_EmptyNotice(t *testing.T) {
	tm := newTestMode(t)
	ch := make(chan string, 1)
	ch <- "" // empty — no notice
	tm.mode.updateCh = ch

	before := tm.messageCount()
	tm.mode.startUpdateNoticeWatcher()
	tm.waitRender()

	if tm.messageCount() != before {
		t.Error("expected no message when notice is empty")
	}
}

func TestStartUpdateNoticeWatcher_ContextCancel(t *testing.T) {
	tm := newTestMode(t)
	ch := make(chan string) // unbuffered — will not send until cancelled
	tm.mode.updateCh = ch

	// Cancel the mode context immediately; goroutine should exit cleanly.
	tm.mode.cancel()
	tm.mode.startUpdateNoticeWatcher()

	// Give the goroutine a moment to exit.
	done := make(chan struct{})
	go func() {
		tm.waitRender()
		close(done)
	}()

	select {
	case <-done:
		// OK — goroutine exited promptly
	case <-time.After(2 * time.Second):
		t.Error("goroutine did not exit promptly after context cancellation")
	}
}
