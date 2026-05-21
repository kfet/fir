package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/session/store"
)

// mockPlanUpdater records UpdatePlan calls and shares an in-memory
// observable cards store with the tool so tests can assert on the
// "plan/active" card that the tool publishes.
type mockPlanUpdater struct {
	title    string
	entries  []agent.PlanEntry
	metadata map[string]string
	calls    int
	store    *store.ObservableStore
}

func newMockPlanUpdater() *mockPlanUpdater {
	return &mockPlanUpdater{store: store.NewObservableStore("")}
}

func (m *mockPlanUpdater) UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string) {
	m.title = title
	m.entries = entries
	m.metadata = metadata
	m.calls++
}

func (m *mockPlanUpdater) Observables() *store.ObservableStore {
	return m.store
}

func TestPlanTool_Basic(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	if tool.Name != "plan" {
		t.Fatalf("name = %q, want plan", tool.Name)
	}

	result, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"entries": []any{
			map[string]any{"content": "step 1", "status": "pending", "priority": "high"},
			map[string]any{"content": "step 2", "status": "in_progress", "priority": "medium"},
		},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("unexpected error result")
	}
	if mock.calls != 1 {
		t.Fatalf("calls = %d, want 1", mock.calls)
	}
	if len(mock.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(mock.entries))
	}
	if mock.entries[0].Content != "step 1" || mock.entries[0].Status != "pending" || mock.entries[0].Priority != "high" {
		t.Errorf("entry 0 = %+v", mock.entries[0])
	}
	if mock.entries[1].Status != "in_progress" {
		t.Errorf("entry 1 status = %q", mock.entries[1].Status)
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestPlanTool_EmptyEntries(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	result, err := tool.Execute(context.Background(), "tc2", map[string]any{
		"entries": []any{},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if mock.calls != 1 || len(mock.entries) != 0 {
		t.Fatalf("calls=%d entries=%d", mock.calls, len(mock.entries))
	}
	if result.Content[0].Text != "Plan cleared." {
		t.Errorf("text = %q", result.Content[0].Text)
	}
}

func TestPlanTool_InvalidStatus(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	result, err := tool.Execute(context.Background(), "tc3", map[string]any{
		"entries": []any{
			map[string]any{"content": "step", "status": "bogus", "priority": "bogus"},
		},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	// Invalid values default to pending/medium
	if mock.entries[0].Status != "pending" {
		t.Errorf("status = %q, want pending", mock.entries[0].Status)
	}
	if mock.entries[0].Priority != "medium" {
		t.Errorf("priority = %q, want medium", mock.entries[0].Priority)
	}
	_ = result
}

func TestPlanTool_MissingContent(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	result, err := tool.Execute(context.Background(), "tc4", map[string]any{
		"entries": []any{
			map[string]any{"status": "pending", "priority": "high"},
		},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing content")
	}
	if mock.calls != 0 {
		t.Error("should not have called UpdatePlan")
	}
}

func TestPlanTool_NoEntriesParam(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	result, err := tool.Execute(context.Background(), "tc5", map[string]any{}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Error("unexpected error")
	}
	// nil entries → clears plan
	if mock.calls != 1 {
		t.Fatalf("calls = %d", mock.calls)
	}
}

func TestPlanTool_EntriesNotArray(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	result, err := tool.Execute(context.Background(), "tc6", map[string]any{
		"entries": "not an array",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for non-array entries")
	}
	if mock.calls != 0 {
		t.Fatalf("calls = %d, want 0", mock.calls)
	}
}

// --- Observable card publishing tests --------------------------------

func TestPlanTool_PublishesCardOnUpdate(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	_, err := tool.Execute(context.Background(), "tc-publish", map[string]any{
		"title": "Wire feature X",
		"metadata": map[string]any{
			"progress_metric": "endpoints migrated 1/3",
		},
		"entries": []any{
			map[string]any{"content": "design", "status": "completed", "priority": "high"},
			map[string]any{"content": "wire api", "status": "in_progress", "priority": "high"},
			map[string]any{"content": "tests", "status": "pending", "priority": "medium"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	cards := mock.store.List()
	if len(cards) != 1 {
		t.Fatalf("expected 1 plan card, got %d: %#v", len(cards), cards)
	}
	c := cards[0]
	if c.Source != "plan" || c.Key != "active" {
		t.Errorf("card address = (%s,%s); want (plan,active)", c.Source, c.Key)
	}
	if c.Slug != "endpoints migrated 1/3" {
		t.Errorf("slug = %q; want progress_metric value", c.Slug)
	}
	if c.EntryID != "tc-publish" {
		t.Errorf("entry_id = %q; want tool-call id tc-publish", c.EntryID)
	}
	// Detail must include the title and every entry's content.
	if !strings.Contains(c.Detail, "Wire feature X") {
		t.Errorf("detail missing title:\n%s", c.Detail)
	}
	for _, frag := range []string{"design", "wire api", "tests"} {
		if !strings.Contains(c.Detail, frag) {
			t.Errorf("detail missing %q:\n%s", frag, c.Detail)
		}
	}
	// progress_metric drives the slug, not the detail — detail is the
	// pure bullet listing per the design.
	if strings.Contains(c.Detail, "progress_metric") {
		t.Errorf("detail unexpectedly contains metadata:\n%s", c.Detail)
	}
}

func TestPlanTool_PublishesCardSlugFallback(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	// No progress_metric → slug falls back to "<done>/<total> <status>".
	_, err := tool.Execute(context.Background(), "tc", map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "status": "completed", "priority": "high"},
			map[string]any{"content": "b", "status": "in_progress", "priority": "high"},
			map[string]any{"content": "c", "status": "pending", "priority": "medium"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	cards := mock.store.List()
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].Slug != "1/3 in_progress" {
		t.Errorf("slug = %q; want '1/3 in_progress'", cards[0].Slug)
	}
}

func TestPlanTool_PublishesCardSlugDone(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)
	_, err := tool.Execute(context.Background(), "tc", map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "status": "completed", "priority": "high"},
			map[string]any{"content": "b", "status": "completed", "priority": "high"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cards := mock.store.List()
	if len(cards) != 1 || cards[0].Slug != "2/2 done" {
		t.Fatalf("slug = %q; want '2/2 done'", cards[0].Slug)
	}
}

func TestPlanTool_ClearsCardOnEmptyEntries(t *testing.T) {
	mock := newMockPlanUpdater()
	tool := NewPlanTool(mock)

	// Seed a card via a normal call.
	if _, err := tool.Execute(context.Background(), "tc1", map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "status": "pending", "priority": "high"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(mock.store.List()) != 1 {
		t.Fatal("expected card after first call")
	}

	// Now clear.
	if _, err := tool.Execute(context.Background(), "tc2", map[string]any{
		"entries": []any{},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := mock.store.List(); len(got) != 0 {
		t.Errorf("expected card cleared, got %#v", got)
	}
}

// TestPlanTool_NilObservablesIsSafe pins the design guarantee that a
// session without an observable store still functions — the plan tool
// just skips the card publish.
func TestPlanTool_NilObservablesIsSafe(t *testing.T) {
	mock := &mockPlanUpdater{} // store == nil
	tool := NewPlanTool(mock)
	_, err := tool.Execute(context.Background(), "tc", map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "status": "pending", "priority": "high"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("plan tool must tolerate nil observables: %v", err)
	}
}
