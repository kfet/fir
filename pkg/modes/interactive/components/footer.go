// Ported from: packages/coding-agent/src/modes/interactive/components/footer.ts
// Upstream hash: 7f9a2b3c
package components

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/tui"
)

// FooterData provides the data needed to render the footer.
type FooterData struct {
	Pwd               string
	GitBranch         string
	SessionName       string
	ModelID           string
	ModelProvider     string
	ModelReasoning    bool
	ThinkingLevel     string
	ContextWindow     int
	TotalInput        int
	TotalOutput       int
	TotalCacheRead    int
	TotalCacheWrite   int
	TotalCost         float64
	UsingSubscription bool
	AutoCompactMode   string // "off", "client", "server"
	MultipleProviders bool
	ExtensionStatuses map[string]string
	// ContextPercent is the estimated context usage percentage from GetContextUsage.
	// A negative value means unknown (e.g. right after compaction).
	ContextPercent float64
	// ContextTokens is the estimated context tokens. Negative means unknown.
	ContextTokens int
	// QueuedMessages is the number of follow-up messages waiting in the queue.
	QueuedMessages int
	// PlanCompleted is the number of completed plan entries.
	PlanCompleted int
	// PlanTotal is the total number of plan entries (0 = no plan).
	PlanTotal int
	// PlanCurrentStep is the content of the current in-progress step (empty if none).
	PlanCurrentStep string
	// PlanTitle is a short title for the plan (shown when plan is complete).
	PlanTitle string
	// PlanKeyHint is the display string for the toggle-plan keybinding (e.g. "ctrl+r").
	PlanKeyHint string
}

// FooterComponent renders a status footer with pwd, token stats, and context usage.
type FooterComponent struct {
	getData func() FooterData
}

var _ tui.Component = (*FooterComponent)(nil)

// NewFooterComponent creates a new footer component.
// getData is called on each render to get the latest data.
func NewFooterComponent(getData func() FooterData) *FooterComponent {
	return &FooterComponent{getData: getData}
}

// Invalidate is a no-op.
func (f *FooterComponent) Invalidate() {}

// Render renders the footer.
func (f *FooterComponent) Render(width int) []string {
	data := f.getData()
	t := theme.GetTheme()

	// Build pwd line
	pwd := data.Pwd
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	if home != "" && strings.HasPrefix(pwd, home) {
		pwd = "~" + pwd[len(home):]
	}
	if data.GitBranch != "" {
		pwd = fmt.Sprintf("%s (%s)", pwd, data.GitBranch)
	}
	if data.SessionName != "" {
		pwd = pwd + " • " + data.SessionName
	}
	// Truncate pwd
	if len(pwd) > width {
		half := width/2 - 2
		if half > 1 {
			pwd = pwd[:half] + "..." + pwd[len(pwd)-(half-1):]
		} else if width > 0 {
			pwd = pwd[:width]
		}
	}

	// Build stats parts
	var statsParts []string
	if data.TotalInput > 0 {
		statsParts = append(statsParts, "↑"+formatTokens(data.TotalInput))
	}
	if data.TotalOutput > 0 {
		statsParts = append(statsParts, "↓"+formatTokens(data.TotalOutput))
	}
	if data.TotalCacheRead > 0 {
		statsParts = append(statsParts, "R"+formatTokens(data.TotalCacheRead))
	}
	if data.TotalCacheWrite > 0 {
		statsParts = append(statsParts, "W"+formatTokens(data.TotalCacheWrite))
	}
	if data.TotalCost > 0 || data.UsingSubscription {
		costStr := fmt.Sprintf("$%.3f", data.TotalCost)
		if data.UsingSubscription {
			costStr += " (sub)"
		}
		statsParts = append(statsParts, costStr)
	}

	// Context percentage — use estimated context usage (not accumulated totals)
	autoIndicator := ""
	switch data.AutoCompactMode {
	case "client":
		autoIndicator = " (auto)"
	case "server":
		autoIndicator = " (auto-server)"
	}

	contextWindow := data.ContextWindow
	contextPercentValue := data.ContextPercent

	var contextDisplay string
	if contextPercentValue < 0 {
		// Unknown (e.g. right after compaction, before next LLM response)
		contextDisplay = fmt.Sprintf("?%%/%s%s", formatTokens(contextWindow), autoIndicator)
	} else {
		contextDisplay = fmt.Sprintf("%.1f%%/%s%s", contextPercentValue, formatTokens(contextWindow), autoIndicator)
	}

	var contextPercentStr string
	if contextPercentValue > 90 {
		contextPercentStr = t.Fg("error", contextDisplay)
	} else if contextPercentValue > 70 {
		contextPercentStr = t.Fg("warning", contextDisplay)
	} else {
		contextPercentStr = contextDisplay
	}
	statsParts = append(statsParts, contextPercentStr)

	// Queued follow-up messages
	if data.QueuedMessages > 0 {
		queueStr := fmt.Sprintf("📬 %d queued", data.QueuedMessages)
		statsParts = append(statsParts, t.Fg("warning", queueStr))
	}

	// Plan progress
	if data.PlanTotal > 0 {
		planStr := fmt.Sprintf("📋 %d/%d", data.PlanCompleted, data.PlanTotal)
		if data.PlanCompleted == data.PlanTotal && data.PlanTitle != "" {
			// Plan is done — show the title instead of step content
			title := data.PlanTitle
			if len(title) > 30 {
				title = title[:27] + "..."
			}
			planStr += ": " + title
		} else if data.PlanCurrentStep != "" {
			step := data.PlanCurrentStep
			if len(step) > 30 {
				step = step[:27] + "..."
			}
			planStr += ": " + step
		}
		if data.PlanKeyHint != "" {
			planStr += " (" + data.PlanKeyHint + ")"
		}
		if data.PlanCompleted == data.PlanTotal {
			statsParts = append(statsParts, t.Fg("success", planStr))
		} else {
			statsParts = append(statsParts, t.Fg("accent", planStr))
		}
	}

	statsLeft := strings.Join(statsParts, " ")

	// Right side: model name + thinking level
	rightSide := data.ModelID
	if rightSide == "" {
		rightSide = "no-model"
	}
	if data.ModelReasoning {
		if data.ThinkingLevel == "" || data.ThinkingLevel == "off" {
			rightSide += " • thinking off"
		} else {
			rightSide += " • " + data.ThinkingLevel
		}
	}
	if data.ModelProvider != "" {
		full := fmt.Sprintf("(%s) %s", data.ModelProvider, rightSide)
		if tui.VisibleWidth(statsLeft)+2+tui.VisibleWidth(full) <= width {
			rightSide = full
		}
	}

	// Compose stats line
	statsLeftWidth := tui.VisibleWidth(statsLeft)
	rightSideWidth := tui.VisibleWidth(rightSide)
	totalNeeded := statsLeftWidth + 2 + rightSideWidth

	var statsLine string
	if totalNeeded <= width {
		padding := strings.Repeat(" ", width-statsLeftWidth-rightSideWidth)
		statsLine = statsLeft + padding + rightSide
	} else {
		avail := width - statsLeftWidth - 2
		if avail > 3 {
			statsLine = statsLeft + "  " + rightSide[:avail]
		} else {
			statsLine = statsLeft
		}
	}

	// Dim styling
	dimPwd := t.Fg("dim", pwd)
	dimStatsLeft := t.Fg("dim", statsLeft)
	remainder := statsLine[len(statsLeft):]
	dimRemainder := t.Fg("dim", remainder)

	// Merge extension statuses into the pwd line (right-aligned) to avoid a third row.
	extStatus := ""
	if len(data.ExtensionStatuses) > 0 {
		keys := make([]string, 0, len(data.ExtensionStatuses))
		for k := range data.ExtensionStatuses {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, sanitizeStatusText(data.ExtensionStatuses[k]))
		}
		extStatus = strings.Join(parts, " ")
		if tui.VisibleWidth(extStatus) > width/2 {
			extStatus = tui.TruncateToWidth(extStatus, width/2, t.Fg("dim", "…"), false)
		}
	}

	if extStatus != "" {
		pwdWidth := tui.VisibleWidth(pwd)
		extWidth := tui.VisibleWidth(extStatus)
		gap := width - pwdWidth - extWidth
		if gap >= 2 {
			dimPwd = dimPwd + strings.Repeat(" ", gap) + t.Fg("dim", extStatus)
		} else {
			// Not enough room — truncate pwd to make space
			avail := width - extWidth - 2
			if avail > 3 {
				pwd = tui.TruncateToWidth(pwd, avail, "…", false)
				dimPwd = t.Fg("dim", pwd)
				pwdWidth = tui.VisibleWidth(pwd)
				gap = width - pwdWidth - extWidth
				dimPwd = dimPwd + strings.Repeat(" ", max(1, gap)) + t.Fg("dim", extStatus)
			}
			// else: skip extension status, not enough space
		}
	}

	lines := []string{dimPwd, dimStatsLeft + dimRemainder}

	return lines
}

// formatTokens formats token counts for display.
func formatTokens(count int) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	if count < 10000 {
		return fmt.Sprintf("%.1fk", float64(count)/1000)
	}
	if count < 1000000 {
		return fmt.Sprintf("%dk", int(math.Round(float64(count)/1000)))
	}
	if count < 10000000 {
		return fmt.Sprintf("%.1fM", float64(count)/1000000)
	}
	return fmt.Sprintf("%dM", int(math.Round(float64(count)/1000000)))
}

// sanitizeStatusText removes control characters for single-line display.
func sanitizeStatusText(text string) string {
	text = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
	// Collapse multiple spaces
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	return strings.TrimSpace(text)
}
