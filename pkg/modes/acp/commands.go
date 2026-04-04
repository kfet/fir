// commands.go — slash command registry and dispatch for ACP mode.
package acp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Command infrastructure
// ============================================================================

// slashCommand represents a registered slash command.
type slashCommand struct {
	Name        string
	Description string
	Handler     func(ctx *commandContext, args string)
}

// commandContext bundles everything a command handler needs.
type commandContext struct {
	sessionID string
	entry     *firSession
	agent     *firAgent
}

// sendMessage is a convenience shorthand for sending a message to the client.
func (c *commandContext) sendMessage(msg string) {
	c.agent.sendAgentMessage(c.sessionID, msg)
}

// commandRegistry maps command names to handlers.
type commandRegistry struct {
	commands map[string]slashCommand
	order    []string
}

func newCommandRegistry() *commandRegistry {
	r := &commandRegistry{commands: make(map[string]slashCommand)}
	r.register(slashCommand{"compact", "Compact the session history to save tokens", cmdCompact})
	r.register(slashCommand{"resume", "List or resume a session (usage: /resume [number|path])", cmdResume})
	r.register(slashCommand{"continue", "Continue the most recent session", cmdContinue})
	r.register(slashCommand{"name", "Rename the current session (usage: /name <new name>)", cmdName})
	r.register(slashCommand{"session", "Show session statistics", cmdSession})
	r.register(slashCommand{"changelog", "Show changelog", cmdChangelog})
	r.register(slashCommand{"share", "Share session as a secret GitHub Gist with a preview link", cmdShare})
	r.register(slashCommand{"export", "Export session to an HTML file (usage: /export [path])", cmdExport})
	r.register(slashCommand{"login", "Login with OAuth provider (usage: /login [provider-id])", cmdLogin})
	r.register(slashCommand{"logout", "Log out from provider (usage: /logout [provider-id|all])", cmdLogout})
	r.register(slashCommand{"reload", "Reload extensions, skills, prompts", cmdReload})
	r.register(slashCommand{"skills", "List loaded skills (or /skills install <name>)", cmdSkills})
	r.register(slashCommand{"mcp", "Show MCP servers summary, or /mcp <name> for full tool details", cmdMCP})
	return r
}

func (r *commandRegistry) register(cmd slashCommand) {
	r.commands[cmd.Name] = cmd
	r.order = append(r.order, cmd.Name)
}

func (r *commandRegistry) lookup(name string) (slashCommand, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}

// availableCommands returns the ACP command list from the registry.
func (r *commandRegistry) availableCommands() []acpsdk.AvailableCommand {
	cmds := make([]acpsdk.AvailableCommand, 0, len(r.order))
	for _, name := range r.order {
		cmd := r.commands[name]
		cmds = append(cmds, acpsdk.AvailableCommand{Name: cmd.Name, Description: cmd.Description})
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
	if cmd, ok := cmds.lookup(command); ok {
		cmd.Handler(ctx, args)
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

	// 3. Prompt templates.
	templates, _ := entry.session.ResourceLoader().GetPrompts()
	for _, t := range templates {
		if t.Name == command {
			fullCmd := "/" + command
			if args != "" {
				fullCmd += " " + args
			}
			if expanded := resources.ExpandPromptTemplate(fullCmd, templates); expanded != fullCmd {
				_ = entry.session.Prompt(expanded)
			}
			return true
		}
	}

	// 4. Skill commands.
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

func cmdName(ctx *commandContext, args string) {
	if args == "" {
		ctx.sendMessage("Usage: /name <new name>")
		return
	}
	ctx.entry.session.SessionStore.AppendSessionInfo(args)
	ctx.sendMessage(fmt.Sprintf("Session renamed to: %s", args))
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

func cmdChangelog(ctx *commandContext, _ string) {
	entries := session.GetChangelogEntries()
	if len(entries) == 0 {
		ctx.sendMessage("No changelog entries found.")
		return
	}
	var texts []string
	for i := len(entries) - 1; i >= 0; i-- {
		texts = append(texts, entries[i].Content)
	}
	ctx.sendMessage("**What's New**\n\n" + strings.Join(texts, "\n\n"))
}

func cmdShare(ctx *commandContext, _ string) {
	go ctx.agent.performShare(ctx.sessionID, ctx.entry)
}

func cmdExport(ctx *commandContext, args string) {
	go func() {
		filePath, err := ctx.entry.session.ExportToHTML(args)
		if err != nil {
			ctx.sendMessage(fmt.Sprintf("Failed to export session: %v", err))
			return
		}
		ctx.sendMessage(fmt.Sprintf("Session exported to: %s", filePath))
	}()
}

func cmdLogin(ctx *commandContext, args string) {
	entry := ctx.entry
	authStorage := entry.modelRegistry.AuthStorage()
	providers := authStorage.GetOAuthProviders()
	if len(providers) == 0 {
		ctx.sendMessage("No OAuth providers available.")
		return
	}

	if args == "" {
		var lines []string
		for _, p := range providers {
			lines = append(lines, "- "+p.ID())
		}
		ctx.sendMessage(fmt.Sprintf("Available OAuth providers:\n%s\n\nTo login, run: /login <provider-id>", strings.Join(lines, "\n")))
		return
	}

	if !providerIDRegex.MatchString(args) {
		ctx.sendMessage(fmt.Sprintf("Invalid provider ID: %s", args))
		return
	}

	var found bool
	for _, p := range providers {
		if p.ID() == args {
			found = true
			break
		}
	}
	if !found {
		ctx.sendMessage(fmt.Sprintf("Provider not found: %s", args))
		return
	}

	loginCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	err := authStorage.Login(args, oauth.LoginCallbacks{
		OnAuth: func(info oauth.AuthInfo) {
			formattedURL := session.FormatAuthURL(info.URL)
			msg := fmt.Sprintf("Open this URL to authenticate:\n%s", formattedURL)
			if info.Instructions != "" {
				msg += "\n\n" + info.Instructions
			}
			ctx.sendMessage(msg)
		},
		OnProgress: func(message string) {
			ctx.sendMessage(message)
		},
		OnPrompt: func(prompt oauth.Prompt) (string, error) {
			ctx.sendMessage(prompt.Message + " (using default)")
			return "", nil
		},
		Ctx: loginCtx,
	})
	if err != nil {
		if loginCtx.Err() != nil {
			ctx.sendMessage("Login timed out after 5 minutes.")
		} else {
			ctx.sendMessage(fmt.Sprintf("Login failed: %v", err))
		}
		return
	}
	entry.modelRegistry.Refresh()
	ctx.sendMessage(fmt.Sprintf("Successfully logged in to %s.", args))
}

func cmdLogout(ctx *commandContext, args string) {
	entry := ctx.entry
	authStorage := entry.modelRegistry.AuthStorage()
	creds := authStorage.GetAll()
	loggedIn := make([]string, 0, len(creds))
	for k := range creds {
		loggedIn = append(loggedIn, k)
	}
	sort.Strings(loggedIn)

	if len(loggedIn) == 0 {
		ctx.sendMessage("No providers currently logged in.")
		return
	}

	if args == "" {
		var lines []string
		for _, p := range loggedIn {
			lines = append(lines, "- "+p)
		}
		ctx.sendMessage(fmt.Sprintf("Logged in providers:\n%s\n\nTo logout: /logout <provider-id> or /logout all", strings.Join(lines, "\n")))
	} else if args == "all" {
		for _, p := range loggedIn {
			authStorage.Logout(p)
		}
		entry.modelRegistry.Refresh()
		ctx.sendMessage("Logged out from all providers.")
	} else {
		if !providerIDRegex.MatchString(args) {
			ctx.sendMessage(fmt.Sprintf("Invalid provider ID: %s", args))
			return
		}
		found := false
		for _, p := range loggedIn {
			if p == args {
				found = true
				break
			}
		}
		if !found {
			ctx.sendMessage(fmt.Sprintf("Provider not logged in: %s", args))
			return
		}
		authStorage.Logout(args)
		entry.modelRegistry.Refresh()
		ctx.sendMessage(fmt.Sprintf("Logged out from %s.", args))
	}
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
	if err := session.ReloadMCP(context.Background(), &entry.mcpManager, entry.session, entry.cwd, ctx.agent.options.MCPConfig); err != nil {
		ctx.sendMessage(fmt.Sprintf("MCP reload failed: %v", err))
	}
	entry.mcpStatus = mcp.StatusFunc(entry.mcpManager)
	ctx.agent.sendAvailableCommands(ctx.sessionID)
	ctx.sendMessage("Reload completed successfully.")
}

func cmdSkills(ctx *commandContext, args string) {
	parts := strings.Fields(args)
	if len(parts) == 0 || parts[0] == "list" {
		cmdSkillsList(ctx)
		return
	}
	if parts[0] == "install" {
		if len(parts) < 2 {
			ctx.sendMessage("Usage: /skills install <name> [--user] [--force]")
			return
		}
		cmdSkillsInstall(ctx, parts[1:])
		return
	}
	ctx.sendMessage(fmt.Sprintf("Unknown skills subcommand: %s. Usage: /skills [list | install <name> [--user] [--force]]", parts[0]))
}

func cmdSkillsList(ctx *commandContext) {
	skills, _ := ctx.entry.session.ResourceLoader().GetSkills()
	if len(skills) == 0 {
		ctx.sendMessage("No skills loaded.")
		return
	}
	sorted := make([]resources.Skill, len(skills))
	copy(sorted, skills)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var sb strings.Builder
	sb.WriteString("| Name | Source | Description |\n")
	sb.WriteString("|------|--------|-------------|\n")
	for _, s := range sorted {
		sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", s.Name, s.Source, s.Description))
	}
	ctx.sendMessage(strings.TrimRight(sb.String(), "\n"))
}

func cmdSkillsInstall(ctx *commandContext, parts []string) {
	name := parts[0]
	var toUser, force bool
	for _, p := range parts[1:] {
		switch p {
		case "--user":
			toUser = true
		case "--force":
			force = true
		}
	}

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
		ctx.sendMessage(fmt.Sprintf("Unknown builtin skill %q. Available: %s", name, strings.Join(available, ", ")))
		return
	}

	var targetDir string
	if toUser {
		home, _ := os.UserHomeDir()
		targetDir = filepath.Join(home, ".fir", "agent", "skills", name)
	} else {
		targetDir = filepath.Join(ctx.entry.cwd, ".fir", "skills", name)
	}

	if _, err := os.Stat(targetDir); err == nil && !force {
		ctx.sendMessage(fmt.Sprintf("Skill %q already exists at %s. Use --force to overwrite.", name, targetDir))
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
		dest := filepath.Join(targetDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := fs.ReadFile(resources.BuiltinSkillsFS, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
	if err != nil {
		ctx.sendMessage(fmt.Sprintf("Failed to install skill: %v", err))
		return
	}
	ctx.sendMessage(fmt.Sprintf("Installed skill %q to %s", name, targetDir))
}

func cmdMCP(ctx *commandContext, args string) {
	entry := ctx.entry
	if entry.mcpManager == nil {
		ctx.sendMessage("No MCP servers configured.")
		return
	}
	details := entry.mcpManager.Details()
	if len(details) == 0 {
		ctx.sendMessage("No MCP servers configured.")
		return
	}

	serverName := strings.TrimSpace(args)

	// If a server name is given, show full details for that server.
	if serverName != "" {
		var found *mcp.ServerDetail
		for i := range details {
			if details[i].Name == serverName {
				found = &details[i]
				break
			}
		}
		if found == nil {
			ctx.sendMessage(fmt.Sprintf("MCP server %q not found.", serverName))
			return
		}
		d := found
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("### %s (%s)\n\n", d.Name, d.Status))
		if d.Error != "" {
			sb.WriteString(fmt.Sprintf("- **Error:** %s\n", d.Error))
		}
		transport := d.Config.Transport
		if transport == "" {
			transport = "stdio"
		}
		sb.WriteString(fmt.Sprintf("- **Transport:** %s\n", transport))
		if d.Config.Command != "" {
			cmd := d.Config.Command
			if len(d.Config.Args) > 0 {
				cmd += " " + strings.Join(d.Config.Args, " ")
			}
			sb.WriteString(fmt.Sprintf("- **Command:** `%s`\n", cmd))
		}
		if d.Config.URL != "" {
			sb.WriteString(fmt.Sprintf("- **URL:** %s\n", d.Config.URL))
		}
		var caps []string
		if d.HasResources {
			caps = append(caps, "resources")
		}
		if d.HasPrompts {
			caps = append(caps, "prompts")
		}
		if len(caps) > 0 {
			sb.WriteString(fmt.Sprintf("- **Capabilities:** %s\n", strings.Join(caps, ", ")))
		}
		if len(d.Tools) > 0 {
			sb.WriteString(fmt.Sprintf("\n**Tools (%d):**\n\n", len(d.Tools)))
			for _, tool := range d.Tools {
				if tool.Description != "" {
					sb.WriteString(fmt.Sprintf("- `%s` — %s\n", tool.Name, tool.Description))
				} else {
					sb.WriteString(fmt.Sprintf("- `%s`\n", tool.Name))
				}
			}
		} else {
			sb.WriteString("- **Tools:** none\n")
		}
		ctx.sendMessage(strings.TrimRight(sb.String(), "\n"))
		return
	}

	// Summary view: no full tool list.
	var sb strings.Builder
	sb.WriteString("**MCP Servers**\n\n")
	for _, d := range details {
		transport := d.Config.Transport
		if transport == "" {
			transport = "stdio"
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s) — %s, %d tools", d.Name, d.Status, transport, len(d.Tools)))
		if d.Error != "" {
			sb.WriteString(fmt.Sprintf(" — error: %s", d.Error))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nUse `/mcp <server-name>` to see full tool details.")
	ctx.sendMessage(sb.String())
}

// ============================================================================
// Command helpers
// ============================================================================

func (pa *firAgent) handleResumeArg(sessionID string, entry *firSession, args string) {
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

// performShare creates a secret GitHub Gist from the session HTML export and
// sends back both the raw gist URL and a gistpreview.github.io preview link.
func (pa *firAgent) performShare(sessionID string, entry *firSession) {
	// Verify gh CLI is installed and authenticated.
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		if isNotFound(err) {
			pa.sendAgentMessage(sessionID, "GitHub CLI (gh) is not installed. Install it from https://cli.github.com/")
		} else {
			pa.sendAgentMessage(sessionID, "GitHub CLI is not logged in. Run 'gh auth login' first.")
		}
		return
	}

	// Export session to a temp HTML file.
	tmpPath, err := entry.session.ExportToHTML("")
	if err != nil {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Failed to export session: %v", err))
		return
	}
	defer os.Remove(tmpPath)

	out, err := exec.Command("gh", "gist", "create", "--public=false", tmpPath).Output()
	if err != nil {
		pa.sendAgentMessage(sessionID, "Failed to create gist. Check that 'gh' is installed and authenticated.")
		return
	}

	gistURL := strings.TrimSpace(string(out))
	if gistURL == "" {
		pa.sendAgentMessage(sessionID, "Gist created but no URL returned.")
		return
	}

	// Extract the gist ID — last path component of the URL.
	gistID := gistURL[strings.LastIndex(gistURL, "/")+1:]
	if !gistIDRegex.MatchString(gistID) {
		pa.sendAgentMessage(sessionID, fmt.Sprintf("Gist created but could not parse ID from URL: %s", gistURL))
		return
	}

	previewURL := "https://gistpreview.github.io/?" + gistID
	pa.sendAgentMessage(sessionID, fmt.Sprintf("Session shared (secret gist):\nGist: %s\nPreview: %s", gistURL, previewURL))
}
