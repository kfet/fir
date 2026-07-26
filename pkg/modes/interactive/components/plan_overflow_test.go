package components

import (
	"fmt"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/tui"
)

func TestPlanComponent_LineOverflow(t *testing.T) {
	entries := []agent.PlanEntry{
		{Content: "Regenerate models (make generate-models)", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Full build & test (make all)", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Check CHANGELOG & determine version", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Update CHANGELOG and VERSION", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Commit, tag, install, verify", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Publish (after user confirmation)", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityMedium},
	}
	metadata := map[string]string{
		"version":        "0.27.0",
		"cwd":            "/Users/kfet/dev/ai/fir",
		"next_update_in": "5",
	}
	c := NewPlanComponent("Release v0.27.0", entries, metadata)

	for width := 40; width <= 200; width++ {
		lines := c.Render(width)
		for i, line := range lines {
			vw := tui.VisibleWidth(line)
			if vw != width {
				t.Errorf("width=%d line %d: visible width %d != %d", width, i, vw, width)
			}
		}
	}
}

func TestPlanComponent_ConcurrentAccess(t *testing.T) {
	entries := []agent.PlanEntry{
		{Content: "Step one", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Step two", Status: agent.PlanEntryStatusInProgress, Priority: agent.PlanEntryPriorityMedium},
	}
	c := NewPlanComponent("Test", entries, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			c.SetEntries(fmt.Sprintf("Update %d", i), entries, map[string]string{"i": fmt.Sprintf("%d", i)})
		}
	}()

	for i := 0; i < 1000; i++ {
		lines := c.Render(80)
		for _, line := range lines {
			vw := tui.VisibleWidth(line)
			if vw != 80 {
				t.Errorf("render %d: visible width %d != 80", i, vw)
			}
		}
	}
	<-done
}
