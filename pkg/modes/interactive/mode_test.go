package interactive

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/modes/interactive/components"
	itheme "github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
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

	m.statusContainer = &tui.Container{}
	ui.AddChild(m.statusContainer)

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

	if !tm.mode.isBashMode {
		t.Fatal("expected bash mode after typing '!'")
	}

	tm.pressEscape()
	tm.waitRender()

	if tm.mode.isBashMode {
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

	builtins := []string{
		"/help", "/hotkeys", "/clear", "/new", "/compact", "/model",
		"/thinking", "/theme", "/settings", "/session", "/resume",
		"/login", "/logout", "/quit", "/exit",
	}
	for _, cmd := range builtins {
		if !m.isBuiltinSlashCommand(cmd) {
			t.Errorf("expected %q to be a builtin command", cmd)
		}
	}

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

	if tm.mode.isBashMode {
		t.Fatal("should not be in bash mode initially")
	}

	tm.typeText("!")
	tm.waitRender()
	if !tm.mode.isBashMode {
		t.Error("expected bash mode after typing '!'")
	}

	// Backspace to remove '!' should exit bash mode
	tm.pressBackspace()
	tm.waitRender()
	if tm.mode.isBashMode {
		t.Error("expected to leave bash mode after deleting '!'")
	}
}

func TestInteractiveMode_BashModeWithCommand(t *testing.T) {
	tm := newTestMode(t)

	tm.typeText("!echo hello")
	tm.waitRender()

	if !tm.mode.isBashMode {
		t.Error("expected bash mode with '!echo hello'")
	}

	if got := tm.editorText(); got != "!echo hello" {
		t.Errorf("expected %q, got %q", "!echo hello", got)
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
		wantWarning  bool // something added to statusContainer
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
			m2.statusContainer = &tui.Container{}

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
			if tt.wantWarning && len(m2.statusContainer.Children) == 0 {
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
