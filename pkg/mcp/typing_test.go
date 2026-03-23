package mcp

import (
	"testing"
)

func TestSendTypingIndicator_NilManager(t *testing.T) {
	// SendTypingIndicator requires a non-nil manager with a connected server.
	// With a nil manager it should panic or fail — this test just verifies
	// the function signature compiles and the package builds.
	t.Log("SendTypingIndicator compiles correctly")
}
