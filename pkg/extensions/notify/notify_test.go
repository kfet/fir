package notify

import (
	"bytes"
	"os"
	"testing"

	"github.com/kfet/tau/pkg/core"
	"github.com/kfet/tau/pkg/extension"
)

func TestNotifyExtensionRegisters(t *testing.T) {
	// The notify extension registers itself via init().
	// Verify it's in the registry.
	factories := extension.RegisteredFactories()

	found := false
	for _, f := range factories {
		if f.Name == "notify" {
			found = true
			break
		}
	}

	if !found {
		t.Error("notify extension not found in registry")
	}
}

func TestNotifyExtensionLoads(t *testing.T) {
	// Create a runner and load — should not panic
	runner := extension.NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// The notify extension subscribes to agent_end
	if !runner.HasHandlers("agent_end") {
		t.Error("expected agent_end handler from notify extension")
	}
}

// withTestOutput redirects notification output to a buffer and stubs env checks.
// Returns the buffer and a cleanup function.
func withTestOutput(t *testing.T, tmux, kitty bool) *bytes.Buffer {
	t.Helper()

	origOutput := output
	origTmux := inTmux
	origKitty := inKitty

	var buf bytes.Buffer
	output = &buf
	inTmux = func() bool { return tmux }
	inKitty = func() bool { return kitty }

	t.Cleanup(func() {
		output = origOutput
		inTmux = origTmux
		inKitty = origKitty
	})

	return &buf
}

func TestOSC777Plain(t *testing.T) {
	buf := withTestOutput(t, false, false)

	notifyOSC777("Pi", "Ready for input")

	want := "\x1b]777;notify;Pi;Ready for input\x1b\\"
	if got := buf.String(); got != want {
		t.Errorf("OSC 777 plain:\n got %q\nwant %q", got, want)
	}
}

func TestOSC777Tmux(t *testing.T) {
	buf := withTestOutput(t, true, false)

	notifyOSC777("Pi", "Ready for input")

	want := "\x1bPtmux;\x1b\x1b]777;notify;Pi;Ready for input\x1b\x1b\\\x1b\\"
	if got := buf.String(); got != want {
		t.Errorf("OSC 777 tmux:\n got %q\nwant %q", got, want)
	}
}

func TestOSC99Plain(t *testing.T) {
	buf := withTestOutput(t, false, true)

	notifyOSC99("Pi", "Ready for input")

	want := "\x1b]99;i=1:d=0;Pi\x1b\\" +
		"\x1b]99;i=1:p=body;Ready for input\x1b\\"
	if got := buf.String(); got != want {
		t.Errorf("OSC 99 plain:\n got %q\nwant %q", got, want)
	}
}

func TestOSC99Tmux(t *testing.T) {
	buf := withTestOutput(t, true, true)

	notifyOSC99("Pi", "Ready for input")

	want := "\x1bPtmux;\x1b\x1b]99;i=1:d=0;Pi\x1b\x1b\\\x1b\\" +
		"\x1bPtmux;\x1b\x1b]99;i=1:p=body;Ready for input\x1b\x1b\\\x1b\\"
	if got := buf.String(); got != want {
		t.Errorf("OSC 99 tmux:\n got %q\nwant %q", got, want)
	}
}

func TestNotifyTerminalDispatchOSC777(t *testing.T) {
	buf := withTestOutput(t, false, false)

	notifyTerminal("Pi", "done")

	got := buf.String()
	prefix := "\x1b]777;"
	if len(got) < len(prefix) || got[:len(prefix)] != prefix {
		t.Errorf("expected OSC 777 prefix %q, got %q", prefix, got)
	}
}

func TestNotifyTerminalDispatchKitty(t *testing.T) {
	buf := withTestOutput(t, false, true)

	notifyTerminal("Pi", "done")

	got := buf.String()
	prefix := "\x1b]99;"
	if len(got) < len(prefix) || got[:len(prefix)] != prefix {
		t.Errorf("expected OSC 99 prefix %q, got %q", prefix, got)
	}
}

func TestOutputDefaultsToStderr(t *testing.T) {
	if output != os.Stderr {
		t.Error("output should default to os.Stderr")
	}
}
