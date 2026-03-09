package tools

import (
	"context"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
)

// mockPlanUpdater records UpdatePlan calls.
type mockPlanUpdater struct {
	title    string
	entries  []agent.PlanEntry
	metadata map[string]string
	calls    int
}

func (m *mockPlanUpdater) UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string) {
	m.title = title
	m.entries = entries
	m.metadata = metadata
	m.calls++
}

func TestPlanTool_Basic(t *testing.T) {
	mock := &mockPlanUpdater{}
	tool := NewPlanTool(mock, nil)

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
	mock := &mockPlanUpdater{}
	tool := NewPlanTool(mock, nil)

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
	mock := &mockPlanUpdater{}
	tool := NewPlanTool(mock, nil)

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
	mock := &mockPlanUpdater{}
	tool := NewPlanTool(mock, nil)

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
	mock := &mockPlanUpdater{}
	tool := NewPlanTool(mock, nil)

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
	mock := &mockPlanUpdater{}
	tool := NewPlanTool(mock, nil)

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

// ---------------------------------------------------------------------------
// PlanNudger tests
// ---------------------------------------------------------------------------

func TestPlanNudger_FiresAfterTurnThreshold(t *testing.T) {
	active := true
	n := NewPlanNudger(func() bool { return active })

	// Turns 1-19: no nudge
	for i := 0; i < 19; i++ {
		n.RecordTurn()
		if msg := n.Check(); msg != "" {
			t.Fatalf("unexpected nudge after turn %d", i+1)
		}
	}

	// Turn 20: should nudge
	n.RecordTurn()
	if msg := n.Check(); msg == "" {
		t.Fatal("expected nudge after 20 turns")
	}

	// Counter was reset, so next check should not nudge
	if msg := n.Check(); msg != "" {
		t.Fatal("nudge should not fire immediately after reset")
	}
}

func TestPlanNudger_FiresAfterTimeout(t *testing.T) {
	n := NewPlanNudger(func() bool { return true })

	// Fake the last-update time to be 3 minutes ago
	n.mu.Lock()
	n.lastUpdate = n.lastUpdate.Add(-3 * time.Minute)
	n.mu.Unlock()

	if msg := n.Check(); msg == "" {
		t.Fatal("expected nudge when plan has not been updated for 3 minutes")
	}
}

func TestPlanNudger_NoFireBeforeTimeout(t *testing.T) {
	n := NewPlanNudger(func() bool { return true })

	// 30 seconds ago — below threshold
	n.mu.Lock()
	n.lastUpdate = n.lastUpdate.Add(-30 * time.Second)
	n.mu.Unlock()

	if msg := n.Check(); msg != "" {
		t.Fatal("should not nudge when plan was updated 30s ago")
	}
}

func TestPlanNudger_CheckOnEnd_FiresWhenPlanActive(t *testing.T) {
	n := NewPlanNudger(func() bool { return true })
	if msg := n.CheckOnEnd(); msg == "" {
		t.Fatal("CheckOnEnd should nudge when there is an active incomplete plan")
	}
}

func TestPlanNudger_CheckOnEnd_NoFireWithoutActivePlan(t *testing.T) {
	n := NewPlanNudger(func() bool { return false })
	if msg := n.CheckOnEnd(); msg != "" {
		t.Fatal("CheckOnEnd should not nudge when no active plan")
	}
}

func TestPlanNudger_NoNudgeWithoutActivePlan(t *testing.T) {
	n := NewPlanNudger(func() bool { return false })
	n.RecordTurn()
	if msg := n.Check(); msg != "" {
		t.Fatal("should not nudge when no active plan")
	}
}

func TestPlanNudger_ResetOnPlanUpdate(t *testing.T) {
	n := NewPlanNudger(func() bool { return true })

	// Fill up 19 turns
	for i := 0; i < 19; i++ {
		n.RecordTurn()
	}
	n.RecordPlanUpdate() // reset

	// Need another 20 turns after the reset
	n.RecordTurn()
	if msg := n.Check(); msg != "" {
		t.Fatal("should not nudge — counter was reset by plan update")
	}

	for i := 1; i < 20; i++ {
		n.RecordTurn()
	}
	if msg := n.Check(); msg == "" {
		t.Fatal("expected nudge after 20 turns since last update")
	}
}

func TestPlanNudger_NudgerResetsCounter(t *testing.T) {
	mock := &mockPlanUpdater{}
	n := NewPlanNudger(func() bool { return true })
	tool := NewPlanTool(mock, n)

	// Simulate 20 turns (threshold)
	for i := 0; i < 20; i++ {
		n.RecordTurn()
	}

	// Calling the plan tool should reset
	_, _ = tool.Execute(context.Background(), "tc", map[string]any{
		"entries": []any{
			map[string]any{"content": "step", "status": "pending", "priority": "high"},
		},
	}, nil)

	// Counter should be reset — need interval turns again
	if msg := n.Check(); msg != "" {
		t.Fatal("should not nudge immediately after plan tool call")
	}
}
