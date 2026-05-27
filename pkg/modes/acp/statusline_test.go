package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/session/store"
)

func TestBuildStatusLineMeta_WithMoodAndPlan(t *testing.T) {
	obs := store.NewObservableStore("")
	obs.Put("mood", "current", "engaged", "feels productive", "tc-1")
	obs.Put("plan", "active", "3/8", "step three", "tc-2")

	got := buildStatusLineMeta(obs)
	if got == nil {
		t.Fatal("expected non-nil meta, got nil")
	}

	ext, ok := got[statusLineExtKey]
	if !ok {
		t.Fatalf("missing key %q in meta", statusLineExtKey)
	}
	payload, ok := ext.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string, got %T", ext)
	}
	if payload["mood"] != "engaged" {
		t.Errorf("mood = %q, want %q", payload["mood"], "engaged")
	}
	if payload["plan"] != "3/8" {
		t.Errorf("plan = %q, want %q", payload["plan"], "3/8")
	}
}

func TestBuildStatusLineMeta_MoodOnly(t *testing.T) {
	obs := store.NewObservableStore("")
	obs.Put("mood", "current", "calm", "", "")

	got := buildStatusLineMeta(obs)
	if got == nil {
		t.Fatal("expected non-nil meta")
	}
	payload := got[statusLineExtKey].(map[string]string)
	if payload["mood"] != "calm" {
		t.Errorf("mood = %q, want %q", payload["mood"], "calm")
	}
	if _, ok := payload["plan"]; ok {
		t.Errorf("plan should be absent, got %q", payload["plan"])
	}
}

func TestBuildStatusLineMeta_PlanOnly(t *testing.T) {
	obs := store.NewObservableStore("")
	obs.Put("plan", "active", "5/10", "", "")

	got := buildStatusLineMeta(obs)
	if got == nil {
		t.Fatal("expected non-nil meta")
	}
	payload := got[statusLineExtKey].(map[string]string)
	if payload["plan"] != "5/10" {
		t.Errorf("plan = %q, want %q", payload["plan"], "5/10")
	}
	if _, ok := payload["mood"]; ok {
		t.Errorf("mood should be absent")
	}
}

func TestBuildStatusLineMeta_EmptyStore(t *testing.T) {
	obs := store.NewObservableStore("")
	got := buildStatusLineMeta(obs)
	if got != nil {
		t.Errorf("expected nil meta for empty store, got %v", got)
	}
}

func TestBuildStatusLineMeta_NilStore(t *testing.T) {
	got := buildStatusLineMeta(nil)
	if got != nil {
		t.Errorf("expected nil meta for nil store, got %v", got)
	}
}

func TestBuildStatusLineMeta_NonMoodPlanSourcesIgnored(t *testing.T) {
	obs := store.NewObservableStore("")
	obs.Put("custom", "status", "running", "", "")

	got := buildStatusLineMeta(obs)
	if got != nil {
		t.Errorf("expected nil meta when only non-mood/plan sources present, got %v", got)
	}
}

func TestBuildStatusLineMeta_PicksLatestPerSource(t *testing.T) {
	obs := store.NewObservableStore("")
	// Put two entries for mood with different keys — latest Ts wins
	obs.Put("mood", "old", "stale", "", "")
	obs.Put("mood", "current", "fresh", "", "")

	got := buildStatusLineMeta(obs)
	if got == nil {
		t.Fatal("expected non-nil meta")
	}
	payload := got[statusLineExtKey].(map[string]string)
	// List() returns (Source asc, Ts desc) so the most recent Ts wins.
	if payload["mood"] != "fresh" {
		t.Errorf("mood = %q, want %q (latest)", payload["mood"], "fresh")
	}
}

func TestBuildStatusLineMeta_EmptySlugsOmitted(t *testing.T) {
	obs := store.NewObservableStore("")
	// mood with empty slug and plan with empty slug → nil meta
	obs.Put("mood", "current", "", "", "")
	obs.Put("plan", "active", "", "", "")

	got := buildStatusLineMeta(obs)
	if got != nil {
		t.Errorf("expected nil meta when all slugs are empty, got %v", got)
	}
}

func TestFirSession_NotificationWithMeta(t *testing.T) {
	// Build a minimal firSession with an observable store containing
	// mood+plan cards, then verify the notification helper stamps _meta.
	obs := store.NewObservableStore("")
	obs.Put("mood", "current", "engaged", "", "")
	obs.Put("plan", "active", "3/8", "", "")

	// We can't easily construct a full *session.AgentSession in a unit
	// test, so test buildStatusLineMeta → notification path by calling
	// buildStatusLineMeta directly and checking the notification struct.
	meta := buildStatusLineMeta(obs)
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}

	n := acpsdk.SessionNotification{
		Meta:      meta,
		SessionId: "test-session",
		Update:    acpsdk.UpdateAgentMessageText("hello"),
	}

	// Verify the meta is set correctly.
	metaMap, ok := n.Meta.(map[string]any)
	if !ok {
		t.Fatalf("Meta type = %T, want map[string]any", n.Meta)
	}
	ext, ok := metaMap[statusLineExtKey]
	if !ok {
		t.Fatalf("missing %q in Meta", statusLineExtKey)
	}
	payload, ok := ext.(map[string]string)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]string", ext)
	}
	if payload["mood"] != "engaged" || payload["plan"] != "3/8" {
		t.Errorf("payload = %v, want mood=engaged plan=3/8", payload)
	}
}

func TestFirSession_NotificationWithoutMeta(t *testing.T) {
	// Empty observable store → buildStatusLineMeta returns nil.
	obs := store.NewObservableStore("")
	meta := buildStatusLineMeta(obs)
	if meta != nil {
		t.Errorf("expected nil meta for empty store, got %v", meta)
	}
	// The notification helper on firSession with a nil session also returns nil.
	s := &firSession{}
	n := s.notification("test-session", acpsdk.UpdateAgentMessageText("hello"))
	if n.Meta != nil {
		t.Errorf("expected nil Meta on notification with nil session, got %v", n.Meta)
	}
}
