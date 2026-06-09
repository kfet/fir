package session

import (
	"fmt"
	"strings"

	"github.com/kfet/agent"
	"github.com/kfet/agent/tools"
	"github.com/kfet/fir/pkg/session/store"
)

// planCardPublisher returns a tools.CardPublisher that mirrors plan
// state into the per-session observable cards store as a single
// "plan/active" card. The implementation was hoisted out of the agent
// module's tools package (github.com/kfet/agent/tools) to keep the tools
// package free of fir-specific dependencies on pkg/session/store; see
// docs/design/ai-agent-extraction.md (Phase 1).
//
// observables may be nil — Put/Clear are nil-safe at the store layer,
// so the returned publisher remains safe to invoke.
func planCardPublisher(observables *store.ObservableStore) tools.CardPublisher {
	return func(title string, entries []agent.PlanEntry, metadata map[string]string, entryID string) {
		if len(entries) == 0 {
			observables.Clear("plan", "active")
			return
		}
		observables.Put(
			"plan", "active",
			planSlug(entries, metadata),
			planDetail(title, entries),
			entryID,
		)
	}
}

// planSlug renders the short headline for the plan card.
//
//	progress_metric (if set, non-empty)
//	  OR
//	"<completed>/<total> <inflight-status>"
//	  where inflight-status is "in_progress" if any entry is in progress,
//	  "done" if all completed, else "pending"
func planSlug(entries []agent.PlanEntry, metadata map[string]string) string {
	if metric := strings.TrimSpace(metadata["progress_metric"]); metric != "" {
		return metric
	}
	total := len(entries)
	completed := 0
	inProgress := false
	for _, e := range entries {
		switch e.Status {
		case agent.PlanEntryStatusCompleted:
			completed++
		case agent.PlanEntryStatusInProgress:
			inProgress = true
		}
	}
	status := "pending"
	switch {
	case completed == total:
		status = "done"
	case inProgress:
		status = "in_progress"
	}
	return fmt.Sprintf("%d/%d %s", completed, total, status)
}

// planDetail renders the bullet listing of plan entries. One line per
// entry, with a status marker (✓ done, ▶ in progress, · pending) and
// the entry's content. Title (when set) goes on the first line.
func planDetail(title string, entries []agent.PlanEntry) string {
	var sb strings.Builder
	if t := strings.TrimSpace(title); t != "" {
		sb.WriteString(t)
		sb.WriteByte('\n')
	}
	for _, e := range entries {
		marker := "·" // pending / unknown
		switch e.Status {
		case agent.PlanEntryStatusCompleted:
			marker = "✓"
		case agent.PlanEntryStatusInProgress:
			marker = "▶"
		}
		sb.WriteString(marker)
		sb.WriteByte(' ')
		sb.WriteString(e.Content)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}
