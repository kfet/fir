package components

import (
	"fmt"
	"sync"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// PlanComponent renders the current session plan entries.
type PlanComponent struct {
	mu       sync.Mutex
	box      *tuicomp.Box
	title    string
	metadata map[string]string
	entries  []agent.PlanEntry
}

// NewPlanComponent creates a new PlanComponent.
func NewPlanComponent(title string, entries []agent.PlanEntry, metadata map[string]string) *PlanComponent {
	t := theme.GetTheme()
	c := &PlanComponent{
		box:      tuicomp.NewBox(1, 1, func(s string) string { return t.Bg("customMessageBg", s) }),
		title:    title,
		metadata: metadata,
		entries:  entries,
	}
	c.rebuildBox()
	return c
}

// SetEntries updates the plan entries and rebuilds the display.
func (c *PlanComponent) SetEntries(title string, entries []agent.PlanEntry, metadata map[string]string) {
	c.mu.Lock()
	c.title = title
	c.metadata = metadata
	c.entries = entries
	c.rebuildBox()
	c.mu.Unlock()
}

// Invalidate rebuilds the display.
func (c *PlanComponent) Invalidate() {
	c.mu.Lock()
	c.box.Invalidate()
	c.rebuildBox()
	c.mu.Unlock()
}

// Render renders the plan component. Thread-safe.
func (c *PlanComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.box.Render(width)
}

// rebuildBox rebuilds all Box children from current state.
// Must be called with c.mu held.
func (c *PlanComponent) rebuildBox() {
	c.box.Clear()
	t := theme.GetTheme()

	title := c.title
	metadata := c.metadata
	entries := c.entries

	labelText := "[plan]"
	if title != "" {
		labelText = "[plan: " + title + "]"
	}
	label := t.Fg("customMessageLabel", "\x1b[1m"+labelText+"\x1b[22m")
	c.box.AddChild(tuicomp.NewText(label, 0, 0, nil))

	// Render metadata key-value pairs in stable order.
	if len(metadata) > 0 {
		keys := sortedKeys(metadata)
		for _, k := range keys {
			line := t.Fg("muted", fmt.Sprintf("  %s: %s", k, metadata[k]))
			c.box.AddChild(tuicomp.NewText(line, 0, 0, nil))
		}
	}

	if len(entries) == 0 {
		c.box.AddChild(tuicomp.NewText(t.Fg("muted", "  No plan entries."), 0, 0, nil))
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
	c.box.AddChild(tuicomp.NewText(t.Fg("muted", summary), 0, 0, nil))
	c.box.AddChild(tuicomp.NewSpacer(1))

	for _, e := range entries {
		icon, color := planEntryStyle(e)
		priorityTag := ""
		if e.Priority == agent.PlanEntryPriorityHigh {
			priorityTag = t.Fg("warning", " !")
		}
		line := t.Fg(color, fmt.Sprintf("  %s %s", icon, e.Content)) + priorityTag
		c.box.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}
}

// sortedKeys returns map keys in sorted order for stable rendering.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort for small maps (typically 3-5 keys).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
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

// Ensure PlanComponent implements tui.Component.
var _ tui.Component = (*PlanComponent)(nil)
