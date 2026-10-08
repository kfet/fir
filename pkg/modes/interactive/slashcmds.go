// slashcmds.go — TUI adapter for the shared slash-command registry.
package interactive

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/slashcmd"
	"github.com/kfet/fir/pkg/slashcmd/builtin"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
	"github.com/kfet/pinoauth"
	"github.com/kfet/tui"
)

// tuiCommands is the TUI slash-command registry. Handlers receive the mode
// through slashcmd.Ctx.Host, so one registry serves every InteractiveMode.
var tuiCommands = newCommandRegistry()

// CommandRegistry returns the TUI slash-command registry (for parity tests).
func CommandRegistry() *slashcmd.Registry { return tuiCommands }

// tuiOnly adapts a TUI-specific handler to slashcmd.Handler.
func tuiOnly(fn func(m *InteractiveMode, args string)) slashcmd.Handler {
	return func(c *slashcmd.Ctx, args string) { fn(c.Host.(*tuiCmdHost).m, args) }
}

// newCommandRegistry binds the shared builtin handlers plus the TUI-specific
// ones. Every command declared for the TUI in slashcmd.Specs must be bound
// here; TestCommandParity enforces it.
func newCommandRegistry() *slashcmd.Registry {
	r := slashcmd.NewRegistry(slashcmd.TUI)
	builtin.Register(r)
	r.Bind("help", tuiOnly(func(m *InteractiveMode, _ string) { m.toggleHelpVisibility() }))
	r.Bind("new", tuiOnly(func(m *InteractiveMode, args string) { go m.handleClearCommand(args, "") }))
	r.Bind("compact", tuiOnly(func(m *InteractiveMode, args string) { go m.handleCompactCommand(args) }))
	r.Bind("model", tuiOnly(func(m *InteractiveMode, args string) { m.showModelSelector(args) }))
	r.Bind("thinking", tuiOnly(func(m *InteractiveMode, _ string) { m.showThinkingSelector() }))
	r.Bind("theme", tuiOnly(func(m *InteractiveMode, _ string) { m.showThemeSelector() }))
	r.Bind("settings", tuiOnly(func(m *InteractiveMode, _ string) { m.showSettingsSelector() }))
	r.Bind("session", tuiOnly(func(m *InteractiveMode, _ string) { m.handleSessionCommand() }))
	r.Bind("resume", tuiOnly(func(m *InteractiveMode, _ string) { m.showSessionSelector() }))
	r.Bind("tree", tuiOnly(func(m *InteractiveMode, _ string) { m.showTreeSelector() }))
	r.Bind("changelog", tuiOnly((*InteractiveMode).handleChangelogCommand))
	r.Bind("reload", tuiOnly(func(m *InteractiveMode, _ string) { m.handleReloadCommand() }))
	r.Bind("reexec", tuiOnly((*InteractiveMode).handleReexecCommand))
	r.Bind("update", tuiOnly(func(m *InteractiveMode, _ string) { go m.handleUpdateCommand() }))
	r.Bind("queue", tuiOnly(func(m *InteractiveMode, _ string) { m.handleQueueCommand() }))
	r.Bind("dequeue", tuiOnly(func(m *InteractiveMode, args string) {
		arg, _, _ := strings.Cut(args, " ")
		m.handleDequeueCommand(arg)
	}))
	r.Bind("plan", tuiOnly(func(m *InteractiveMode, _ string) { m.handlePlanCommand() }))
	r.Bind("quit", tuiOnly(func(m *InteractiveMode, _ string) { m.Shutdown() }))
	return r
}

// tuiCmdHost is the TUI's slashcmd Output and builtin.Host.
type tuiCmdHost struct{ m *InteractiveMode }

func (m *InteractiveMode) cmdCtx() *slashcmd.Ctx {
	h := &tuiCmdHost{m: m}
	return &slashcmd.Ctx{Context: m.ctx, Out: h, Host: h}
}

// slashcmd.Output.
func (h *tuiCmdHost) Message(text string) { h.m.showMessage(text) }
func (h *tuiCmdHost) Status(text string)  { h.m.showStatus(text) }
func (h *tuiCmdHost) Warn(text string)    { h.m.showWarning(text) }
func (h *tuiCmdHost) Code(text string)    { h.m.showStatus(text) }

func (h *tuiCmdHost) LoginCallbacks() pinoauth.LoginCallbacks { return h.m.oauthLoginCallbacks() }

// builtin.Host.
func (h *tuiCmdHost) Cwd() string {
	cwd, _ := os.Getwd()
	return cwd
}

func (h *tuiCmdHost) Session() builtin.Session {
	if h.m.session == nil {
		return nil
	}
	return builtin.NewSession(h.m.session)
}

func (h *tuiCmdHost) Auth() builtin.Auth { return builtin.NewAuth(h.m.session) }

func (h *tuiCmdHost) MCP() builtin.MCP {
	m := h.m
	if m.mcpDetails == nil && m.mcpReload == nil && m.mcpLogin == nil && m.mcpLogout == nil {
		return nil
	}
	f := builtin.FuncMCP{DetailsFn: m.mcpDetails, LoginFn: m.mcpLogin, LogoutFn: m.mcpLogout}
	if m.mcpReload != nil {
		f.ReloadFn = func(context.Context) error { return m.mcpReload() }
	}
	return f
}

// ResourcesReloaded refreshes autocomplete after skills changed on disk.
func (h *tuiCmdHost) ResourcesReloaded() { h.m.setupAutocomplete() }

// Pick shows a select list (builtin.Picker).
func (h *tuiCmdHost) Pick(choices []builtin.Choice, onPick func(id string)) {
	items := make([]tuicomp.SelectItem, len(choices))
	for i, c := range choices {
		items[i] = tuicomp.SelectItem{Label: c.Label, Value: c.ID, Description: c.Description}
	}
	h.m.showSelector(func(done func()) (tui.Component, tui.Component) {
		list := tuicomp.NewSelectList(items, 10, itheme.GetSelectListTheme())
		list.OnSelect = func(item tuicomp.SelectItem) {
			done()
			onPick(item.Value)
		}
		list.OnCancel = func() { done() }
		return list, list
	})
}

// StartProgress shows a cancellable bordered loader in place of the editor
// (builtin.Progress).
func (h *tuiCmdHost) StartProgress(label string, cancel func()) (stop func()) {
	m := h.m
	if m.ui == nil || m.editorContainer == nil {
		return func() {}
	}
	loader := components.NewBorderedLoader(m.ui.AsRenderRequester(), itheme.GetTheme(), label, nil)
	var stopped atomic.Bool
	restore := func() {
		if stopped.Swap(true) {
			return
		}
		loader.Dispose()
		m.editorContainer.Clear()
		m.editorContainer.AddChild(m.editor)
		m.ui.SetFocus(m.editor)
		m.ui.RequestRender(false)
	}
	loader.SetOnAbort(func() {
		restore()
		cancel()
	})
	m.editorContainer.Clear()
	m.editorContainer.AddChild(loader)
	m.ui.SetFocus(loader)
	m.ui.RequestRender(true)
	return restore
}

// ShowMCPSummary renders the MCP overlay (builtin.MCPView).
func (h *tuiCmdHost) ShowMCPSummary(details []mcp.ServerDetail) {
	t := itheme.GetTheme()
	lines := []string{t.Bold("MCP Servers")}
	for i := range details {
		lines = append(lines, "")
		lines = renderServerDetail(lines, &details[i], t)
		lines = append(lines, "  "+t.Fg("dim", "Tools: ")+strconv.Itoa(len(details[i].Tools)))
	}
	lines = append(lines, "",
		t.Fg("dim", "Use /mcp <server-name> to see full tool details."),
		t.Fg("dim", "Use /mcp login <server-name> to authenticate a remote server."))
	h.m.showMCPOverlay(lines)
}

// ShowMCPDetail renders one server in the MCP overlay (builtin.MCPView).
func (h *tuiCmdHost) ShowMCPDetail(d mcp.ServerDetail) {
	t := itheme.GetTheme()
	lines := renderServerDetail(nil, &d, t)
	if len(d.Tools) > 0 {
		lines = append(lines, "", t.Bold("  Tools ("+strconv.Itoa(len(d.Tools))+")"))
		for _, tool := range d.Tools {
			lines = append(lines, "    "+t.Fg("accent", tool.Name))
			if tool.Description != "" {
				lines = append(lines, "      "+t.Fg("dim", tool.Description))
			}
		}
	} else {
		lines = append(lines, "  "+t.Fg("dim", "Tools: none"))
	}
	h.m.showMCPOverlay(lines)
}
