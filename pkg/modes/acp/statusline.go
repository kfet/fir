package acp

import (
	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/session/store"
)

// statusLineExtKey is the ACP _meta extension key that relays (poe-acp,
// slack-acp) use to render a mood/plan status header. See
// docs/design/observable-cards.md for the data-source side.
const statusLineExtKey = "dev.acp-kit.status-line/v1"

// buildStatusLineMeta reads the observable cards store and returns a
// map suitable for SessionNotification.Meta, or nil if there is nothing
// to report. The returned map contains:
//
//	{"dev.poe-acp.status-line/v1": {"mood": "<slug>", "plan": "<slug>"}}
//
// Only populated fields are included; if both mood and plan are absent
// the function returns nil so callers can skip the _meta key entirely.
func buildStatusLineMeta(obs *store.ObservableStore) map[string]any {
	if obs == nil {
		return nil
	}
	cards := obs.List() // sorted (Source asc, Ts desc)
	if len(cards) == 0 {
		return nil
	}

	// Pick the latest slug per source. List() is ordered by (Source asc,
	// Ts desc), so the first card per source is the most recent.
	var moodSlug, planSlug string
	seen := make(map[string]bool, 2)
	for _, c := range cards {
		if seen[c.Source] {
			continue
		}
		seen[c.Source] = true
		switch c.Source {
		case "mood":
			moodSlug = c.Slug
		case "plan":
			planSlug = c.Slug
		}
	}

	if moodSlug == "" && planSlug == "" {
		return nil
	}

	payload := make(map[string]string, 2)
	if moodSlug != "" {
		payload["mood"] = moodSlug
	}
	if planSlug != "" {
		payload["plan"] = planSlug
	}
	return map[string]any{
		statusLineExtKey: payload,
	}
}

// statusLineMeta returns the _meta value for a firSession's observable
// cards store. Returns nil when there is nothing to report.
func (s *firSession) statusLineMeta() any {
	if s == nil || s.session == nil {
		return nil
	}
	m := buildStatusLineMeta(s.session.Observables())
	if m == nil {
		return nil
	}
	return m
}

// notification builds an acpsdk.SessionNotification with the status-line
// _meta extension populated from the session's observable cards store.
func (s *firSession) notification(sid acpsdk.SessionId, update acpsdk.SessionUpdate) acpsdk.SessionNotification {
	return acpsdk.SessionNotification{
		Meta:      s.statusLineMeta(),
		SessionId: sid,
		Update:    update,
	}
}
