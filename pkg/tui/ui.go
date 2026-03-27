// Package tui provides types and utilities for terminal UI rendering.
//
// The UI interface abstracts over the concrete TUI framework so that callers
// (e.g. cmd/fir/app.go) can program against a stable contract. The main branch
// uses a custom TUI; the bubbletea branch uses Bubble Tea — both satisfy UI.
package tui

// UI is the top-level interface for running an interactive terminal session.
//
// The lifecycle is:
//
//	ui := NewSomeUI(...)        // construct
//	ui.SetUpdateChannel(ch)     // optional: wire version-check notice
//	ui.Init()                   // initialise TUI state
//	ui.Send(extensionReadyMsg)  // optional: wire extensions via typed message
//	ui.Run()                    // blocks until quit
//	ui.Cleanup()                // release resources
//	ui.ReexecIfRequested()      // never returns on success
type UI interface {
	// SetUpdateChannel supplies a channel that delivers a single
	// version-check notice string at startup. Must be called before Init.
	SetUpdateChannel(ch <-chan string)

	// Init performs one-time initialisation (e.g. loading history, starting
	// subscriptions). Returns an error if setup fails.
	Init() error

	// Run starts the UI event loop and blocks until the user quits.
	Run() error

	// Send pushes a message into the running UI event loop. Safe to call
	// from any goroutine. The concrete type of msg depends on the backend.
	Send(msg any)

	// Cleanup releases resources (subscriptions, goroutines, etc.).
	// Always call after Run returns.
	Cleanup()

	// ReexecIfRequested performs syscall.Exec if the user triggered /reexec.
	// Call after Cleanup. It never returns on success.
	ReexecIfRequested()
}
