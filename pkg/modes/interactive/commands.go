// commands.go — slash command dispatch and individual command handlers.
package interactive

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/agent/tools"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/resources/clipboard"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/fir/pkg/update"
)

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
	if resources.IsBuiltinSlashCommandName(cmd[1:]) {
		return true
	}
	return false
}

// isExtensionSlashCommand reports whether text is a slash command registered
// by a running extension.
func (m *InteractiveMode) isExtensionSlashCommand(text string) bool {
	if m.extSetup == nil || m.extSetup.Manager == nil {
		return false
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	name := strings.TrimPrefix(parts[0], "/")
	for _, ec := range m.extSetup.Manager.GetCommands() {
		if ec.Spec.Name == name {
			return true
		}
	}
	return false
}

// handleExtensionSlashCommand dispatches text to the owning extension and
// shows any message it returns.
func (m *InteractiveMode) handleExtensionSlashCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	name := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]

	// Record extension command for audit/metering.
	if m.session != nil {
		m.session.RecordCommand("ext:"+name, strings.Join(args, " "))
	}

	result, err := m.extSetup.Manager.DispatchCommand(name, args, 0)
	if err != nil {
		m.showWarning(fmt.Sprintf("Extension command /%s failed: %v", name, err))
		return
	}
	if result.Message != "" {
		m.showStatus(result.Message)
	}
}

// handleSlashCommand dispatches a builtin slash command.
// Every case in the switch below must have a corresponding entry in
// resources.BuiltinSlashCommands (or builtinAliases for hidden aliases);
// TestInteractiveMode_IsBuiltinSlashCommand enforces this.
func (m *InteractiveMode) handleSlashCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	cmd := parts[0]

	// Record all slash commands for audit/metering. Recording is best-effort;
	// it is skipped if there is no session. Args are included for commands
	// where they are meaningful for usage analysis.
	if m.session != nil {
		args := ""
		if len(parts) > 1 {
			args = strings.Join(parts[1:], " ")
		}
		m.session.RecordCommand(strings.TrimPrefix(cmd, "/"), args)
	}

	// Track slash command usage locally.
	if m.session != nil {
		if ut := m.session.UsageTracker(); ut != nil {
			ut.RecordSlashCommand(cmd)
		}
	}

	switch cmd {
	case "/help":
		m.showHelp()
	case "/new":
		var newName string
		if len(parts) > 1 {
			newName = strings.Join(parts[1:], " ")
		}
		go m.handleClearCommand(newName)
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
	case "/tree":
		m.showTreeSelector()
	case "/export":
		m.handleExportCommand(text)
	case "/share":
		m.handleShareCommand()
	case "/name":
		m.handleNameCommand(text)
	case "/changelog":
		m.handleChangelogCommand()
	case "/reload":
		m.handleReloadCommand()
	case "/skills":
		m.handleSkillsCommand(parts[1:])
	case "/reexec":
		m.handleReexecCommand(text)
	case "/update":
		go m.handleUpdateCommand()
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
	case "/plan":
		m.handlePlanCommand()
	default:
		// Not a builtin command.
		// Check if it's a skill or prompt template command before declaring unknown.
		m.showWarning(fmt.Sprintf("Unknown command: %s. Type /help for available commands.", cmd))
	}
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
	ctx = session.WithCompactionProgress(ctx, progressFn)

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
func (m *InteractiveMode) compactionLoaderLabel(info *session.CompactionInfo, suffix string) string {
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
	// Record the bash invocation for audit/metering.
	if m.session != nil {
		cmdArgs := command
		if len(cmdArgs) > 200 {
			cmdArgs = cmdArgs[:197] + "..."
		}
		name := "bash"
		if excludeFromContext {
			name = "bash_excluded"
		}
		m.session.RecordCommand(name, cmdArgs)
	}

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
	img := m.clipboardReader()
	if img == nil {
		return // no image on clipboard, silently ignore
	}

	ext := clipboard.ExtensionForImageMimeType(img.MimeType)
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

func (m *InteractiveMode) handleClearCommand(newName string) {
	if m.session != nil {
		_, err := m.session.NewSessionCmd()
		if err != nil {
			m.showWarning(fmt.Sprintf("Failed to create new session: %s", err))
			return
		}
		if newName != "" {
			m.session.SetSessionName(newName)
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
	if newName != "" {
		m.showStatus(fmt.Sprintf("New session started: %s", newName))
	} else {
		m.showStatus("New session started")
	}
}

// ============================================================================
// Export / share / copy / name / changelog / session info / reload
// ============================================================================

// extractEntryText extracts display text from a session entry.
func extractEntryText(entry *store.SessionEntry) string {
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
	link := session.Hyperlink(gistURL, gistURL)
	m.showStatus(fmt.Sprintf("Session shared: %s", link))
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
	lines = append(lines, t.Fg("dim", "Mode: ")+"interactive")
	if bin, err := os.Executable(); err == nil {
		lines = append(lines, t.Fg("dim", "Binary: ")+bin)
	}
	if sessionName != "" {
		lines = append(lines, t.Fg("dim", "Name: ")+sessionName)
	}
	if stats.SessionFile != "" {
		lines = append(lines, t.Fg("dim", "File: ")+stats.SessionFile)
	} else {
		lines = append(lines, t.Fg("dim", "File: ")+"In-memory")
	}
	lines = append(lines, t.Fg("dim", "ID: ")+stats.SessionID)
	if m.extSetup != nil && m.extSetup.Manager != nil {
		enabled := m.extSetup.Manager.EnabledExtensionNames()
		if len(enabled) > 0 {
			lines = append(lines, t.Fg("dim", "Extensions: ")+strings.Join(enabled, ", "))
		}
	}
	if model := m.session.Model(); model != nil {
		lines = append(lines, t.Fg("dim", "Model: ")+model.ID)
		lines = append(lines, t.Fg("dim", "Provider: ")+string(model.Provider))
	}
	if cwd, err := os.Getwd(); err == nil {
		if mcpCfg, err := mcp.LoadDefaultConfigs(cwd); err == nil && len(mcpCfg.MCPServers) > 0 {
			var names []string
			for name := range mcpCfg.MCPServers {
				names = append(names, name)
			}
			sort.Strings(names)
			lines = append(lines, t.Fg("dim", "MCP Servers: ")+strings.Join(names, ", "))
		}
	}
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
	entries := session.GetChangelogEntries()
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
func formatChangelogEntry(t *itheme.Theme, entry session.ChangelogEntry) []string {
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
		if m.beforeExtensionReload != nil {
			if err := m.beforeExtensionReload(); err != nil {
				m.showWarning(fmt.Sprintf("Extension reload setup failed: %v", err))
			}
		}
		if err := m.extSetup.Reload(m.ctx); err != nil {
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
	sorted := make([]resources.Skill, len(skills))
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
	builtins := resources.LoadBuiltinSkills()
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
	err := fs.WalkDir(resources.BuiltinSkillsFS, prefix, func(path string, d fs.DirEntry, walkErr error) error {
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
		data, readErr := resources.BuiltinSkillsFS.ReadFile(path)
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

func (m *InteractiveMode) handleUpdateCommand() {
	if m.session != nil && m.session.IsStreaming() {
		m.showWarning("Wait for the current response to finish.")
		return
	}

	m.showMessage("Checking for updates...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rel, err := update.FetchLatestOrGH(ctx)
	if err != nil {
		m.showWarning(fmt.Sprintf("Failed to check for updates: %v", err))
		return
	}

	if !update.IsNewer(rel.Version, version) {
		m.showMessage(fmt.Sprintf("Already on the latest version (%s).", version))
		return
	}

	m.showMessage(fmt.Sprintf("Updating fir %s → %s...", version, rel.Version))

	if err := update.SelfUpdate(ctx, rel); err != nil {
		m.showWarning(fmt.Sprintf("Update failed: %v", err))
		return
	}

	m.showMessage(fmt.Sprintf("Updated to fir %s. Restarting...", rel.Version))

	// Give the user a moment to see the message, then reexec.
	time.Sleep(500 * time.Millisecond)
	m.handleReexecCommand("/reexec")
}

func (m *InteractiveMode) handleReexecCommand(text string) {
	if m.session == nil {
		m.showWarning("No session available")
		return
	}
	if m.session.IsStreaming() {
		m.showWarning("Wait for the current response to finish.")
		return
	}

	reexecPath := strings.TrimSpace(strings.TrimPrefix(text, "/reexec"))
	binary := ""
	if reexecPath == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			m.showWarning(fmt.Sprintf("Cannot determine executable path: %v", err))
			return
		}
	} else {
		abs, err := filepath.Abs(reexecPath)
		if err != nil {
			m.showWarning(fmt.Sprintf("Invalid reexec path %q: %v", reexecPath, err))
			return
		}
		info, err := os.Stat(abs)
		if err != nil {
			m.showWarning(fmt.Sprintf("Cannot access reexec binary %q: %v", reexecPath, err))
			return
		}
		if info.IsDir() {
			m.showWarning(fmt.Sprintf("Reexec path is a directory: %s", abs))
			return
		}
		if info.Mode()&0o111 == 0 {
			m.showWarning(fmt.Sprintf("Reexec target is not executable: %s", abs))
			return
		}
		binary = abs
	}

	sessionFile := m.session.SessionManager.GetSessionFile()
	sessionDir := m.session.SessionManager.GetSessionDir()
	if sessionFile == "" {
		m.showWarning("No persisted session to resume after reexec")
		return
	}

	// Force-flush the session file so metadata (e.g. session name) that
	// hasn't been written yet (no assistant message) survives the reexec.
	m.session.SessionManager.ForceFlush()

	sessionBase := filepath.Base(sessionFile)

	// Save queue, pending input, and extension session data to survive the exec.
	// ShutdownAndCollect fires "session_shutdown" to every extension so their
	// handlers can call ctx.set_session_data(...), waits for those inbound
	// RPC calls to complete, and then snapshots all per-extension data.
	// This must happen before m.Shutdown() cancels the bridge Run goroutines.
	queueTexts := m.session.PeekFollowUpQueue()
	sc := &store.ReexecSidecar{
		QueueMessages: queueTexts,
	}
	if m.extSetup != nil && m.extSetup.Manager != nil {
		sc.ExtensionData = m.extSetup.Manager.ShutdownAndCollect()
	}
	if err := store.WriteReexecSidecar(sessionFile, sc); err != nil {
		// Non-fatal, but warn the user.
		m.showWarning(fmt.Sprintf("Failed to save reexec state: %v", err))
	}

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
	env := append(os.Environ(), "FIR_REEXEC_CONTINUE=1")
	if err := syscall.Exec(m.reexecBinary, m.reexecArgs, env); err != nil {
		fmt.Fprintf(os.Stderr, "reexec failed: exec %s: %v\n", m.reexecBinary, err)
		os.Exit(1)
	}
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
	// Cycle through all available models.
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
// Plan commands
// ============================================================================

func (m *InteractiveMode) handlePlanCommand() {
	m.togglePlanVisibility()
}

func (m *InteractiveMode) togglePlanVisibility() {
	if m.session == nil {
		m.showWarning("No active session.")
		return
	}
	entries := m.session.PlanEntries()
	title := m.session.PlanTitle()
	metadata := m.session.PlanMetadata()
	if len(entries) == 0 {
		m.showStatus("No plan entries.")
		return
	}
	m.planHidden = !m.planHidden
	if m.planHidden {
		if m.planInContainer {
			m.planContainer.Clear()
			m.planInContainer = false
		}
	} else {
		if m.planComponent == nil {
			m.planComponent = components.NewPlanComponent(title, entries, metadata)
		}
		if !m.planInContainer {
			m.planContainer.AddChild(m.planComponent)
			m.planInContainer = true
		}
	}
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
  /plan           - Show/hide the current session plan
  /theme          - Select theme
  /new            - Start a new session
  /compact        - Compact conversation context
  /resume         - Resume a different session
  /session        - Show session info and stats
  /name <name>    - Set session display name
  /login          - Login with OAuth provider
  /logout         - Logout from OAuth provider
  /tree           - Navigate session tree (switch branches)
  /export         - Export session to HTML file
  /share          - Share session as a secret GitHub gist
  /changelog      - Show changelog entries
  /reload         - Reload extensions, skills, prompts, and themes
  /skills         - List loaded skills (or /skills install <name>)
  /reexec [path] - Re-exec into specified or current binary (%s), preserving the session
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
  Ctrl+R          - Toggle plan visibility
  Ctrl+Z          - Suspend to background
  Ctrl+V          - Paste image from clipboard
  /               - Slash commands
  !<command>      - Run bash command`
	bin, _ := os.Executable()
	if bin == "" {
		bin = "(unknown)"
	} else if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(bin, home+"/") {
		bin = "~/" + bin[len(home)+1:]
	}
	m.showMessage(fmt.Sprintf(helpText, bin))
}
