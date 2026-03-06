package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

func TestPlanComponent_Empty(t *testing.T) {
	c := NewPlanComponent(nil)
	lines := c.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "[plan]") {
		t.Fatal("expected [plan] label")
	}
	if !strings.Contains(joined, "No plan entries") {
		t.Fatal("expected empty message")
	}
}

func TestPlanComponent_Entries(t *testing.T) {
	entries := []agent.PlanEntry{
		{Content: "Step one", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityMedium},
		{Content: "Step two", Status: agent.PlanEntryStatusInProgress, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Step three", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityLow},
	}
	c := NewPlanComponent(entries)
	lines := c.Render(80)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "1/3 completed") {
		t.Fatalf("expected summary, got:\n%s", joined)
	}
	if !strings.Contains(joined, "1 in progress") {
		t.Fatal("expected in progress count")
	}
	if !strings.Contains(joined, "Step one") {
		t.Fatal("expected step one")
	}
	if !strings.Contains(joined, "✓") {
		t.Fatal("expected checkmark for completed")
	}
	if !strings.Contains(joined, "●") {
		t.Fatal("expected bullet for in progress")
	}
	if !strings.Contains(joined, "○") {
		t.Fatal("expected circle for pending")
	}
}

func TestPlanComponent_SetEntries(t *testing.T) {
	c := NewPlanComponent(nil)
	lines := c.Render(80)
	if !strings.Contains(strings.Join(lines, "\n"), "No plan entries") {
		t.Fatal("expected empty initially")
	}

	c.SetEntries([]agent.PlanEntry{
		{Content: "New step", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityMedium},
	})
	lines = c.Render(80)
	if !strings.Contains(strings.Join(lines, "\n"), "New step") {
		t.Fatal("expected new step after SetEntries")
	}
}

func TestPlanComponent_HighPriority(t *testing.T) {
	entries := []agent.PlanEntry{
		{Content: "Urgent", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityHigh},
	}
	c := NewPlanComponent(entries)
	lines := c.Render(80)
	joined := strings.Join(lines, "\n")
	// High priority items get a "!" marker
	if !strings.Contains(joined, "!") {
		t.Fatal("expected ! for high priority")
	}
}
