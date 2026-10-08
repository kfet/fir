package builtin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/fir/pkg/slashcmd"
	"github.com/kfet/pinoauth"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type out struct{ lines []string }

func (o *out) Message(t string)                        { o.lines = append(o.lines, "msg:"+t) }
func (o *out) Status(t string)                         { o.lines = append(o.lines, "status:"+t) }
func (o *out) Warn(t string)                           { o.lines = append(o.lines, "warn:"+t) }
func (o *out) Code(t string)                           { o.lines = append(o.lines, "code:"+t) }
func (o *out) LoginCallbacks() pinoauth.LoginCallbacks { return pinoauth.LoginCallbacks{} }
func (o *out) last() string {
	if len(o.lines) == 0 {
		return ""
	}
	return o.lines[len(o.lines)-1]
}

// richOut adds every optional capability.
type richOut struct {
	out
	picked   []Choice
	pick     string
	summary  int
	detail   string
	progress int
}

func (o *richOut) Pick(ch []Choice, on func(string))   { o.picked = ch; on(o.pick) }
func (o *richOut) ShowMCPSummary(d []mcp.ServerDetail) { o.summary = len(d) }
func (o *richOut) ShowMCPDetail(d mcp.ServerDetail)    { o.detail = d.Name }
func (o *richOut) StartProgress(string, func()) func() { o.progress++; return func() {} }

type fakeSession struct {
	name      string
	skills    []resources.Skill
	streaming bool
	exportErr error
	exported  string
	reloads   int
}

func (s *fakeSession) SessionName() string       { return s.name }
func (s *fakeSession) SetSessionName(n string)   { s.name = n }
func (s *fakeSession) Skills() []resources.Skill { return s.skills }
func (s *fakeSession) Reload() error             { s.reloads++; return nil }
func (s *fakeSession) SectionsText() string      { return "sections!" }
func (s *fakeSession) IsStreaming() bool         { return s.streaming }
func (s *fakeSession) ExportToHTML(p string) (string, error) {
	if s.exportErr != nil {
		return "", s.exportErr
	}
	if p == "" {
		p = "/tmp/x.html"
	}
	s.exported = p
	return p, nil
}

type prov struct {
	ai.OAuthProvider
	id string
}

func (p prov) ID() string   { return p.id }
func (p prov) Name() string { return strings.ToUpper(p.id) }

type fakeAuth struct {
	providers []ai.OAuthProvider
	creds     map[string]*auth.AuthCredential
	loginErr  error
	logoutErr error
	refreshes int
}

func (a *fakeAuth) OAuthProviders() []ai.OAuthProvider        { return a.providers }
func (a *fakeAuth) Credential(id string) *auth.AuthCredential { return a.creds[id] }
func (a *fakeAuth) List() []string {
	var ids []string
	for k := range a.creds {
		ids = append(ids, k)
	}
	return ids
}
func (a *fakeAuth) Login(_ context.Context, id string, _ pinoauth.LoginCallbacks) error {
	if a.loginErr != nil {
		return a.loginErr
	}
	a.creds[id] = &auth.AuthCredential{Type: auth.CredentialTypeOAuth}
	return nil
}
func (a *fakeAuth) Logout(id string) error {
	if a.logoutErr != nil {
		return a.logoutErr
	}
	delete(a.creds, id)
	return nil
}
func (a *fakeAuth) Refresh() { a.refreshes++ }

type fakeHost struct {
	cwd      string
	sess     Session
	auth     Auth
	mcp      MCP
	reloaded int
}

func (h *fakeHost) Cwd() string        { return h.cwd }
func (h *fakeHost) Session() Session   { return h.sess }
func (h *fakeHost) Auth() Auth         { return h.auth }
func (h *fakeHost) MCP() MCP           { return h.mcp }
func (h *fakeHost) ResourcesReloaded() { h.reloaded++ }

var registry = func() *slashcmd.Registry {
	r := slashcmd.NewRegistry(slashcmd.ACP)
	Register(r)
	return r
}()

func run(t *testing.T, h Host, o slashcmd.Output, name, args string) {
	t.Helper()
	c := &slashcmd.Ctx{Out: o, Host: h, Go: func(fn func()) { fn() }}
	if !registry.Dispatch(c, name, args) {
		t.Fatalf("/%s not dispatched", name)
	}
}

func expect(t *testing.T, o *out, want string) {
	t.Helper()
	if got := o.last(); !strings.Contains(got, want) {
		t.Fatalf("got %q, want substring %q (all: %q)", got, want, o.lines)
	}
}

// ---------------------------------------------------------------------------
// /mcp
// ---------------------------------------------------------------------------

func details() []mcp.ServerDetail {
	return []mcp.ServerDetail{
		{Name: "demo", Status: "connected", HasResources: true, HasPrompts: true,
			Config: mcp.ServerConfig{Command: "srv", Args: []string{"-v"}},
			Tools:  []mcp.ToolInfo{{Name: "echo", Description: "Echo"}, {Name: "bare"}}},
		{Name: "remote", Status: "error", Error: "boom", Config: mcp.ServerConfig{Transport: "http", URL: "https://x"}},
	}
}

func TestMCPViews(t *testing.T) {
	h := &fakeHost{mcp: FuncMCP{DetailsFn: details}}
	o := &out{}
	run(t, &fakeHost{}, o, "mcp", "")
	expect(t, o, "warn:No MCP servers configured.")
	run(t, &fakeHost{mcp: FuncMCP{}}, o, "mcp", "")
	expect(t, o, "status:No MCP servers configured.")

	run(t, h, o, "mcp", "")
	expect(t, o, "- **remote** (error) — http, 0 tools — error: boom")
	run(t, h, o, "mcp", "demo")
	for _, want := range []string{"### demo (connected)", "`srv -v`", "resources, prompts", "`echo` — Echo", "- `bare`"} {
		expect(t, o, want)
	}
	run(t, h, o, "mcp", "remote")
	for _, want := range []string{"**Error:** boom", "**URL:** https://x", "**Tools:** none", "http"} {
		expect(t, o, want)
	}
	run(t, h, o, "mcp", "ghost")
	expect(t, o, `warn:MCP server "ghost" not found.`)

	ro := &richOut{}
	run(t, h, ro, "mcp", "")
	run(t, h, ro, "mcp", "remote")
	if ro.summary != 2 || ro.detail != "remote" || len(ro.lines) != 0 {
		t.Fatalf("MCPView not used: %+v", ro)
	}
}

func TestMCPReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	o := &out{}
	run(t, &fakeHost{}, o, "mcp", "reload")
	expect(t, o, "warn:No session available.")
	run(t, &fakeHost{sess: &fakeSession{streaming: true}}, o, "mcp", "reload")
	expect(t, o, "Wait for the current response")
	run(t, &fakeHost{sess: &fakeSession{}}, o, "mcp", "reload")
	expect(t, o, "warn:MCP reload not available.")

	cwd := t.TempDir()
	reloadErr := errors.New("bad")
	h := &fakeHost{cwd: cwd, sess: &fakeSession{}, mcp: FuncMCP{ReloadFn: func(context.Context) error { return reloadErr }}}
	run(t, h, o, "mcp", "reload")
	expect(t, o, "warn:MCP reload failed: bad")
	reloadErr = nil
	run(t, h, o, "mcp", "reload")
	expect(t, o, "status:MCP servers reloaded.")

	// A malformed project config is reported as a warning but still reloads.
	if err := os.MkdirAll(filepath.Join(cwd, ".fir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".fir", "mcp.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, h, o, "mcp", "reload")
	expect(t, o, "MCP config warning")
}

func TestMCPLoginLogout(t *testing.T) {
	o := &out{}
	var loginErr, logoutErr error
	m := FuncMCP{
		LoginFn:  func(context.Context, string, pinoauth.LoginCallbacks) error { return loginErr },
		LogoutFn: func(string) error { return logoutErr },
	}
	h := &fakeHost{mcp: m}
	for _, tc := range []struct {
		host Host
		args string
		want string
	}{
		{h, "login", "warn:Usage: /mcp login <server>"},
		{h, "logout", "warn:Usage: /mcp logout <server>"},
		{&fakeHost{}, "login s", "warn:No MCP servers configured."},
		{&fakeHost{}, "logout s", "warn:No MCP servers configured."},
		{&fakeHost{mcp: FuncMCP{}}, "login s", "warn:No MCP servers configured."},
		{&fakeHost{mcp: FuncMCP{}}, "logout s", "warn:No MCP servers configured."},
		{h, "login s", `status:Logged in to MCP server "s"`},
		{h, "logout s", `status:Removed stored credentials for MCP server "s".`},
	} {
		run(t, tc.host, o, "mcp", tc.args)
		expect(t, o, tc.want)
	}
	loginErr, logoutErr = errors.New("nope"), errors.New("nah")
	run(t, h, o, "mcp", "login s")
	expect(t, o, "warn:MCP login failed: nope")
	run(t, h, o, "mcp", "logout s")
	expect(t, o, "warn:MCP logout failed: nah")
}

func TestManagerMCP(t *testing.T) {
	var mgr *mcp.Manager
	m := ManagerMCP(func() *mcp.Manager { return mgr }, func(context.Context) error { return nil })
	if m.Details() != nil || !errors.Is(m.Login(context.Background(), "x", pinoauth.LoginCallbacks{}), errNoMCP) || !errors.Is(m.Logout("x"), errNoMCP) {
		t.Fatal("nil manager should report errNoMCP")
	}
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	mgr = mcp.NewManager(map[string]mcp.ServerConfig{"local": {Command: "true"}}, false)
	if len(m.Details()) != 1 {
		t.Fatal("expected one server")
	}
	if err := m.Login(context.Background(), "local", pinoauth.LoginCallbacks{}); err == nil {
		t.Fatal("stdio login should fail")
	}
	_ = m.Logout("local")
}

// ---------------------------------------------------------------------------
// /login, /logout
// ---------------------------------------------------------------------------

func newAuth() *fakeAuth {
	return &fakeAuth{
		providers: []ai.OAuthProvider{prov{id: "anthropic"}, prov{id: "openai"}},
		creds: map[string]*auth.AuthCredential{
			"openai":      {Type: auth.CredentialTypeOAuth},
			"groq":        {Type: auth.CredentialTypeAPIKey},
			"mcp:server1": {Type: auth.CredentialTypeOAuth},
		},
	}
}

func TestLogin(t *testing.T) {
	o := &out{}
	run(t, &fakeHost{}, o, "login", "")
	expect(t, o, "warn:Model registry not available.")
	run(t, &fakeHost{auth: &fakeAuth{}}, o, "login", "")
	expect(t, o, "warn:No OAuth providers available.")

	a := newAuth()
	h := &fakeHost{auth: a}
	run(t, h, o, "login", "")
	expect(t, o, "- anthropic\n- openai (logged in)\n\nTo login, run: /login <provider-id>")
	run(t, h, o, "login", "bad;id")
	expect(t, o, "warn:Invalid provider ID: bad;id")
	run(t, h, o, "login", "ghost")
	expect(t, o, "warn:Provider not found: ghost")
	run(t, h, o, "login", "anthropic")
	expect(t, o, "status:Logged in to ANTHROPIC. Credentials saved.")
	if a.refreshes != 1 {
		t.Fatal("expected refresh")
	}
	a.loginErr = errors.New("Login cancelled")
	n := len(o.lines)
	run(t, h, o, "login", "anthropic")
	if len(o.lines) != n {
		t.Fatalf("cancelled login should be silent: %q", o.lines)
	}
	a.loginErr = errors.New("denied")
	run(t, h, o, "login", "anthropic")
	expect(t, o, "warn:Failed to login to ANTHROPIC: denied")

	ro := &richOut{pick: "openai"}
	a.loginErr = nil
	run(t, h, ro, "login", "")
	if len(ro.picked) != 2 || ro.picked[1].Description != "logged in" {
		t.Fatalf("picker choices %+v", ro.picked)
	}
	expect(t, &ro.out, "status:Logged in to OPENAI")
}

func TestLoginTimeout(t *testing.T) {
	a := newAuth()
	a.loginErr = errors.New("ctx")
	o := &out{}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()
	c := &slashcmd.Ctx{Context: ctx, Out: o, Host: &fakeHost{auth: a}, Go: func(fn func()) { fn() }}
	registry.Dispatch(c, "login", "anthropic")
	expect(t, o, "Login timed out after 5 minutes.")
	c.Host = &fakeHost{mcp: FuncMCP{LoginFn: func(context.Context, string, pinoauth.LoginCallbacks) error { return errors.New("x") }}}
	registry.Dispatch(c, "mcp", "login s")
	expect(t, o, "MCP login timed out after 5 minutes.")
}

func TestLogout(t *testing.T) {
	o := &out{}
	run(t, &fakeHost{}, o, "logout", "")
	expect(t, o, "warn:Model registry not available.")
	run(t, &fakeHost{auth: &fakeAuth{}}, o, "logout", "")
	expect(t, o, "status:No providers currently logged in.")

	a := newAuth()
	h := &fakeHost{auth: a}
	run(t, h, o, "logout", "")
	expect(t, o, "- groq (logged in (api_key))\n- openai (logged in (oauth))")
	if strings.Contains(o.last(), "mcp:") {
		t.Fatal("MCP credentials must not be listed")
	}
	run(t, h, o, "logout", "x;y")
	expect(t, o, "warn:Invalid provider ID")
	run(t, h, o, "logout", "anthropic")
	expect(t, o, "warn:Provider not logged in: anthropic")
	run(t, h, o, "logout", "openai")
	expect(t, o, "status:Logged out from OPENAI.")

	a.logoutErr = errors.New("io")
	run(t, h, o, "logout", "groq")
	expect(t, o, "warn:Logout failed: io")
	run(t, h, o, "logout", "all")
	expect(t, o, "warn:Logout failed: io")
	a.logoutErr = nil

	ro := &richOut{pick: "groq"}
	run(t, h, ro, "logout", "")
	expect(t, &ro.out, "status:Logged out from groq.")

	a = newAuth()
	run(t, &fakeHost{auth: a}, o, "logout", "all")
	expect(t, o, "status:Logged out from all providers.")
	if len(a.creds) != 1 {
		t.Fatalf("only the MCP credential should remain: %v", a.creds)
	}
}

// ---------------------------------------------------------------------------
// Session commands
// ---------------------------------------------------------------------------

func TestNoSessionCommands(t *testing.T) {
	for _, cmd := range [][2]string{{"name", "x"}, {"sections", ""}, {"export", ""}, {"share", ""}, {"skills", ""}, {"skills", "foo"}} {
		o := &out{}
		run(t, &fakeHost{}, o, cmd[0], cmd[1])
		expect(t, o, "warn:No session available.")
	}
}

func TestNameSectionsExport(t *testing.T) {
	s := &fakeSession{}
	h := &fakeHost{sess: s}
	o := &out{}
	run(t, h, o, "name", "")
	expect(t, o, "warn:Usage: /name <name>")
	run(t, h, o, "name", "my session")
	expect(t, o, "msg:Session name set: my session")
	run(t, h, o, "name", "")
	expect(t, o, "msg:Session name: my session")
	run(t, h, o, "sections", "")
	expect(t, o, "code:sections!")
	run(t, h, o, "export", "/out.html")
	expect(t, o, "status:Session exported to: /out.html")
	s.exportErr = errors.New("disk")
	run(t, h, o, "export", "")
	expect(t, o, "warn:Failed to export session: disk")
}

func TestShare(t *testing.T) {
	s := &fakeSession{}
	h := &fakeHost{sess: s}
	var gistOut string
	var authErr, gistErr error
	orig := runCommand
	t.Cleanup(func() { runCommand = orig })
	runCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "auth" {
			return nil, authErr
		}
		return []byte(gistOut), gistErr
	}
	o := &out{}

	authErr = &exec.ExitError{}
	run(t, h, o, "share", "")
	expect(t, o, "not logged in")
	authErr = exec.ErrNotFound
	run(t, h, o, "share", "")
	expect(t, o, "not installed")
	authErr = nil

	s.exportErr = errors.New("x")
	run(t, h, o, "share", "")
	expect(t, o, "Failed to export session")
	s.exportErr = nil

	gistErr = errors.New("net")
	run(t, h, o, "share", "")
	expect(t, o, "Failed to create gist")
	gistErr = nil
	run(t, h, o, "share", "")
	expect(t, o, "no URL returned")
	gistOut = "https://gist.github.com/u/zzz"
	run(t, h, o, "share", "")
	expect(t, o, "could not parse ID")

	ro := &richOut{}
	gistOut = "https://gist.github.com/u/d168778e8e62f65886000f3f314d63e3\n"
	run(t, h, ro, "share", "")
	expect(t, &ro.out, "Preview: https://gistpreview.github.io/?d168778e8e62f65886000f3f314d63e3")
	if ro.progress != 1 {
		t.Fatal("progress capability not used")
	}

	// Cancelling via the progress indicator reports cancellation.
	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "gist" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}
	co := &cancelOut{}
	run(t, h, co, "share", "")
	expect(t, &co.out, "status:Share cancelled.")
}

type cancelOut struct{ out }

func (o *cancelOut) StartProgress(_ string, cancel func()) func() { cancel(); return func() {} }

func TestGistIDRegex(t *testing.T) {
	for _, s := range []string{"d168778e8e62f65886000f3f314d63e3", "d168778e8e62f6588600"} {
		if !gistIDRegex.MatchString(s) {
			t.Errorf("should match %q", s)
		}
	}
	for _, s := range []string{"", "short", "d168778e8e62f658860", "not-hex-string!!!!!!!!"} {
		if gistIDRegex.MatchString(s) {
			t.Errorf("should not match %q", s)
		}
	}
}

func TestSkills(t *testing.T) {
	s := &fakeSession{skills: []resources.Skill{
		{Name: "zeta", Description: strings.Repeat("d", 60), FilePath: "/z/SKILL.md"},
		{Name: "alpha", Description: "first", FilePath: "/a/SKILL.md"},
	}}
	h := &fakeHost{sess: s, cwd: t.TempDir()}
	o := &out{}
	run(t, &fakeHost{sess: &fakeSession{}}, o, "skills", "")
	expect(t, o, "status:No skills loaded.")
	run(t, h, o, "skills", "")
	if l := o.last(); !strings.HasPrefix(l, "code:NAME") || strings.Index(l, "alpha") > strings.Index(l, "zeta") || !strings.Contains(l, "...") {
		t.Fatalf("bad list %q", l)
	}
	run(t, h, o, "skills", "list")
	expect(t, o, "alpha")
	run(t, h, o, "skills", "alpha")
	expect(t, o, "Location:    /a/SKILL.md")
	run(t, h, o, "skills", "ghost")
	expect(t, o, "Unknown skills subcommand or skill: ghost")
}

func TestSkillsInstall(t *testing.T) {
	builtins := resources.LoadBuiltinSkills().Skills
	if len(builtins) == 0 {
		t.Skip("no builtin skills embedded")
	}
	name := builtins[0].Name
	s := &fakeSession{}
	h := &fakeHost{sess: s, cwd: t.TempDir()}
	userDir := t.TempDir()
	orig := userSkillsDir
	t.Cleanup(func() { userSkillsDir = orig })
	userSkillsDir = func() string { return userDir }
	o := &out{}

	run(t, h, o, "skills", "install")
	expect(t, o, "warn:Usage: /skills install <name>")
	run(t, h, o, "skills", "install ghost-skill")
	expect(t, o, `Unknown builtin skill "ghost-skill"`)

	run(t, h, o, "skills", "install "+name)
	expect(t, o, "(project)")
	if _, err := os.Stat(filepath.Join(h.cwd, ".fir", "skills", name, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if s.reloads != 1 || h.reloaded != 1 {
		t.Fatalf("expected reload + hook, got %d %d", s.reloads, h.reloaded)
	}
	run(t, h, o, "skills", "install "+name)
	expect(t, o, "already exists")
	run(t, h, o, "skills", "install "+name+" --force")
	expect(t, o, "(project)")
	run(t, h, o, "skills", "install "+name+" --user")
	expect(t, o, "(user)")
	if _, err := os.Stat(filepath.Join(userDir, name, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilities(t *testing.T) {
	if len(Capabilities(mcp.ServerDetail{})) != 0 {
		t.Fatal("expected none")
	}
}

func TestNilAdapters(t *testing.T) {
	if NewSession(nil) != nil || NewAuth(nil) != nil {
		t.Fatal("nil session should give nil adapters")
	}
}

func TestSkillsInstallFailure(t *testing.T) {
	builtins := resources.LoadBuiltinSkills().Skills
	if len(builtins) == 0 {
		t.Skip("no builtin skills embedded")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	o := &out{}
	run(t, &fakeHost{sess: &fakeSession{}, cwd: file}, o, "skills", "install "+builtins[0].Name)
	expect(t, o, "Failed to install skill")
	if err := copyBuiltinSkill("no-such-skill", t.TempDir()); err == nil {
		t.Fatal("expected walk error")
	}
}

func TestDefaultHooks(t *testing.T) {
	if !strings.HasSuffix(userSkillsDir(), filepath.Join(".fir", "agent", "skills")) {
		t.Fatal(userSkillsDir())
	}
	if _, err := runCommand(context.Background(), "true"); err != nil {
		t.Fatal(err)
	}
}

func TestMCPReloadCollisions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", dir)
	cfg := []byte(`{"mcpServers":{"dup":{"command":"true"}}}`)
	if err := os.MkdirAll(filepath.Join(dir, "mcp.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "mcp.json"), filepath.Join(dir, "mcp.d", "a.json")} {
		if err := os.WriteFile(p, cfg, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o := &out{}
	h := &fakeHost{cwd: t.TempDir(), sess: &fakeSession{}, mcp: FuncMCP{ReloadFn: func(context.Context) error { return nil }}}
	run(t, h, o, "mcp", "reload")
	expect(t, o, "Collisions detected:\n- dup — loaded from")
}

func TestNilHost(t *testing.T) {
	for _, cmd := range [][2]string{{"mcp", ""}, {"mcp", "login s"}, {"mcp", "logout s"}, {"login", "x"}, {"logout", ""}, {"name", ""}, {"skills", "install x"}} {
		o := &out{}
		c := &slashcmd.Ctx{Out: o}
		registry.Dispatch(c, cmd[0], cmd[1])
		if !strings.HasPrefix(o.last(), "warn:") {
			t.Errorf("/%s %s with no host: %q", cmd[0], cmd[1], o.lines)
		}
	}
	o := &out{}
	LoginProvider(&slashcmd.Ctx{Out: o}, "x")
	expect(t, o, "warn:Model registry not available.")
}

func TestAdapters(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	as := auth.NewInMemoryAuthStorage(nil)
	s := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          agent.NewAgent(agent.AgentOptions{}),
		SessionStore:   store.NewSessionStore(cwd, filepath.Join(agentDir, "sessions")),
		ResourceLoader: resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir}),
		ModelRegistry:  models.NewModelRegistry(as, ""),
		Cwd:            cwd,
	})
	t.Cleanup(func() { s.Close() })

	sa := NewSession(s)
	sa.SetSessionName("n")
	if sa.SessionName() != "n" || sa.IsStreaming() || sa.SectionsText() == "" {
		t.Fatal("session adapter")
	}
	_ = sa.Skills()
	if err := sa.Reload(); err != nil {
		t.Fatal(err)
	}
	if p, err := sa.ExportToHTML(filepath.Join(t.TempDir(), "x.html")); err != nil || p == "" {
		t.Fatal(err)
	}

	a := NewAuth(s)
	if a == nil {
		t.Fatal("expected auth adapter")
	}
	_ = a.OAuthProviders()
	_ = a.List()
	if a.Credential("x") != nil {
		t.Fatal("unexpected credential")
	}
	if err := a.Login(context.Background(), "no-such-provider", pinoauth.LoginCallbacks{}); err == nil {
		t.Fatal("expected login error")
	}
	_ = a.Logout("x")
	a.Refresh()
	bare := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          agent.NewAgent(agent.AgentOptions{}),
		SessionStore:   store.NewSessionStore(cwd, filepath.Join(agentDir, "s2")),
		ResourceLoader: resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir}),
		Cwd:            cwd,
	})
	t.Cleanup(func() { bare.Close() })
	if NewAuth(bare) != nil {
		t.Fatal("no registry should give nil auth")
	}
	if !errors.Is(FuncMCP{}.Reload(context.Background()), errNoMCP) {
		t.Fatal("nil reload")
	}
}
