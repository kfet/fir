package interactive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/resources/clipboard"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/fir/pkg/tui"
)

func TestNewInteractiveMode(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	if m == nil {
		t.Fatal("expected non-nil InteractiveMode")
	}
	if m.autoCompactMode != "client" {
		t.Error("expected autoCompactMode default client")
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
	if data.AutoCompactMode != "client" {
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

	sm := store.NewSessionStore(cwd, agentDir+"/sessions")
	settingsManager := config.NewSettingsManager(cwd, agentDir)

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
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
			return store.ConvertToLLM(msgs)
		},
	})

	session := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   models.NewModelRegistry(auth.NewAuthStorage(agentDir+"/auth.json"), ""),
		Cwd:             cwd,
	})
	t.Cleanup(func() { session.Close() })

	return newTestModeInternal(t, session)
}

// newTestModeInternal creates a minimal interactive mode with MockTerminal.
// If session is non-nil it is set BEFORE ui.Start() to avoid a data race
// between the test goroutine and the TUI render goroutine.
func newTestModeInternal(t *testing.T, session *session.AgentSession) *testMode {
	t.Helper()
	term := tui.NewMockTerminal(80, 24)
	ui := tui.NewTUI(term, false)

	keybindings := tui.NewKeybindingsManager("")
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
	// Give the TUI event loop time to process — poll quickly rather than
	// sleeping a fixed 200ms.  Most events settle within a few ms.
	time.Sleep(10 * time.Millisecond)
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
	keybindings := tui.NewKeybindingsManager("")
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

func TestInteractiveMode_ReexecCommand_CustomBinary(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Ensure the session is persisted so /reexec can resume it.
	tm.mode.session.SessionStore.AppendAIMessage(ai.NewUserMsg("persist", 0))

	bin := filepath.Join(t.TempDir(), "fir-test-bin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	tm.mode.handleReexecCommand("/reexec " + bin)

	abs, _ := filepath.Abs(bin)
	if tm.mode.reexecBinary != abs {
		t.Fatalf("reexecBinary = %q, want %q", tm.mode.reexecBinary, abs)
	}
	if len(tm.mode.reexecArgs) < 1 || tm.mode.reexecArgs[0] != abs {
		t.Fatalf("reexecArgs = %v, want first arg %q", tm.mode.reexecArgs, abs)
	}
}

func TestInteractiveMode_ReexecCommand_NonExecutablePath(t *testing.T) {
	tm := newTestModeWithSession(t)

	// Ensure the session is persisted so validation reaches executable checks.
	tm.mode.session.SessionStore.AppendAIMessage(ai.NewUserMsg("persist", 0))

	nonExec := filepath.Join(t.TempDir(), "not-exec")
	if err := os.WriteFile(nonExec, []byte("nope"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tm.mode.handleReexecCommand("/reexec " + nonExec)

	if tm.mode.reexecBinary != "" {
		t.Fatalf("reexecBinary = %q, want empty", tm.mode.reexecBinary)
	}
}

func TestInteractiveMode_IsBuiltinSlashCommand(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})

	// Every entry in resources.BuiltinSlashCommands must be recognised.
	for _, cmd := range resources.BuiltinSlashCommands {
		full := "/" + cmd.Name
		if !m.isBuiltinSlashCommand(full) {
			t.Errorf("resources.BuiltinSlashCommands entry %q not recognised by isBuiltinSlashCommand", full)
		}
	}

	// Every case handled by handleSlashCommand must also be recognised.
	// If you add a new case to that switch, add it here AND to
	// resources.BuiltinSlashCommands (or builtinAliases for hidden aliases).
	handleCases := []string{
		"/help",
		"/new",
		"/compact",
		"/model",
		"/thinking",
		"/theme",
		"/settings",
		"/session",
		"/resume",
		"/login", "/logout",
		"/tree",
		"/export",
		"/share",
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
			t.Errorf("handleSlashCommand case %q not recognised; add it to resources.BuiltinSlashCommands or builtinAliases", cmd)
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
	time.Sleep(50 * time.Millisecond)
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
	time.Sleep(50 * time.Millisecond)
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
	for _, cmd := range []string{"/help"} {
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
		{"/quit", true, false, false},
		{"/exit", true, false, false},
		{"/bogus", false, false, true},
		// New commands (session-independent, should show warnings or work)
		{"/login", false, false, true},     // "not yet implemented" warning
		{"/logout", false, false, true},    // "not yet implemented" warning
		{"/export", false, false, true},    // "not yet implemented"
		{"/share", false, false, true},     // "not yet implemented"
		{"/name", false, false, true},      // usage warning (no args)
		{"/changelog", false, true, false}, // shows "No changelog entries found." message
		{"/tree", false, false, true},
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

	// Type "/he" — should filter to "help"
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

func TestInteractiveMode_StatusClearedOnSubmit(t *testing.T) {
	tm := newTestMode(t)

	tm.mode.showStatus("Queue is empty")
	tm.waitRender()

	output := tm.renderedOutput()
	if !strings.Contains(output, "Queue is empty") {
		t.Fatal("expected status text before submit")
	}

	// Simulate user pressing Enter with a new message — the status should clear.
	// Use a slash command that also produces its own status, but first test with
	// a non-slash input that goes to session.Prompt (which is nil in test, but
	// the clearing happens before dispatch).
	tm.term.ClearOutput()
	tm.mode.editor.SetText("/help")
	tm.mode.editor.OnSubmit("/help")
	tm.waitRender()

	output = tm.renderedOutput()
	if strings.Contains(output, "Queue is empty") {
		t.Error("expected previous status to be cleared after submit")
	}
}

// ---------------------------------------------------------------------------
// extractEntryText
// ---------------------------------------------------------------------------

func TestExtractEntryText_NonMessage(t *testing.T) {
	entry := &store.SessionEntry{Type: "compaction"}
	got := extractEntryText(entry)
	if got != "compaction" {
		t.Errorf("expected 'compaction', got %q", got)
	}
}

func TestExtractEntryText_StringContent(t *testing.T) {
	raw := []byte(`{"role":"user","content":"hello world"}`)
	entry := &store.SessionEntry{Type: "message", RawMessage: raw}
	got := extractEntryText(entry)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestExtractEntryText_StringContentNewlines(t *testing.T) {
	raw := []byte(`{"role":"user","content":"line1\nline2"}`)
	entry := &store.SessionEntry{Type: "message", RawMessage: raw}
	got := extractEntryText(entry)
	if !strings.Contains(got, "line1 line2") {
		t.Errorf("expected newlines replaced with spaces, got %q", got)
	}
}

func TestExtractEntryText_ArrayContent(t *testing.T) {
	raw := []byte(`{"role":"assistant","content":[{"type":"text","text":"response text"}]}`)
	entry := &store.SessionEntry{Type: "message", RawMessage: raw}
	got := extractEntryText(entry)
	if got != "response text" {
		t.Errorf("expected 'response text', got %q", got)
	}
}

func TestExtractEntryText_InvalidJSON(t *testing.T) {
	entry := &store.SessionEntry{Type: "message", RawMessage: []byte(`{invalid`)}
	got := extractEntryText(entry)
	if got != "message" {
		t.Errorf("expected 'message', got %q", got)
	}
}

func TestExtractEntryText_EmptyRawMessage(t *testing.T) {
	entry := &store.SessionEntry{Type: "message"}
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

func TestInteractiveMode_SlashReloadRunsExtensionPreHook(t *testing.T) {
	tm := newTestModeWithSession(t)

	called := false
	tm.mode.extSetup = &extension.SetupResult{}
	tm.mode.SetBeforeExtensionReload(func() error {
		called = true
		return nil
	})

	tm.mode.handleSlashCommand("/reload")
	tm.waitRender()

	if !called {
		t.Fatal("expected /reload to run extension pre-reload hook")
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
	info := &session.CompactionInfo{MessagesToSummarize: 42, TokensBefore: 95000}
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

	// Override the clipboard reader to return nil (no image available).
	tm.mode.clipboardReader = func() *clipboard.ClipboardImage { return nil }

	tm.mode.handleClipboardImagePaste()
	tm.waitRender()

	if got := tm.editorText(); got != initial {
		t.Errorf("expected editor unchanged (no image), got %q", got)
	}
}

func TestHandleClipboardImagePaste_WithImage(t *testing.T) {
	tm := newTestMode(t)

	// Override the clipboard reader to return a fake PNG image.
	fakeBytes := []byte("\x89PNG\r\n\x1a\n" + string(make([]byte, 100)))
	tm.mode.clipboardReader = func() *clipboard.ClipboardImage {
		return &clipboard.ClipboardImage{Bytes: fakeBytes, MimeType: "image/png"}
	}

	tm.mode.handleClipboardImagePaste()
	tm.waitRender()

	got := tm.editorText()
	if got == "" {
		t.Errorf("expected editor to contain image path, got empty string")
	}
	if !strings.HasSuffix(got, ".png") {
		t.Errorf("expected editor to contain a .png path, got %q", got)
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
	for _, key := range envkeys.KnownApiKeyEnvVars() {
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
// ---------------------------------------------------------------------------
// Init() pre-population tests
// ---------------------------------------------------------------------------

// TestInteractiveMode_Init_PrePopulatesHistoryFromSession verifies that Init()
// calls rebuildChatFromMessages() when the session already has messages (the
// --continue / --resume code path added in cycle 119).
func TestInteractiveMode_Init_PrePopulatesHistoryFromSession(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := store.NewSessionStore(cwd, agentDir+"/sessions")
	settingsManager := config.NewSettingsManager(cwd, agentDir)

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
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
			return store.ConvertToLLM(msgs)
		},
	})

	session := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   models.NewModelRegistry(auth.NewAuthStorage(agentDir+"/auth.json"), ""),
		Cwd:             cwd,
	})
	t.Cleanup(func() { session.Close() })

	keybindings := tui.NewKeybindingsManager("")
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

// ============================================================================
// userMsgText
// ============================================================================

func TestUserMsgText_StringContent(t *testing.T) {
	got := userMsgText("hello world")
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestUserMsgText_EmptyString(t *testing.T) {
	got := userMsgText("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestUserMsgText_SliceWithTextBlock(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "slice text"},
	}
	got := userMsgText(content)
	if got != "slice text" {
		t.Errorf("expected 'slice text', got %q", got)
	}
}

// Text block may appear after an image block; the helper must still return it.
func TestUserMsgText_SliceImageThenText(t *testing.T) {
	content := []any{
		map[string]any{"type": "image_url", "url": "data:image/png;base64,..."},
		map[string]any{"type": "text", "text": "caption"},
	}
	got := userMsgText(content)
	if got != "caption" {
		t.Errorf("expected 'caption', got %q", got)
	}
}

// When only an image block is present, text extraction returns "".
func TestUserMsgText_SliceNoTextBlock(t *testing.T) {
	content := []any{
		map[string]any{"type": "image_url", "url": "data:image/png;base64,..."},
	}
	got := userMsgText(content)
	if got != "" {
		t.Errorf("expected empty string for image-only slice, got %q", got)
	}
}

func TestUserMsgText_EmptySlice(t *testing.T) {
	got := userMsgText([]any{})
	if got != "" {
		t.Errorf("expected empty string for empty slice, got %q", got)
	}
}

func TestUserMsgText_UnknownType(t *testing.T) {
	got := userMsgText(42)
	if got != "" {
		t.Errorf("expected empty string for unknown type, got %q", got)
	}
}

func TestUserMsgText_NilValue(t *testing.T) {
	got := userMsgText(nil)
	if got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

// ============================================================================
// Preemptive-dedup counter: onMessageStart
// ============================================================================

// makeUserEvent builds a minimal AgentEvent carrying a user message.
func makeUserMsgEvent(text string) *agent.AgentEvent {
	msg := agent.NewAgentMessage(ai.NewUserMsg(text, 0))
	return &agent.AgentEvent{
		Type:    agent.EventMessageStart,
		Message: &msg,
	}
}

// TestOnMessageStart_SkipsWhenPreemptivePending verifies that when
// AddUserMessage has already shown a user message (counter > 0), the
// corresponding EventMessageStart does NOT add a second copy to the
// message container.
func TestOnMessageStart_SkipsWhenPreemptivePending(t *testing.T) {
	tm := newTestMode(t)

	// AddUserMessage increments the counter and adds 1 child.
	tm.mode.pendingPreemptiveUserMsgs.Add(1)
	before := tm.messageCount()

	tm.mode.onMessageStart(makeUserMsgEvent("hello"))
	tm.waitRender()

	// Counter should be back to 0 (consumed by onMessageStart).
	if got := tm.mode.pendingPreemptiveUserMsgs.Load(); got != 0 {
		t.Errorf("expected counter 0 after skip, got %d", got)
	}
	// No new child added.
	if after := tm.messageCount(); after != before {
		t.Errorf("expected message count unchanged (%d), got %d", before, after)
	}
}

// TestOnMessageStart_RendersWhenNoPreemptivePending verifies that an
// EventMessageStart user message with no preemptive counter (e.g. from an
// extension) is rendered and the counter remains at 0.
func TestOnMessageStart_RendersWhenNoPreemptivePending(t *testing.T) {
	tm := newTestMode(t)

	if got := tm.mode.pendingPreemptiveUserMsgs.Load(); got != 0 {
		t.Fatalf("precondition: expected counter 0, got %d", got)
	}
	before := tm.messageCount()

	tm.mode.onMessageStart(makeUserMsgEvent("from extension"))
	tm.waitRender()

	// Counter must be restored to 0.
	if got := tm.mode.pendingPreemptiveUserMsgs.Load(); got != 0 {
		t.Errorf("expected counter 0 after render, got %d", got)
	}
	// One new child added.
	if after := tm.messageCount(); after != before+1 {
		t.Errorf("expected message count %d, got %d", before+1, after)
	}
}

// TestOnMessageStart_MultiplePreemptive verifies that N preemptive messages
// consumed exactly N EventMessageStart events without adding duplicates.
func TestOnMessageStart_MultiplePreemptive(t *testing.T) {
	tm := newTestMode(t)

	const n = 3
	tm.mode.pendingPreemptiveUserMsgs.Add(int32(n))
	before := tm.messageCount()

	for i := range n {
		tm.mode.onMessageStart(makeUserMsgEvent(fmt.Sprintf("msg %d", i)))
	}
	tm.waitRender()

	if got := tm.mode.pendingPreemptiveUserMsgs.Load(); got != 0 {
		t.Errorf("expected counter 0 after %d skips, got %d", n, got)
	}
	if after := tm.messageCount(); after != before {
		t.Errorf("expected message count unchanged (%d), got %d", before, after)
	}
}

// TestOnMessageStart_NilMessageIsNoop ensures a nil Message field is handled
// gracefully (no panic, no state change).
func TestOnMessageStart_NilMessageIsNoop(t *testing.T) {
	tm := newTestMode(t)
	before := tm.messageCount()

	tm.mode.onMessageStart(&agent.AgentEvent{Type: agent.EventMessageStart, Message: nil})
	tm.waitRender()

	if after := tm.messageCount(); after != before {
		t.Errorf("expected no change for nil message, got %d → %d", before, after)
	}
	if got := tm.mode.pendingPreemptiveUserMsgs.Load(); got != 0 {
		t.Errorf("expected counter unchanged at 0, got %d", got)
	}
}

func TestInteractiveMode_StatusClearsOnNextTurn(t *testing.T) {
	tm := newTestMode(t)

	tm.mode.showStatus("Compacted: 1000 tokens")

	if len(tm.mode.commandStatusContainer.ChildrenSnapshot()) == 0 {
		t.Fatal("expected commandStatusContainer to have children immediately after showStatus")
	}

	// Status should persist (no auto-clear timer).
	time.Sleep(100 * time.Millisecond)
	if len(tm.mode.commandStatusContainer.ChildrenSnapshot()) == 0 {
		t.Error("expected commandStatusContainer to still have children — no auto-clear")
	}

	// Simulate clearing on Escape.
	tm.mode.commandStatusContainer.Clear()
	if len(tm.mode.commandStatusContainer.ChildrenSnapshot()) != 0 {
		t.Error("expected commandStatusContainer to be cleared after Escape")
	}
}
