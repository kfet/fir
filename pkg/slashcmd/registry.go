package slashcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kfet/pinoauth"
)

// Output is how a command handler talks to the user. Each mode implements it:
// the TUI renders themed status lines and overlays, ACP sends agent messages.
//
// Implementations may additionally satisfy optional capability interfaces
// declared by handler packages (e.g. a provider picker) to present richer UI;
// handlers must always fall back to plain text when a capability is absent.
type Output interface {
	// Message shows a command's result body (markdown-friendly text).
	Message(text string)
	// Status shows a short success / informational line.
	Status(text string)
	// Warn shows a short warning or error line.
	Warn(text string)
	// Code shows preformatted text whose whitespace must be kept (tables,
	// aligned columns).
	Code(text string)
	// LoginCallbacks returns the callbacks used to drive an interactive OAuth
	// flow (show URL, report progress, prompt for input).
	LoginCallbacks() pinoauth.LoginCallbacks
}

// Ctx is passed to every handler.
type Ctx struct {
	Context context.Context
	Mode    Mode
	Out     Output
	// Host is the mode's handler environment. Shared handlers assert it to
	// the interface they need; mode-only handlers to the concrete mode type.
	Host any
	// Go runs long work off the caller's goroutine. Nil means `go fn()`;
	// tests set it to run fn inline.
	Go func(fn func())
}

// Async runs fn via c.Go (default: a new goroutine). Handlers validate
// arguments synchronously and hand only the slow part (network, OAuth) here,
// so usage errors are reported before Dispatch returns.
func (c *Ctx) Async(fn func()) {
	if c.Go != nil {
		c.Go(fn)
		return
	}
	go fn()
}

// Handler runs a command. args is the text after the command (and after the
// subcommand, for subcommand handlers), trimmed.
type Handler func(c *Ctx, args string)

// Registry binds handlers to the declared Specs for one mode.
type Registry struct {
	mode     Mode
	handlers map[string]Handler
}

// NewRegistry returns an empty registry for mode.
func NewRegistry(mode Mode) *Registry {
	return &Registry{mode: mode, handlers: make(map[string]Handler)}
}

// Mode returns the registry's mode.
func (r *Registry) Mode() Mode { return r.mode }

// Bind registers h for path: "cmd" or "cmd sub". It panics when path is not a
// declared command/subcommand available in the registry's mode, or is
// already bound — so a mode cannot silently shadow a shared handler.
func (r *Registry) Bind(path string, h Handler) {
	if err := r.checkPath(path); err != nil {
		panic(err)
	}
	if _, dup := r.handlers[path]; dup {
		panic(fmt.Sprintf("slashcmd: /%s already bound in %s mode", path, r.mode))
	}
	r.handlers[path] = h
}

// BindIfAvailable binds h only when path exists in the registry's mode. Used
// by shared handler sets so they can register every command they implement
// without knowing which modes declare it.
func (r *Registry) BindIfAvailable(path string, h Handler) {
	if r.checkPath(path) == nil {
		r.Bind(path, h)
	}
}

func (r *Registry) checkPath(path string) error {
	name, sub, _ := strings.Cut(path, " ")
	spec, ok := byName[name]
	if !ok || spec.Name != name {
		return fmt.Errorf("slashcmd: /%s is not a declared builtin command", name)
	}
	if !spec.Available(r.mode) {
		return fmt.Errorf("slashcmd: /%s is not declared for %s mode (set Spec.Modes)", name, r.mode)
	}
	if sub == "" {
		return nil
	}
	for _, s := range spec.Subs {
		if s.Name == sub {
			return nil
		}
	}
	return fmt.Errorf("slashcmd: /%s has no declared subcommand %q", name, sub)
}

// Has reports whether path ("cmd" or "cmd sub") has a handler.
func (r *Registry) Has(path string) bool {
	_, ok := r.handlers[path]
	return ok
}

// Validate reports every command or subcommand that is declared for this
// mode but has no handler. A non-nil error means the mode would silently lack
// a command the other modes have — the drift this package exists to prevent.
func (r *Registry) Validate() error {
	var errs []error
	for _, s := range ForMode(r.mode) {
		if !r.Has(s.Name) {
			// A command whose every use is a subcommand still needs a root
			// handler for the bare form; require it uniformly.
			errs = append(errs, fmt.Errorf("/%s declared for %s mode has no handler", s.Name, r.mode))
		}
		for _, sub := range s.Subs {
			if !r.Has(s.Name + " " + sub.Name) {
				errs = append(errs, fmt.Errorf("/%s %s declared for %s mode has no handler", s.Name, sub.Name, r.mode))
			}
		}
	}
	return errors.Join(errs...)
}

// Resolve finds the handler for a command line: name is the command without
// "/", args the remaining text. It routes `name sub rest` to the subcommand
// handler when sub is declared for this mode. ok is false when name is not a
// builtin available in this mode.
func (r *Registry) Resolve(name, args string) (h Handler, rest string, ok bool) {
	spec, found := byName[name]
	if !found || !spec.Available(r.mode) {
		return nil, "", false
	}
	args = strings.TrimSpace(args)
	first, tail, _ := strings.Cut(args, " ")
	for _, sub := range spec.Subs {
		if sub.Name == first {
			if h, ok := r.handlers[spec.Name+" "+sub.Name]; ok {
				return h, strings.TrimSpace(tail), true
			}
		}
	}
	h, ok = r.handlers[spec.Name]
	return h, args, ok
}

// Dispatch runs the command, returning false when name is not a builtin
// command bound in this mode. Handlers run on the caller's goroutine; long
// work (network, OAuth) is moved off it by the handler via Ctx.Async.
func (r *Registry) Dispatch(c *Ctx, name, args string) bool {
	h, rest, ok := r.Resolve(name, args)
	if !ok {
		return false
	}
	if c.Context == nil {
		c.Context = context.Background()
	}
	c.Mode = r.mode
	h(c, rest)
	return true
}
