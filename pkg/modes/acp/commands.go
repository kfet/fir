// commands.go — slash command registry and dispatch for ACP mode.
package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/fir/pkg/slashcmd"
	"github.com/kfet/fir/pkg/slashcmd/builtin"
	"github.com/kfet/pinoauth"
)

// ============================================================================
// Command infrastructure
// ============================================================================

// commandContext is the ACP host environment passed to every slash-command
// handler as slashcmd.Ctx.Host. It implements builtin.Host for the shared
// handlers and slashcmd.Output for user feedback.
type commandContext struct {
	sessionID string
	entry     *firSession
	agent     *firAgent
}

// sendMessage is a convenience shorthand for sending a message to the client.
func (c *commandContext) sendMessage(msg string) {
	c.agent.sendAgentMessage(c.sessionID, msg)
}

// slashcmd.Output — ACP renders every kind of feedback as an agent message.
func (c *commandContext) Message(text string) { c.sendMessage(text) }
func (c *commandContext) Status(text string)  { c.sendMessage(text) }
func (c *commandContext) Warn(text string)    { c.sendMessage(text) }
func (c *commandContext) Code(text string)    { c.sendMessage("```\n" + text + "\n```") }

// LoginCallbacks reports the auth URL and progress as agent messages. ACP has
// no input channel during a command, so prompts take their default.
func (c *commandContext) LoginCallbacks() pinoauth.LoginCallbacks {
	return pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			msg := fmt.Sprintf("Open this URL to authenticate:\n%s", session.FormatAuthURLs(info.URL, info.ShortURL))
			if info.Instructions != "" {
				msg += "\n\n" + info.Instructions
			}
			c.sendMessage(msg)
		},
		OnProgress: func(message string) { c.sendMessage(message) },
		OnPrompt: func(prompt pinoauth.Prompt) (string, error) {
			c.sendMessage(prompt.Message + " (using default)")
			return "", nil
		},
	}
}

// builtin.Host.
func (c *commandContext) Cwd() string { return c.entry.cwd }

func (c *commandContext) Session() builtin.Session {
	if c.entry.session == nil {
		return nil
	}
	return builtin.NewSession(c.entry.session)
}

func (c *commandContext) Auth() builtin.Auth {
	if c.entry.modelRegistry == nil || c.entry.modelRegistry.AuthStorage() == nil {
		return nil
	}
	reg := c.entry.modelRegistry
	return builtin.NewAuthStorage(reg.AuthStorage(), reg.Refresh)
}

func (c *commandContext) MCP() builtin.MCP {
	entry := c.entry
	return builtin.ManagerMCP(
		func() *mcp.Manager { return entry.mcpManager },
		func(ctx context.Context) error {
			err := session.ReloadMCP(ctx, &entry.mcpManager, entry.session, entry.cwd, c.agent.options.MCPConfig, nil)
			entry.mcpStatus = mcp.StatusFunc(entry.mcpManager)
			return err
		},
	)
}

// acpOnly adapts an ACP-specific handler to slashcmd.Handler.
func acpOnly(fn func(ctx *commandContext, args string)) slashcmd.Handler {
	return func(c *slashcmd.Ctx, args string) { fn(c.Host.(*commandContext), args) }
}

// newCommandRegistry binds the shared builtin handlers plus the ACP-specific
// ones. Every command declared for ACP in slashcmd.Specs must be bound here;
// TestCommandParity enforces it.
func newCommandRegistry() *slashcmd.Registry {
	r := slashcmd.NewRegistry(slashcmd.ACP)
	builtin.Register(r)
	r.Bind("help", acpOnly(cmdHelp))
	r.Bind("compact", acpOnly(cmdCompact))
	r.Bind("resume", acpOnly(cmdResume))
	r.Bind("continue", acpOnly(cmdContinue))
	r.Bind("session", acpOnly(cmdSession))
	r.Bind("changelog", acpOnly(cmdChangelog))
	r.Bind("reload", acpOnly(cmdReload))
	return r
}

// CommandRegistry returns the ACP slash-command registry (for parity tests).
func CommandRegistry() *slashcmd.Registry { return newCommandRegistry() }

// availableCommands returns the ACP command list from the shared registry.
func availableCommands() []acpsdk.AvailableCommand {
	specs := slashcmd.ForMode(slashcmd.ACP)
	cmds := make([]acpsdk.AvailableCommand, 0, len(specs))
	for _, s := range specs {
		cmds = append(cmds, acpsdk.AvailableCommand{Name: s.Name, Description: s.FullDescription(slashcmd.ACP)})
	}
	return cmds
}

// ============================================================================
// Dispatch
// ============================================================================

func (pa *firAgent) handleSlashCommand(sessionID string, entry *firSession, command, args string) bool {
	ctx := &commandContext{sessionID: sessionID, entry: entry, agent: pa}

	// Lazily initialize the command registry (supports tests that don't set it).
	cmds := pa.commands
	if cmds == nil {
		cmds = newCommandRegistry()
	}

	// 1. Built-in commands.
	if cmds.Dispatch(&slashcmd.Ctx{Out: ctx, Host: ctx}, command, args) {
		return true
	}

	// 2. Extension commands.
	if entry.extSetup != nil && entry.extSetup.Manager != nil {
		for _, ec := range entry.extSetup.Manager.GetCommands() {
			if ec.Spec.Name == command {
				var argList []string
				if args != "" {
					argList = strings.Fields(args)
				}
				result, err := entry.extSetup.Manager.DispatchCommand(command, argList, 0)
				if err != nil {
					ctx.sendMessage(fmt.Sprintf("Extension command /%s failed: %v", command, err))
				} else if result.Message != "" {
					ctx.sendMessage(result.Message)
				}
				return true
			}
		}
	}

	// 3. Skill commands.
	if strings.HasPrefix(command, "skill:") && (entry.settingsManager == nil || entry.settingsManager.GetEnableSkillCommands()) {
		skillName := strings.TrimPrefix(command, "skill:")
		skills, _ := entry.session.ResourceLoader().GetSkills()
		for _, s := range skills {
			if s.Name == skillName {
				_ = entry.session.Prompt(fmt.Sprintf("/skill:%s %s", s.Name, args))
				return true
			}
		}
	}

	return false
}

// ============================================================================
// Command handlers
// ============================================================================

func cmdCompact(ctx *commandContext, args string) {
	if _, err := ctx.entry.session.RunCompaction(context.Background(), args); err != nil {
		ctx.sendMessage(fmt.Sprintf("Compaction failed: %v", err))
		return
	}
	if ctx.entry.session.HasPendingWork() {
		go func() { _ = ctx.entry.session.Agent.Continue() }()
		ctx.sendMessage("Session compacted successfully. Resuming.")
	} else {
		ctx.sendMessage("Session compacted successfully.")
	}
}

func cmdResume(ctx *commandContext, args string) {
	if args == "" {
		entry := ctx.entry
		sessionDir := store.DefaultSessionDir(entry.agentDir, entry.cwd)
		sessions, _ := store.ListSessions(entry.cwd, sessionDir)
		if len(sessions) > 10 {
			sessions = sessions[:10]
		}
		entry.resumeMu.Lock()
		entry.lastResumeList = sessions
		entry.resumeMu.Unlock()
		var lines []string
		for i, s := range sessions {
			name := s.Name
			if name == "" {
				name = s.FirstMessage
			}
			if name == "" {
				name = "(unnamed)"
			}
			lines = append(lines, fmt.Sprintf("%d. %s (%s)", i+1, name, s.Path))
		}
		ctx.sendMessage(fmt.Sprintf("Available sessions (top 10):\n%s\n\nTo resume: /resume <number> or /resume <path>", strings.Join(lines, "\n")))
	} else {
		ctx.agent.handleResumeArg(ctx.sessionID, ctx.entry, args)
	}
}

func cmdContinue(ctx *commandContext, _ string) {
	entry := ctx.entry
	sessionDir := store.DefaultSessionDir(entry.agentDir, entry.cwd)
	sessions, _ := store.ListSessions(entry.cwd, sessionDir)
	if len(sessions) == 0 {
		ctx.sendMessage("No sessions available to continue.")
		return
	}
	if !isValidSessionPath(sessions[0].Path, entry.agentDir) {
		ctx.sendMessage("Invalid session path: must be within sessions directory")
		return
	}
	sessionPath := sessions[0].Path
	forked, err := entry.session.SwitchSession(sessionPath)
	if err != nil {
		ctx.sendMessage(fmt.Sprintf("Failed to continue session: %v", err))
		return
	}
	if forked {
		ctx.sendMessage("Session is active in another window — branched with history preserved.")
	}
	name := sessions[0].Name
	if name == "" {
		name = sessions[0].FirstMessage
	}
	if name == "" {
		name = sessions[0].Path
	}
	ctx.sendMessage(fmt.Sprintf("Continued session: %s", name))
	ctx.agent.replaySessionHistory(ctx.sessionID, entry)
}

func cmdSession(ctx *commandContext, _ string) {
	entry := ctx.entry
	stats := entry.session.GetSessionStats()
	name := entry.session.SessionStore.GetSessionName()
	info := "**Session Info**\n\n"
	info += fmt.Sprintf("- **Version:** %s\n", version)
	info += "- **Mode:** acp\n"
	if bin, err := os.Executable(); err == nil {
		info += fmt.Sprintf("- **Binary:** %s\n", bin)
	}
	if name != "" {
		info += fmt.Sprintf("- **Name:** %s\n", name)
	}
	info += fmt.Sprintf("- **ID:** %s\n", stats.SessionID)
	if model := entry.session.Model(); model != nil {
		info += fmt.Sprintf("- **Model:** %s\n", model.ID)
		info += fmt.Sprintf("- **Provider:** %s\n", model.Provider)
		if cu := entry.session.GetContextUsage(); cu != nil {
			info += fmt.Sprintf("- **Context:** %s\n", session.FormatContextUsage(cu, entry.session.CompactMode()))
		}
	}
	if entry.extSetup != nil && entry.extSetup.Manager != nil {
		enabled := entry.extSetup.Manager.EnabledExtensionNames()
		if len(enabled) > 0 {
			info += fmt.Sprintf("- **Extensions:** %s\n", strings.Join(enabled, ", "))
		}
	}
	if entry.mcpManager != nil {
		statuses := entry.mcpManager.Status()
		if len(statuses) > 0 {
			info += "\n**MCP Servers**\n\n"
			for _, s := range statuses {
				info += fmt.Sprintf("- %s: %s\n", s.Name, s.StatusString())
			}
		}
	}
	// Tools (exclude MCP tools — those are shown via /mcp)
	if tools := entry.session.Agent.State().Tools; tools != nil && tools.Len() > 0 {
		var extToolNames map[string][]string
		if entry.extSetup != nil && entry.extSetup.Manager != nil {
			extToolNames = entry.extSetup.Manager.ExtensionToolNames()
		}
		cls := tools.ClassifyTools(extToolNames)
		if len(cls.Builtin) > 0 || len(cls.Extensions) > 0 {
			info += "\n**Tools**\n\n"
			if len(cls.Builtin) > 0 {
				info += fmt.Sprintf("- **Built-in:** %s\n", strings.Join(cls.Builtin, ", "))
			}
			extNames := make([]string, 0, len(cls.Extensions))
			for ext := range cls.Extensions {
				extNames = append(extNames, ext)
			}
			sort.Strings(extNames)
			for _, ext := range extNames {
				info += fmt.Sprintf("- **%s:** %s\n", ext, strings.Join(cls.Extensions[ext], ", "))
			}
		}
	}

	// Paths (for debugging)
	{
		var pathLines []string
		if entry.extSetup != nil && entry.extSetup.Manager != nil {
			paths := entry.extSetup.Manager.Paths()
			if paths.SDKDir != "" {
				pathLines = append(pathLines, fmt.Sprintf("- **SDK:** %s\n", paths.SDKDir))
			}
		}
		if dir := resources.BuiltinSkillsDir(); dir != "" {
			pathLines = append(pathLines, fmt.Sprintf("- **Skills:** %s\n", dir))
		}
		if len(pathLines) > 0 {
			info += "\n**Paths**\n\n"
			for _, l := range pathLines {
				info += l
			}
		}
	}

	info += "\n**Messages**\n\n"
	info += fmt.Sprintf("- **User:** %d\n", stats.UserMessages)
	info += fmt.Sprintf("- **Assistant:** %d\n", stats.AssistantMessages)
	info += fmt.Sprintf("- **Tool Calls:** %d\n", stats.ToolCalls)
	info += fmt.Sprintf("- **Total:** %d\n", stats.TotalMessages)
	info += "\n**Tokens**\n\n"
	info += fmt.Sprintf("- **Input:** %d\n", stats.Tokens.Input)
	info += fmt.Sprintf("- **Output:** %d\n", stats.Tokens.Output)
	info += fmt.Sprintf("- **Total:** %d\n", stats.Tokens.Total)
	if stats.Cost > 0 {
		info += fmt.Sprintf("\n**Cost:** $%.4f\n", stats.Cost)
	}
	ctx.sendMessage(info)
}

func cmdHelp(ctx *commandContext, _ string) {
	ctx.sendMessage(slashcmd.HelpText(slashcmd.ACP))
}

func cmdChangelog(ctx *commandContext, args string) {
	entries := session.GetChangelogEntries()
	if len(entries) == 0 {
		ctx.sendMessage("No changelog entries found.")
		return
	}
	entries, footer, err := session.SelectChangelogEntries(entries, args)
	if err != nil {
		ctx.sendMessage(err.Error())
		return
	}
	var texts []string
	for i := len(entries) - 1; i >= 0; i-- {
		texts = append(texts, entries[i].Content)
	}
	msg := "**What's New**\n\n" + strings.Join(texts, "\n\n")
	if footer != "" {
		msg += "\n\n*" + footer + "*"
	}
	ctx.sendMessage(msg)
}

func cmdReload(ctx *commandContext, _ string) {
	entry := ctx.entry
	if err := entry.session.Reload(); err != nil {
		ctx.sendMessage(fmt.Sprintf("Reload failed: %v", err))
		return
	}
	if entry.extSetup != nil {
		if entry.extSetup.Manager != nil {
			entry.extSetup.Manager.SetAllowedNames(resolveEnabledExtensions(ctx.agent.options.EnabledExtensions, entry.settingsManager))
		}
		_ = entry.extSetup.Reload(context.Background())
	}
	// Reload MCP servers: re-read configs from disk and apply the diff.
	// If no manager existed (no MCPs at startup), create one now if configs appear.
	if err := session.ReloadMCP(context.Background(), &entry.mcpManager, entry.session, entry.cwd, ctx.agent.options.MCPConfig, nil); err != nil {
		ctx.sendMessage(fmt.Sprintf("MCP reload failed: %v", err))
	}
	entry.mcpStatus = mcp.StatusFunc(entry.mcpManager)
	// Re-read provider credentials and the model catalog from disk (auth.json,
	// models.json + models.d/ fragments) so providers authenticated after
	// startup and newly added models become visible to subsequent set_model /
	// new sessions without restarting the agent process.
	if entry.modelRegistry != nil {
		entry.modelRegistry.Refresh()
	}
	ctx.agent.sendAvailableCommands(ctx.sessionID)
	ctx.sendMessage("Reload completed successfully.")
}

// ============================================================================
// Command helpers
// ============================================================================

func (pa *firAgent) handleResumeArg(sessionID string, entry *firSession, args string) {
	args, at := splitAtFlag(args)
	if at == "" && strings.Contains(" "+args+" ", " --at ") {
		pa.sendAgentMessage(sessionID, "--at requires an entry id")
		return
	}
	var sessionPath string
	if n := parseInt(args); n > 0 {
		entry.resumeMu.Lock()
		list := entry.lastResumeList
		entry.resumeMu.Unlock()
		if n <= len(list) {
			sessionPath = list[n-1].Path
		} else {
			hint := "Run /resume first to see available sessions."
			if len(list) > 0 {
				hint = fmt.Sprintf("Pick 1-%d, or run /resume to refresh the list.", len(list))
			}
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Invalid session number: %s. %s", args, hint))
			return
		}
	} else {
		sessionPath, _ = filepath.Abs(args)
	}

	if !isValidSessionPath(sessionPath, entry.agentDir) {
		pa.sendAgentMessage(sessionID, "Invalid session path: must be within sessions directory")
		return
	}

	if at != "" {
		child, _, err := forkSessionAt(sessionPath, at, entry.cwd, store.DefaultSessionDir(entry.agentDir, entry.cwd))
		if err != nil {
			pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to fork session: %v", err))
			return
		}
		sessionPath = child
	}

	forked, err := entry.session.SwitchSession(sessionPath)
	if err != nil {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to resume session: %v", err))
	} else {
		if forked {
			pa.sendAgentMessage(sessionID, "Session is active in another window — branched with history preserved.")
		}
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Resumed session: %s", sessionPath))
		pa.replaySessionHistory(sessionID, entry)
	}
}

// splitAtFlag extracts an "--at <entry-id>" option from /resume arguments,
// returning the remaining argument text and the entry id.
func splitAtFlag(args string) (rest, at string) {
	fields := strings.Fields(args)
	var keep []string
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--at" && i+1 < len(fields) {
			at = fields[i+1]
			i++
			continue
		}
		keep = append(keep, fields[i])
	}
	return strings.Join(keep, " "), at
}
