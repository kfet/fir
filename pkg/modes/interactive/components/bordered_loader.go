// Ported from: packages/coding-agent/src/modes/interactive/components/bordered-loader.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// BorderedLoader wraps a loader with borders for extension UI.
type BorderedLoader struct {
	tui.Container
	loader           tui.Component
	cancellableRef   *tuicomp.CancellableLoader
	cancellable      bool
	abortCh          chan struct{} // for non-cancellable mode
}

var _ tui.Component = (*BorderedLoader)(nil)

// BorderedLoaderOptions configures a BorderedLoader.
type BorderedLoaderOptions struct {
	Cancellable bool // default true
}

// NewBorderedLoader creates a new BorderedLoader.
// If opts is nil, defaults to cancellable=true.
func NewBorderedLoader(ui tuicomp.RenderRequester, theme *theme.Theme, message string, opts *BorderedLoaderOptions) *BorderedLoader {
	cancellable := true
	if opts != nil {
		cancellable = opts.Cancellable
	}

	borderColor := func(s string) string { return theme.Fg("border", s) }

	bl := &BorderedLoader{
		cancellable: cancellable,
	}

	bl.AddChild(NewDynamicBorder(borderColor))

	if cancellable {
		cl := tuicomp.NewCancellableLoader(
			ui,
			func(s string) string { return theme.Fg("accent", s) },
			func(s string) string { return theme.Fg("muted", s) },
			message,
		)
		bl.loader = cl
		bl.cancellableRef = cl
	} else {
		bl.abortCh = make(chan struct{})
		l := tuicomp.NewLoader(
			ui,
			func(s string) string { return theme.Fg("accent", s) },
			func(s string) string { return theme.Fg("muted", s) },
			message,
		)
		bl.loader = l
	}

	bl.AddChild(bl.loader)

	if cancellable {
		bl.AddChild(tuicomp.NewSpacer(1))
		bl.AddChild(tuicomp.NewText(KeyHint("selectCancel", "cancel"), 1, 0, nil))
	}

	bl.AddChild(tuicomp.NewSpacer(1))
	bl.AddChild(NewDynamicBorder(borderColor))

	return bl
}

// AbortCh returns a channel that is closed when the loader is aborted.
// For non-cancellable loaders, returns a channel that is never closed.
func (bl *BorderedLoader) AbortCh() <-chan struct{} {
	if bl.cancellable && bl.cancellableRef != nil {
		return bl.cancellableRef.AbortCh()
	}
	if bl.abortCh != nil {
		return bl.abortCh
	}
	return make(chan struct{})
}

// SetOnAbort sets the abort callback (cancellable mode only).
func (bl *BorderedLoader) SetOnAbort(fn func()) {
	if bl.cancellable && bl.cancellableRef != nil {
		bl.cancellableRef.OnAbort = fn
	}
}

// HandleInput forwards input to the loader (cancellable mode only).
func (bl *BorderedLoader) HandleInput(data string) {
	if bl.cancellable && bl.cancellableRef != nil {
		bl.cancellableRef.HandleInput(data)
	}
}

// Dispose stops the loader animation.
func (bl *BorderedLoader) Dispose() {
	if bl.cancellable && bl.cancellableRef != nil {
		bl.cancellableRef.Dispose()
	} else if l, ok := bl.loader.(*tuicomp.Loader); ok {
		l.Stop()
	}
}
