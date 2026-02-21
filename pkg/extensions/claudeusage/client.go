package claudeusage

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	barWidth        = 20
	yellowThreshold = 85.0
	redThreshold    = 95.0
	userAgent       = "fir-claude-usage/0.1.0"
	maxErrorBodyLen = 200
)

// usageEndpoint is the usage API URL. It's a var so tests can override it.
var usageEndpoint = "https://api.anthropic.com/api/oauth/usage"

// nowFunc returns the current time. Tests can override it.
var nowFunc = time.Now

// windowData represents a single usage time window.
type windowData struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    *string `json:"resets_at"`
}

// usageData holds all usage windows from the API.
type usageData struct {
	FiveHour *windowData `json:"five_hour"`
	SevenDay *windowData `json:"seven_day"`
}

// httpClient is used for usage API requests. The 10-second timeout prevents
// stalled goroutines if the Anthropic API hangs.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// errUnauthorized indicates a 401 response from the API.
var errUnauthorized = fmt.Errorf("unauthorized (401)")

// fetchUsage calls the usage API with the given bearer token.
func fetchUsage(token string) (*usageData, error) {
	req, err := http.NewRequest("GET", usageEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching usage: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		preview := string(body)
		if len(preview) > maxErrorBodyLen {
			preview = preview[:maxErrorBodyLen] + "..."
		}
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, preview)
	}

	var data usageData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parsing usage: %w", err)
	}
	return &data, nil
}

// progressBar renders a text-based progress bar of the given width.
func progressBar(pct float64, width int) string {
	filled := max(0, min(width, int(math.Round(pct/100.0*float64(width)))))
	return strings.Repeat("▰", filled) + strings.Repeat("▱", width-filled)
}

// warnIcon returns a colored warning icon for elevated usage, empty for normal.
func warnIcon(pct float64) string {
	switch {
	case pct >= redThreshold:
		return "‼️"
	case pct >= yellowThreshold:
		return "⚠️"
	default:
		return ""
	}
}

// timeUntil formats an ISO 8601 timestamp as a human-readable duration from now.
func timeUntil(isoStr string) string {
	t, err := time.Parse(time.RFC3339, isoStr)
	if err != nil {
		return "unknown"
	}
	d := t.Sub(nowFunc())
	if d <= 0 {
		return "now"
	}
	return formatDuration(d)
}

// formatDuration renders a duration as a compact human-readable string.
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	if days > 0 {
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// resetClock formats the wall-clock time of a reset timestamp.
// Beyond 24h from now: "Mon 3:04 PM"; within 24h: "3:04 PM".
func resetClock(isoStr string) string {
	t, err := time.Parse(time.RFC3339, isoStr)
	if err != nil {
		return ""
	}
	local := t.Local()
	d := t.Sub(nowFunc())
	if d > 24*time.Hour {
		return local.Format("Mon 3:04 PM")
	}
	return local.Format("3:04 PM")
}

// formatWindowStatus formats a single usage window for the status line.
// Example: "🟡5h:55% ↻2h 30m (7:53 PM)"
func formatWindowStatus(label string, w *windowData) string {
	if w == nil {
		return label + ":—"
	}
	icon := warnIcon(w.Utilization)
	s := fmt.Sprintf("%s%s:%.0f%%", icon, label, w.Utilization)
	if w.ResetsAt != nil {
		s += fmt.Sprintf(" ↻%s", timeUntil(*w.ResetsAt))
		if clock := resetClock(*w.ResetsAt); clock != "" {
			s += fmt.Sprintf(" (%s)", clock)
		}
	}
	return s
}

// formatStatusLine builds a compact status line for the footer.
// It always shows both 5-hour and 7-day windows. Color icons appear only
// for warn (⚠️ ≥85%) or urgent (‼️ ≥95%) utilization levels.
func formatStatusLine(data *usageData) string {
	fiveHour := formatWindowStatus("5h", data.FiveHour)
	sevenDay := formatWindowStatus("7d", data.SevenDay)
	return "◎ " + fiveHour + " · " + sevenDay
}

// formatWindowSection returns display lines for a usage window.
func formatWindowSection(name string, w *windowData) []string {
	if w == nil {
		return []string{
			fmt.Sprintf("%s — n/a", name),
		}
	}
	pct := w.Utilization
	header := fmt.Sprintf("%s  %s — %.1f%%", warnIcon(pct), name, pct)
	bar := fmt.Sprintf("    %s", progressBar(pct, barWidth))
	lines := []string{header, bar}
	if w.ResetsAt != nil {
		lines = append(lines, fmt.Sprintf("    ⏱  Resets in %s", timeUntil(*w.ResetsAt)))
	}
	return lines
}

// formatFull returns a multi-line string for detailed display.
func formatFull(data *usageData) string {
	var sb strings.Builder
	sb.WriteString(formatStatusLine(data))
	sb.WriteString("\n")

	sections := [][]string{
		formatWindowSection("5-Hour", data.FiveHour),
		formatWindowSection("7-Day", data.SevenDay),
	}
	for i, lines := range sections {
		sb.WriteString("\n")
		for _, l := range lines {
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		if i < len(sections)-1 && len(lines) > 1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}
