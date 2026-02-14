package notify

import (
	"testing"

	"github.com/kfet/pi-go/pkg/core"
	"github.com/kfet/pi-go/pkg/extension"
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
