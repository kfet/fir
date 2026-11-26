package mcp

import (
	"context"
	"time"

	"github.com/kfet/agent"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	firlog "github.com/kfet/fir/pkg/log"
)

// ElicitFn answers an MCP elicitation request.
type ElicitFn func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error)

// ElicitModes lists the elicitation modes a front end can present.
type ElicitModes struct {
	Form bool
	URL  bool
}

// elicitation is what SetElicitation installed.
type elicitation struct {
	fn    ElicitFn
	modes ElicitModes
}

// skillsListTimeout bounds the skills/list walk at connect.
var skillsListTimeout = 30 * time.Second

// SetElicitation installs the front end that answers MCP elicitation
// requests, and the modes it can present. fir advertises the elicitation
// capability only to servers that connect while one is installed (the
// capability is fixed at connect), so set it before Start. With none
// installed — headless runs — fir does not advertise elicitation and
// declines any elicitation request a server sends anyway.
func (m *Manager) SetElicitation(fn ElicitFn, modes ElicitModes) {
	if m == nil {
		return
	}
	if fn == nil || (!modes.Form && !modes.URL) {
		m.elicit.Store(nil)
		return
	}
	m.elicit.Store(&elicitation{fn: fn, modes: modes})
}

// elicitationFor returns the handler and capability fir uses for new
// connections: the installed front end, else the legacy ElicitationFn
// field (form mode), else none.
func (m *Manager) elicitationFor() (ElicitFn, *sdk.ElicitationCapabilities) {
	if e := m.elicit.Load(); e != nil {
		caps := &sdk.ElicitationCapabilities{}
		if e.modes.Form {
			caps.Form = &sdk.FormElicitationCapabilities{}
		}
		if e.modes.URL {
			caps.URL = &sdk.URLElicitationCapabilities{}
		}
		return e.fn, caps
	}
	if m.ElicitationFn != nil {
		return m.ElicitationFn, nil // the SDK infers form support
	}
	return nil, nil
}

// clientCapabilities is what fir declares to server name: the SDK default
// (roots with listChanged), the files and tasks extensions when on, and the
// elicitation modes of the installed front end.
func clientCapabilities(cfg ServerConfig, elicitCaps *sdk.ElicitationCapabilities) *sdk.ClientCapabilities {
	caps := &sdk.ClientCapabilities{RootsV2: &sdk.RootCapabilities{ListChanged: true}}
	if filesEnabled(cfg) {
		caps.AddExtension(FilesExtensionID, filesClientCaps())
	}
	if !cfg.DisableTasks {
		caps.AddExtension(TasksExtensionID, map[string]any{})
	}
	caps.Elicitation = elicitCaps
	return caps
}

// connectSkills lists the skills of a server that declares the skills
// extension and returns its catalog, or nil. Failures are logged; they never
// fail the connect.
func (m *Manager) connectSkills(ctx context.Context, name string, cfg ServerConfig, et *extTransport, session *sdk.ClientSession, caps *sdk.ServerCapabilities) *serverSkills {
	if cfg.DisableSkills || !serverDeclaresSkills(caps) {
		return nil
	}
	conn := et.current()
	if conn == nil {
		return nil
	}
	sk := newServerSkills(name, conn, func(ctx context.Context, uri string) (*sdk.ReadResourceResult, error) {
		return session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
	})
	lctx, cancel := context.WithTimeout(ctx, skillsListTimeout)
	defer cancel()
	if err := sk.list(lctx); err != nil {
		// The catalog stays empty; skill URIs from instructions still load
		// through skills/get.
		firlog.Warn("MCP skills/list failed", "server", name, "err", err)
	}
	return sk
}

// SkillsByServer returns the skills each connected, enabled server serves
// through the MCP skills extension, keyed by name; an empty list when it
// serves none. A server that is not connected is omitted.
func (m *Manager) SkillsByServer() map[string][]MCPSkill {
	out := map[string][]MCPSkill{}
	m.forEachServer(func(name string, e *serverEntry) bool {
		if e.config.Disabled || e.session == nil {
			return true
		}
		var list []MCPSkill
		if e.skills != nil {
			list = e.skills.skills()
		}
		out[name] = list
		return true
	})
	return out
}

// serverExtraTools returns the tools fir adds for a server's capabilities:
// resource and prompt access, and the skill loader.
func (m *Manager) serverExtraTools(name string, session *sdk.ClientSession, caps *sdk.ServerCapabilities, sk *serverSkills) []agent.AgentTool {
	var tools []agent.AgentTool
	if caps != nil && caps.Resources != nil {
		tools = append(tools,
			listResourcesTool(session, name),
			readResourceTool(session, name, m.subscribeOnce(session, name)),
		)
	}
	if caps != nil && caps.Prompts != nil {
		tools = append(tools,
			listPromptsTool(session, name),
			getPromptTool(session, name),
		)
	}
	if sk != nil {
		tools = append(tools, readSkillTool(sk))
	}
	return tools
}
