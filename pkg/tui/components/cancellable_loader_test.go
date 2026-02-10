package components

import (
	"testing"
)

type mockRenderRequester struct{}

func (m *mockRenderRequester) RequestRender() {}

func TestCancellableLoader_Create(t *testing.T) {
	cl := NewCancellableLoader(&mockRenderRequester{}, func(s string) string { return s }, func(s string) string { return s }, "Loading...")
	defer cl.Dispose()

	if cl.Aborted() {
		t.Error("should not be aborted initially")
	}
}

func TestCancellableLoader_Render(t *testing.T) {
	cl := NewCancellableLoader(&mockRenderRequester{}, func(s string) string { return s }, func(s string) string { return s }, "Working...")
	defer cl.Dispose()

	lines := cl.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
}

func TestCancellableLoader_Abort(t *testing.T) {
	abortCalled := false
	cl := NewCancellableLoader(&mockRenderRequester{}, func(s string) string { return s }, func(s string) string { return s }, "Loading...")
	defer cl.Dispose()

	cl.OnAbort = func() {
		abortCalled = true
	}

	// Simulate escape key
	cl.HandleInput("\x1b")

	if !cl.Aborted() {
		t.Error("should be aborted after escape")
	}
	if !abortCalled {
		t.Error("OnAbort should have been called")
	}

	// Second abort should be no-op
	cl.HandleInput("\x1b")
}

func TestCancellableLoader_AbortChannel(t *testing.T) {
	cl := NewCancellableLoader(&mockRenderRequester{}, func(s string) string { return s }, func(s string) string { return s }, "Loading...")
	defer cl.Dispose()

	ch := cl.AbortCh()

	// Should not be closed yet
	select {
	case <-ch:
		t.Fatal("channel should not be closed yet")
	default:
	}

	cl.HandleInput("\x1b")

	// Should be closed now
	select {
	case <-ch:
	default:
		t.Fatal("channel should be closed after abort")
	}
}
