package components

import (
	"fmt"
	"sync"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// PlanComponent renders the current session plan entries.
type PlanComponent struct {
	*tuicomp.Box

	mu      sync.Mutex
	entries []agent.PlanEntry
}

// NewPlanComponent creates a new PlanComponent.
func NewPlanComponent(entries []agent.PlanEntry) *PlanComponent {
	t := theme.GetTheme()
	c := &PlanComponent{
		Box:     tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		entries: entries,
	}
	c.updateDisplay()
	return c
}

// SetEntries updates the plan entries and rebuilds the display.
func (c *PlanComponent) SetEntries(entries []agent.PlanEntry) {
	c.mu.Lock()
	c.entries = entries
	c.mu.Unlock()
	c.updateDisplay()
}

// Invalidate rebuilds the display.
func (c *PlanComponent) Invalidate() {
	c.Box.Invalidate()
	c.updateDisplay()
}

func (c *PlanComponent) updateDisplay() {
	c.Clear()
	t := theme.GetTheme()

	c.mu.Lock()
	entries := make([]agent.PlanEntry, len(c.entries))
	copy(entries, c.entries)
	c.mu.Unlock()

	label := t.Fg("customMessageLabel", "\x1b[1m[plan]\x1b[22m")
	c.AddChild(tuicomp.NewText(label, 0, 0, nil))

	if len(entries) == 0 {
		c.AddChild(tuicomp.NewText(t.Fg("muted", "  No plan entries."), 0, 0, nil))
		return
	}

	// Count by status for summary line
	completed, inProgress, pending := 0, 0, 0
	for _, e := range entries {
		switch e.Status {
		case agent.PlanEntryStatusCompleted:
			completed++
		case agent.PlanEntryStatusInProgress:
			inProgress++
		default:
			pending++
		}
	}
	summary := fmt.Sprintf("  %d/%d completed", completed, len(entries))
	if inProgress > 0 {
		summary += fmt.Sprintf(", %d in progress", inProgress)
	}
	if pending > 0 {
		summary += fmt.Sprintf(", %d pending", pending)
	}
	c.AddChild(tuicomp.NewText(t.Fg("muted", summary), 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	for _, e := range entries {
		icon, color := planEntryStyle(e)
		priorityTag := ""
		if e.Priority == agent.PlanEntryPriorityHigh {
			priorityTag = t.Fg("warning", " !")
		}
		line := t.Fg(color, fmt.Sprintf("  %s %s", icon, e.Content)) + priorityTag
		c.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}

// planEntryStyle returns an icon and theme color for a plan entry.
func planEntryStyle(e agent.PlanEntry) (icon string, color string) {
	switch e.Status {
	case agent.PlanEntryStatusCompleted:
		return "✓", "success"
	case agent.PlanEntryStatusInProgress:
		return "●", "accent"
	default:
		return "○", "muted"
	}
}
