package slashcmd

import (
	"strings"
	"testing"

	"github.com/kfet/pinoauth"
)

type recOut struct{ lines []string }

func (o *recOut) Message(t string)                        { o.lines = append(o.lines, "msg:"+t) }
func (o *recOut) Status(t string)                         { o.lines = append(o.lines, "status:"+t) }
func (o *recOut) Warn(t string)                           { o.lines = append(o.lines, "warn:"+t) }
func (o *recOut) Code(t string)                           { o.lines = append(o.lines, "code:"+t) }
func (o *recOut) LoginCallbacks() pinoauth.LoginCallbacks { return pinoauth.LoginCallbacks{} }

func TestSpecs_Sane(t *testing.T) {
	for _, s := range Specs {
		if s.Name == "" || s.Description == "" {
			t.Errorf("spec %+v needs a name and description", s)
		}
		if strings.HasPrefix(s.Name, "/") || strings.ContainsAny(s.Name, " \t") {
			t.Errorf("spec name %q must be bare", s.Name)
		}
	}
}

func TestLookupAndAliases(t *testing.T) {
	if s, ok := Lookup("exit"); !ok || s.Name != "quit" {
		t.Fatalf("exit should alias quit, got %+v %v", s, ok)
	}
	if !IsBuiltinName("mcp") || IsBuiltinName("nope") {
		t.Fatal("IsBuiltinName wrong")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unexpected lookup hit")
	}
}

func TestForMode_RespectsModes(t *testing.T) {
	has := func(m Mode, name string) bool {
		for _, s := range ForMode(m) {
			if s.Name == name {
				return true
			}
		}
		return false
	}
	if !has(TUI, "theme") || has(ACP, "theme") {
		t.Error("theme should be TUI-only")
	}
	if has(TUI, "continue") || !has(ACP, "continue") {
		t.Error("continue should be ACP-only")
	}
	if !has(TUI, "mcp") || !has(ACP, "mcp") {
		t.Error("mcp should be in both modes")
	}
}

func TestRegistry_BindPanics(t *testing.T) {
	cases := map[string]func(r *Registry){
		"undeclared":     func(r *Registry) { r.Bind("nope", nil) },
		"wrong mode":     func(r *Registry) { r.Bind("theme", nil) },
		"alias":          func(r *Registry) { NewRegistry(TUI).Bind("exit", nil) },
		"undeclared sub": func(r *Registry) { r.Bind("mcp nope", nil) },
		"double bind": func(r *Registry) {
			r.Bind("mcp", func(*Ctx, string) {})
			r.Bind("mcp", func(*Ctx, string) {})
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			fn(NewRegistry(ACP))
		})
	}
}

func TestRegistry_BindIfAvailableSkipsOtherModes(t *testing.T) {
	r := NewRegistry(ACP)
	r.BindIfAvailable("theme", func(*Ctx, string) {})
	if r.Has("theme") {
		t.Fatal("theme must not bind in ACP")
	}
}

func TestRegistry_ValidateReportsMissing(t *testing.T) {
	r := NewRegistry(ACP)
	err := r.Validate()
	if err == nil {
		t.Fatal("empty registry must fail validation")
	}
	for _, want := range []string{"/mcp declared", "/mcp login declared", "/continue declared"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "/theme") {
		t.Errorf("TUI-only /theme reported for ACP: %v", err)
	}
}

func TestRegistry_DispatchRoutesSubcommands(t *testing.T) {
	r := NewRegistry(TUI)
	var got []string
	r.Bind("mcp", func(_ *Ctx, a string) { got = append(got, "root:"+a) })
	r.Bind("mcp login", func(_ *Ctx, a string) { got = append(got, "login:"+a) })
	r.Bind("quit", func(c *Ctx, a string) { got = append(got, "quit:"+c.Mode.String()) })
	c := &Ctx{Out: &recOut{}}
	for _, line := range [][2]string{{"mcp", ""}, {"mcp", "  demo "}, {"mcp", "login  slack "}, {"mcp", "loginx"}, {"exit", ""}} {
		if !r.Dispatch(c, line[0], line[1]) {
			t.Fatalf("dispatch %v failed", line)
		}
	}
	want := "root:|root:demo|login:slack|root:loginx|quit:tui"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %v want %s", got, want)
	}
	if r.Dispatch(c, "continue", "") {
		t.Fatal("ACP-only command dispatched in TUI")
	}
	if r.Dispatch(c, "nope", "") {
		t.Fatal("unknown command dispatched")
	}
}

func TestCtxAsync(t *testing.T) {
	ran := false
	(&Ctx{Go: func(fn func()) { fn() }}).Async(func() { ran = true })
	if !ran {
		t.Fatal("Go hook not used")
	}
	done := make(chan struct{})
	(&Ctx{}).Async(func() { close(done) })
	<-done
}

func TestHelpTextAndFullDescription(t *testing.T) {
	h := HelpText(ACP)
	if !strings.Contains(h, "/mcp [<name>]") || !strings.Contains(h, "  login") || strings.Contains(h, "/theme") {
		t.Fatalf("unexpected ACP help:\n%s", h)
	}
	s, _ := Lookup("mcp")
	d := s.FullDescription(ACP)
	if !strings.Contains(d, "subcommands: reload, login, logout") || !strings.Contains(d, "usage: /mcp") {
		t.Fatalf("FullDescription = %q", d)
	}
	if Mode(0).String() != "mode(0)" || All.String() != "all" {
		t.Fatal("Mode.String")
	}
}

func TestIndexRejectsDuplicates(t *testing.T) {
	if _, err := index([]Spec{{Name: "a"}, {Name: "b", Aliases: []string{"a"}}}); err == nil {
		t.Fatal("expected duplicate name error")
	}
	if _, err := index([]Spec{{Name: "a", Subs: []Sub{{Name: "x"}, {Name: "x"}}}}); err == nil {
		t.Fatal("expected duplicate sub error")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("mustIndex should panic")
		}
	}()
	mustIndex([]Spec{{Name: "a"}, {Name: "a"}})
}

func TestRegistry_ModeAndBindIfAvailable(t *testing.T) {
	r := NewRegistry(TUI)
	if r.Mode() != TUI {
		t.Fatal("Mode")
	}
	r.BindIfAvailable("theme", func(*Ctx, string) {})
	if !r.Has("theme") {
		t.Fatal("theme should bind in TUI")
	}
}
