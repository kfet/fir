package tools

import (
	"context"
	"testing"

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
	mock := &mockPlanUpdater{}
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
	mock := &mockPlanUpdater{}
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
	mock := &mockPlanUpdater{}
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
	mock := &mockPlanUpdater{}
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
	mock := &mockPlanUpdater{}
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
