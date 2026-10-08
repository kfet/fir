package builtin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/slashcmd"
)

var errNoMCP = errors.New("no MCP servers configured")

// loginTimeout bounds every interactive OAuth flow.
const loginTimeout = 5 * time.Minute

// providerIDRegex validates provider IDs typed by the user.
var providerIDRegex = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

const noSession = "No session available."

// ============================================================================
// /mcp
// ============================================================================

func cmdMCP(c *slashcmd.Ctx, args string) {
	h := host(c)
	var m MCP
	if h != nil {
		m = h.MCP()
	}
	if m == nil {
		c.Out.Warn("No MCP servers configured.")
		return
	}
	details := m.Details()
	if len(details) == 0 {
		c.Out.Status("No MCP servers configured.")
		return
	}
	view, _ := c.Out.(MCPView)
	if args == "" {
		if view != nil {
			view.ShowMCPSummary(details)
		} else {
			c.Out.Message(renderMCPSummary(details))
		}
		return
	}
	for _, d := range details {
		if d.Name == args {
			if view != nil {
				view.ShowMCPDetail(d)
			} else {
				c.Out.Message(renderMCPDetail(d))
			}
			return
		}
	}
	c.Out.Warn(fmt.Sprintf("MCP server %q not found.", args))
}

func transportOf(d mcp.ServerDetail) string {
	if d.Config.Transport == "" {
		return "stdio"
	}
	return d.Config.Transport
}

func renderMCPSummary(details []mcp.ServerDetail) string {
	var sb strings.Builder
	sb.WriteString("**MCP Servers**\n\n")
	for _, d := range details {
		fmt.Fprintf(&sb, "- **%s** (%s) — %s, %d tools", d.Name, d.Status, transportOf(d), len(d.Tools))
		if d.Error != "" {
			fmt.Fprintf(&sb, " — error: %s", d.Error)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nUse `/mcp <server-name>` to see full tool details.")
	sb.WriteString("\nUse `/mcp login <server-name>` to authenticate a remote server.")
	return sb.String()
}

func renderMCPDetail(d mcp.ServerDetail) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "### %s (%s)\n\n", d.Name, d.Status)
	if d.Error != "" {
		fmt.Fprintf(&sb, "- **Error:** %s\n", d.Error)
	}
	fmt.Fprintf(&sb, "- **Transport:** %s\n", transportOf(d))
	if d.Config.Command != "" {
		cmd := d.Config.Command
		if len(d.Config.Args) > 0 {
			cmd += " " + strings.Join(d.Config.Args, " ")
		}
		fmt.Fprintf(&sb, "- **Command:** `%s`\n", cmd)
	}
	if d.Config.URL != "" {
		fmt.Fprintf(&sb, "- **URL:** %s\n", d.Config.URL)
	}
	if caps := Capabilities(d); len(caps) > 0 {
		fmt.Fprintf(&sb, "- **Capabilities:** %s\n", strings.Join(caps, ", "))
	}
	if len(d.Tools) > 0 {
		fmt.Fprintf(&sb, "\n**Tools (%d):**\n\n", len(d.Tools))
		for _, tool := range d.Tools {
			if tool.Description != "" {
				fmt.Fprintf(&sb, "- `%s` — %s\n", tool.Name, tool.Description)
			} else {
				fmt.Fprintf(&sb, "- `%s`\n", tool.Name)
			}
		}
	} else {
		sb.WriteString("- **Tools:** none\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Capabilities lists the optional MCP capabilities a server advertises.
func Capabilities(d mcp.ServerDetail) []string {
	var caps []string
	if d.HasResources {
		caps = append(caps, "resources")
	}
	if d.HasPrompts {
		caps = append(caps, "prompts")
	}
	return caps
}

// cmdMCPReload performs an MCP-only reload and reports config collisions.
func cmdMCPReload(c *slashcmd.Ctx, _ string) {
	h := host(c)
	s := sess(c)
	if s == nil {
		c.Out.Warn(noSession)
		return
	}
	if s.IsStreaming() {
		c.Out.Warn("Wait for the current response to finish before reloading.")
		return
	}
	m := h.MCP()
	if m == nil {
		c.Out.Warn("MCP reload not available.")
		return
	}
	_, collisions, loadErr := mcp.LoadDefaultConfigsReport(h.Cwd())
	if err := m.Reload(c.Context); err != nil {
		c.Out.Warn(fmt.Sprintf("MCP reload failed: %v", err))
		return
	}
	var sb strings.Builder
	if loadErr != nil {
		fmt.Fprintf(&sb, "MCP config warning: %v\nMCP servers reloaded.", loadErr)
	} else {
		sb.WriteString("MCP servers reloaded.")
	}
	if len(collisions) > 0 {
		sb.WriteString("\n\nCollisions detected:")
		for _, col := range collisions {
			fmt.Fprintf(&sb, "\n- %s — loaded from %s, shadows %s",
				col.Server, col.WonFile, strings.Join(col.ShadowedFiles, ", "))
		}
	}
	if loadErr != nil || len(collisions) > 0 {
		c.Out.Warn(sb.String())
		return
	}
	c.Out.Status(sb.String())
}

// cmdMCPLogin runs the OAuth login flow for one remote MCP server. fir never
// starts an MCP login on its own (servers connect in the background, where
// prompting is neither safe nor welcome); a server that needs credentials
// reports this command instead.
func cmdMCPLogin(c *slashcmd.Ctx, server string) {
	if server == "" {
		c.Out.Warn("Usage: /mcp login <server>")
		return
	}
	m := mcpOf(c)
	if m == nil {
		c.Out.Warn("No MCP servers configured.")
		return
	}
	c.Async(func() {
		ctx, cancel := context.WithTimeout(c.Context, loginTimeout)
		defer cancel()
		if err := m.Login(ctx, server, c.Out.LoginCallbacks()); err != nil {
			if errors.Is(err, errNoMCP) {
				c.Out.Warn("No MCP servers configured.")
			} else if ctx.Err() == context.DeadlineExceeded {
				c.Out.Warn("MCP login timed out after 5 minutes.")
			} else {
				c.Out.Warn(fmt.Sprintf("MCP login failed: %v", err))
			}
			return
		}
		c.Out.Status(fmt.Sprintf("Logged in to MCP server %q. Credentials saved.", server))
	})
}

func cmdMCPLogout(c *slashcmd.Ctx, server string) {
	if server == "" {
		c.Out.Warn("Usage: /mcp logout <server>")
		return
	}
	m := mcpOf(c)
	if m == nil {
		c.Out.Warn("No MCP servers configured.")
		return
	}
	if err := m.Logout(server); err != nil {
		if errors.Is(err, errNoMCP) {
			c.Out.Warn("No MCP servers configured.")
			return
		}
		c.Out.Warn(fmt.Sprintf("MCP logout failed: %v", err))
		return
	}
	c.Out.Status(fmt.Sprintf("Removed stored credentials for MCP server %q.", server))
}

func mcpOf(c *slashcmd.Ctx) MCP {
	if h := host(c); h != nil {
		return h.MCP()
	}
	return nil
}

// ============================================================================
// /login, /logout
// ============================================================================

func authOf(c *slashcmd.Ctx) Auth {
	if h := host(c); h != nil {
		return h.Auth()
	}
	return nil
}

func isOAuth(a Auth, id string) bool {
	cred := a.Credential(id)
	return cred != nil && cred.Type == auth.CredentialTypeOAuth
}

func providerName(a Auth, id string) string {
	for _, p := range a.OAuthProviders() {
		if p.ID() == id {
			return p.Name()
		}
	}
	return id
}

func cmdLogin(c *slashcmd.Ctx, args string) {
	a := authOf(c)
	if a == nil {
		c.Out.Warn("Model registry not available.")
		return
	}
	providers := a.OAuthProviders()
	if len(providers) == 0 {
		c.Out.Warn("No OAuth providers available.")
		return
	}
	if args == "" {
		choices := make([]Choice, len(providers))
		for i, p := range providers {
			desc := ""
			if isOAuth(a, p.ID()) {
				desc = "logged in"
			}
			choices[i] = Choice{ID: p.ID(), Label: p.Name(), Description: desc}
		}
		if pk, ok := c.Out.(Picker); ok {
			pk.Pick(choices, func(id string) { LoginProvider(c, id) })
			return
		}
		c.Out.Message(listChoices("Available OAuth providers:", choices, "To login, run: /login <provider-id>"))
		return
	}
	LoginProvider(c, args)
}

// LoginProvider validates id and runs its OAuth login flow via c.Async.
func LoginProvider(c *slashcmd.Ctx, id string) {
	a := authOf(c)
	if a == nil {
		c.Out.Warn("Model registry not available.")
		return
	}
	if !providerIDRegex.MatchString(id) {
		c.Out.Warn(fmt.Sprintf("Invalid provider ID: %s", id))
		return
	}
	found := false
	for _, p := range a.OAuthProviders() {
		if p.ID() == id {
			found = true
			break
		}
	}
	if !found {
		c.Out.Warn(fmt.Sprintf("Provider not found: %s", id))
		return
	}
	name := providerName(a, id)
	c.Async(func() {
		ctx, cancel := context.WithTimeout(c.Context, loginTimeout)
		defer cancel()
		if err := a.Login(ctx, id, c.Out.LoginCallbacks()); err != nil {
			switch {
			case ctx.Err() == context.DeadlineExceeded:
				c.Out.Warn("Login timed out after 5 minutes.")
			case err.Error() == "Login cancelled":
				// The user dismissed the flow; nothing to report.
			default:
				c.Out.Warn(fmt.Sprintf("Failed to login to %s: %v", name, err))
			}
			return
		}
		a.Refresh()
		c.Out.Status(fmt.Sprintf("Logged in to %s. Credentials saved.", name))
	})
}

func cmdLogout(c *slashcmd.Ctx, args string) {
	a := authOf(c)
	if a == nil {
		c.Out.Warn("Model registry not available.")
		return
	}
	var loggedIn []string
	for _, id := range a.List() {
		if !auth.IsMCPKey(id) {
			loggedIn = append(loggedIn, id)
		}
	}
	sort.Strings(loggedIn)
	if len(loggedIn) == 0 {
		c.Out.Status("No providers currently logged in.")
		return
	}
	switch args {
	case "":
		choices := make([]Choice, len(loggedIn))
		for i, id := range loggedIn {
			desc := "logged in"
			if cred := a.Credential(id); cred != nil && cred.Type != "" {
				desc = "logged in (" + string(cred.Type) + ")"
			}
			choices[i] = Choice{ID: id, Label: providerName(a, id), Description: desc}
		}
		if pk, ok := c.Out.(Picker); ok {
			pk.Pick(choices, func(id string) { logoutOne(c, a, id) })
			return
		}
		c.Out.Message(listChoices("Logged in providers:", choices, "To logout: /logout <provider-id> or /logout all"))
	case "all":
		for _, id := range loggedIn {
			if err := a.Logout(id); err != nil {
				c.Out.Warn(fmt.Sprintf("Logout failed: %v", err))
				return
			}
		}
		a.Refresh()
		c.Out.Status("Logged out from all providers.")
	default:
		if !providerIDRegex.MatchString(args) {
			c.Out.Warn(fmt.Sprintf("Invalid provider ID: %s", args))
			return
		}
		for _, id := range loggedIn {
			if id == args {
				logoutOne(c, a, id)
				return
			}
		}
		c.Out.Warn(fmt.Sprintf("Provider not logged in: %s", args))
	}
}

func logoutOne(c *slashcmd.Ctx, a Auth, id string) {
	if err := a.Logout(id); err != nil {
		c.Out.Warn(fmt.Sprintf("Logout failed: %v", err))
		return
	}
	a.Refresh()
	c.Out.Status(fmt.Sprintf("Logged out from %s.", providerName(a, id)))
}

func listChoices(title string, choices []Choice, hint string) string {
	lines := []string{title}
	for _, ch := range choices {
		line := "- " + ch.ID
		if ch.Description != "" {
			line += " (" + ch.Description + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n\n" + hint
}
