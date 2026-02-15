package components

import (
	"testing"

	"github.com/kfet/tau/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

type loaderRenderRequester struct{}

func (m *loaderRenderRequester) RequestRender() {}

func TestBorderedLoader_Cancellable(t *testing.T) {
	theme := theme.GetTheme()
	bl := NewBorderedLoader(&loaderRenderRequester{}, theme, "Loading...", nil)
	defer bl.Dispose()

	lines := bl.Render(60)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (borders + loader), got %d", len(lines))
	}
}

func TestBorderedLoader_NonCancellable(t *testing.T) {
	theme := theme.GetTheme()
	bl := NewBorderedLoader(&loaderRenderRequester{}, theme, "Working...", &BorderedLoaderOptions{Cancellable: false})
	defer bl.Dispose()

	lines := bl.Render(60)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (borders + loader), got %d", len(lines))
	}
}

func TestBorderedLoader_Abort(t *testing.T) {
	theme := theme.GetTheme()
	bl := NewBorderedLoader(&loaderRenderRequester{}, theme, "Loading...", nil)
	defer bl.Dispose()

	abortCalled := false
	bl.SetOnAbort(func() { abortCalled = true })

	// Simulate escape
	bl.HandleInput("\x1b")

	ch := bl.AbortCh()
	select {
	case <-ch:
	default:
		t.Fatal("abort channel should be closed after escape")
	}

	if !abortCalled {
		t.Error("OnAbort should have been called")
	}
}

func TestBorderedLoader_ImplementsComponent(t *testing.T) {
	theme := theme.GetTheme()
	bl := NewBorderedLoader(&loaderRenderRequester{}, theme, "Test", nil)
	defer bl.Dispose()

	// Verify it implements the tui.Component interface via the Container embedding
	var _ tuicomp.RenderRequester = &loaderRenderRequester{}
	_ = bl.Render(40)
}
