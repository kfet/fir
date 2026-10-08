// Package builtin implements the slash commands whose logic is shared by every
// fir mode. Handlers reach the session, credentials and MCP servers only
// through the narrow Host interfaces below, and the user only through
// slashcmd.Output (plus optional presentation capabilities), so the TUI and
// ACP adapters run the exact same code.
package builtin

import (
	"context"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/slashcmd"
	"github.com/kfet/pinoauth"
)

// Host is the environment a mode adapter supplies as slashcmd.Ctx.Host.
// Each accessor may return nil when the capability is unavailable.
type Host interface {
	Cwd() string
	Session() Session
	Auth() Auth
	MCP() MCP
}

// Session is the subset of the agent session the shared commands use.
type Session interface {
	SessionName() string
	SetSessionName(name string)
	Skills() []resources.Skill
	Reload() error
	ExportToHTML(path string) (string, error)
	SectionsText() string
	IsStreaming() bool
}

// Auth manages provider credentials.
type Auth interface {
	OAuthProviders() []ai.OAuthProvider
	Credential(id string) *auth.AuthCredential
	List() []string
	Login(ctx context.Context, id string, cb pinoauth.LoginCallbacks) error
	Logout(id string) error
	// Refresh re-reads credentials and the model catalog after a change.
	Refresh()
}

// MCP manages MCP servers.
type MCP interface {
	Details() []mcp.ServerDetail
	Reload(ctx context.Context) error
	Login(ctx context.Context, server string, cb pinoauth.LoginCallbacks) error
	Logout(server string) error
}

// ResourcesReloadedHook is an optional Host capability, called after a
// command reloaded skills/resources (e.g. /skills install), so the mode can
// refresh autocomplete or the advertised command list.
type ResourcesReloadedHook interface {
	ResourcesReloaded()
}

// Choice is one entry offered by a Picker.
type Choice struct {
	ID          string
	Label       string
	Description string
}

// Picker is an optional Output capability: present choices interactively and
// call onPick with the chosen ID. Without it, handlers print the choices and
// the command usage instead.
type Picker interface {
	Pick(choices []Choice, onPick func(id string))
}

// Progress is an optional Output capability: show a cancellable busy
// indicator. cancel is invoked if the user aborts; the returned stop removes
// the indicator.
type Progress interface {
	StartProgress(label string, cancel func()) (stop func())
}

// MCPView is an optional Output capability for rich MCP server rendering.
// Without it, handlers render markdown.
type MCPView interface {
	ShowMCPSummary(details []mcp.ServerDetail)
	ShowMCPDetail(d mcp.ServerDetail)
}

// Register binds every shared handler that is declared for r's mode.
func Register(r *slashcmd.Registry) {
	r.BindIfAvailable("mcp", cmdMCP)
	r.BindIfAvailable("mcp reload", cmdMCPReload)
	r.BindIfAvailable("mcp login", cmdMCPLogin)
	r.BindIfAvailable("mcp logout", cmdMCPLogout)
	r.BindIfAvailable("login", cmdLogin)
	r.BindIfAvailable("logout", cmdLogout)
	r.BindIfAvailable("name", cmdName)
	r.BindIfAvailable("sections", cmdSections)
	r.BindIfAvailable("skills", cmdSkills)
	r.BindIfAvailable("skills list", cmdSkillsList)
	r.BindIfAvailable("skills install", cmdSkillsInstall)
	r.BindIfAvailable("export", cmdExport)
	r.BindIfAvailable("share", cmdShare)
}

func host(c *slashcmd.Ctx) Host {
	h, _ := c.Host.(Host)
	return h
}

func sess(c *slashcmd.Ctx) Session {
	if h := host(c); h != nil {
		return h.Session()
	}
	return nil
}

// SessionAdapter adapts *session.AgentSession to Session.
type SessionAdapter struct{ S *session.AgentSession }

// NewSession returns a Session for s, or nil when s is nil.
func NewSession(s *session.AgentSession) Session {
	if s == nil {
		return nil
	}
	return SessionAdapter{s}
}

func (a SessionAdapter) SessionName() string                   { return a.S.SessionStore.GetSessionName() }
func (a SessionAdapter) SetSessionName(name string)            { a.S.SetSessionName(name) }
func (a SessionAdapter) Reload() error                         { return a.S.Reload() }
func (a SessionAdapter) ExportToHTML(p string) (string, error) { return a.S.ExportToHTML(p) }
func (a SessionAdapter) SectionsText() string                  { return a.S.Sections().Format() }
func (a SessionAdapter) IsStreaming() bool                     { return a.S.IsStreaming() }
func (a SessionAdapter) Skills() []resources.Skill {
	skills, _ := a.S.ResourceLoader().GetSkills()
	return skills
}

// NewAuth returns an Auth for the session's model registry, or nil.
func NewAuth(s *session.AgentSession) Auth {
	if s == nil {
		return nil
	}
	reg := s.ModelRegistryRef()
	if reg == nil || reg.AuthStorage() == nil {
		return nil
	}
	return NewAuthStorage(reg.AuthStorage(), reg.Refresh)
}

// NewAuthStorage adapts an auth store plus its registry refresh to Auth.
func NewAuthStorage(st *auth.AuthStorage, refresh func()) Auth {
	return registryAuth{st: st, refresh: refresh}
}

type registryAuth struct {
	st      *auth.AuthStorage
	refresh func()
}

func (a registryAuth) OAuthProviders() []ai.OAuthProvider        { return a.st.GetOAuthProviders() }
func (a registryAuth) Credential(id string) *auth.AuthCredential { return a.st.Get(id) }
func (a registryAuth) List() []string                            { return a.st.List() }
func (a registryAuth) Login(ctx context.Context, id string, cb pinoauth.LoginCallbacks) error {
	return a.st.Login(ctx, id, cb)
}
func (a registryAuth) Logout(id string) error { return a.st.Logout(id) }
func (a registryAuth) Refresh()               { a.refresh() }

// FuncMCP implements MCP with plain functions; nil functions report
// errNoMCP (or no servers).
type FuncMCP struct {
	DetailsFn func() []mcp.ServerDetail
	ReloadFn  func(ctx context.Context) error
	LoginFn   func(ctx context.Context, server string, cb pinoauth.LoginCallbacks) error
	LogoutFn  func(server string) error
}

func (f FuncMCP) Details() []mcp.ServerDetail {
	if f.DetailsFn == nil {
		return nil
	}
	return f.DetailsFn()
}

func (f FuncMCP) Reload(ctx context.Context) error {
	if f.ReloadFn == nil {
		return errNoMCP
	}
	return f.ReloadFn(ctx)
}

func (f FuncMCP) Login(ctx context.Context, server string, cb pinoauth.LoginCallbacks) error {
	if f.LoginFn == nil {
		return errNoMCP
	}
	return f.LoginFn(ctx, server, cb)
}

func (f FuncMCP) Logout(server string) error {
	if f.LogoutFn == nil {
		return errNoMCP
	}
	return f.LogoutFn(server)
}

// ManagerMCP adapts a late-bound *mcp.Manager (a reload may create or replace
// it) to MCP. reload performs the full reload, creating the manager if needed.
func ManagerMCP(mgr func() *mcp.Manager, reload func(ctx context.Context) error) MCP {
	return FuncMCP{
		DetailsFn: func() []mcp.ServerDetail {
			if mg := mgr(); mg != nil {
				return mg.Details()
			}
			return nil
		},
		ReloadFn: reload,
		LoginFn: func(ctx context.Context, server string, cb pinoauth.LoginCallbacks) error {
			if mg := mgr(); mg != nil {
				return mg.LoginServer(ctx, server, cb)
			}
			return errNoMCP
		},
		LogoutFn: func(server string) error {
			if mg := mgr(); mg != nil {
				return mg.LogoutServer(server)
			}
			return errNoMCP
		},
	}
}
