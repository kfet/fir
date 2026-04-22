package session

import "fmt"

// FormatContextUsage returns a compact human-readable context usage summary
// in the form "<percent> / <used> / <window> (<mode>)".
// When usage is unknown (e.g. right after compaction), renders "?%" / "?".
func FormatContextUsage(cu *ContextUsage, compactMode string) string {
	if cu == nil {
		return ""
	}
	if cu.Tokens < 0 || cu.Percent < 0 {
		return fmt.Sprintf("?%% / ? / %d (%s)", cu.ContextWindow, compactMode)
	}
	return fmt.Sprintf("%.1f%% / %d / %d (%s)", cu.Percent, cu.Tokens, cu.ContextWindow, compactMode)
}
