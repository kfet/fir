package tools

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

// fakePlanSink records UpdatePlan calls so tests can assert on what
// the plan tool committed.
type fakePlanSink struct {
	title    string
	entries  []agent.PlanEntry
	metadata map[string]string
	calls    int
}

func (f *fakePlanSink) UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string) {
	f.title = title
	f.entries = entries
	f.metadata = metadata
	f.calls++
}

// recordedPublish captures one invocation of the CardPublisher.
type recordedPublish struct {
	title    string
	entries  []agent.PlanEntry
	metadata map[string]string
	entryID  string
}

// newRecordingPublisher returns a CardPublisher that appends every
// call to *out so tests can inspect publishing behaviour without
// depending on the host card store.
func newRecordingPublisher(out *[]recordedPublish) CardPublisher {
	return func(title string, entries []agent.PlanEntry, metadata map[string]string, entryID string) {
		*out = append(*out, recordedPublish{title, entries, metadata, entryID})
	}
}

// TestPlanTool_CompactWireForm pins that the plan tool accepts the compressed
// wire form (short keys c/p/s + short enum codes) and expands it to canonical
// full-name entries (transcript-footprint P3).
func TestPlanTool_CompactWireForm(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	_, err := tool.Execute(context.Background(), "tc-compact", map[string]any{
		"entries": []any{
			map[string]any{"c": "step 1", "p": "h", "s": "i"},
			map[string]any{"c": "step 2", "p": "l", "s": "x"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(sink.entries))
	}
	if sink.entries[0].Content != "step 1" ||
		sink.entries[0].Priority != agent.PlanEntryPriorityHigh ||
		sink.entries[0].Status != agent.PlanEntryStatusInProgress {
		t.Errorf("entry 0 = %+v", sink.entries[0])
	}
	if sink.entries[1].Priority != agent.PlanEntryPriorityLow ||
		sink.entries[1].Status != agent.PlanEntryStatusCompleted {
		t.Errorf("entry 1 = %+v", sink.entries[1])
	}
}

// TestPlanTool_TolerantOldKeys pins that the full-keyed/full-enum form (old
// transcripts) still decodes unchanged alongside the new compact form.
func TestPlanTool_TolerantOldKeys(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	_, err := tool.Execute(context.Background(), "tc-old", map[string]any{
		"entries": []any{
			map[string]any{"content": "old", "priority": "medium", "status": "pending"},
			map[string]any{"c": "new", "p": "h", "s": "x"}, // mixed in same call
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(sink.entries))
	}
	if sink.entries[0].Content != "old" || sink.entries[0].Priority != agent.PlanEntryPriorityMedium {
		t.Errorf("entry 0 = %+v", sink.entries[0])
	}
	if sink.entries[1].Content != "new" || sink.entries[1].Status != agent.PlanEntryStatusCompleted {
		t.Errorf("entry 1 = %+v", sink.entries[1])
	}
}

// TestPlanTool_CompactMissingContentFailsClosed pins that a compact entry with
// no content (c) is still rejected — validation runs on the canonical form.
func TestPlanTool_CompactMissingContentFailsClosed(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	result, err := tool.Execute(context.Background(), "tc-bad", map[string]any{
		"entries": []any{
			map[string]any{"p": "h", "s": "i"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing content")
	}
	if sink.calls != 0 {
		t.Error("should not have committed plan")
	}
}

func TestPlanTool_Basic(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

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
	if sink.calls != 1 {
		t.Fatalf("calls = %d, want 1", sink.calls)
	}
	if len(sink.entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(sink.entries))
	}
	if sink.entries[0].Content != "step 1" || sink.entries[0].Status != agent.PlanEntryStatusPending || sink.entries[0].Priority != agent.PlanEntryPriorityHigh {
		t.Errorf("entry 0 = %+v", sink.entries[0])
	}
	if sink.entries[1].Status != agent.PlanEntryStatusInProgress {
		t.Errorf("entry 1 status = %q", sink.entries[1].Status)
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected non-empty result text")
	}
}

func TestPlanTool_EmptyEntries(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	result, err := tool.Execute(context.Background(), "tc2", map[string]any{
		"entries": []any{},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if sink.calls != 1 || len(sink.entries) != 0 {
		t.Fatalf("calls=%d entries=%d", sink.calls, len(sink.entries))
	}
	if result.Content[0].Text != "Plan cleared." {
		t.Errorf("text = %q", result.Content[0].Text)
	}
}

func TestPlanTool_InvalidStatus(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	_, err := tool.Execute(context.Background(), "tc3", map[string]any{
		"entries": []any{
			map[string]any{"content": "step", "status": "bogus", "priority": "bogus"},
		},
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if sink.entries[0].Status != agent.PlanEntryStatusPending {
		t.Errorf("status = %q, want pending", sink.entries[0].Status)
	}
	if sink.entries[0].Priority != agent.PlanEntryPriorityMedium {
		t.Errorf("priority = %q, want medium", sink.entries[0].Priority)
	}
}

func TestPlanTool_MissingContent(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

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
	if sink.calls != 0 {
		t.Error("should not have called UpdatePlan")
	}
}

func TestPlanTool_NoEntriesParam(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	result, err := tool.Execute(context.Background(), "tc5", map[string]any{}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Error("unexpected error")
	}
	if sink.calls != 1 {
		t.Fatalf("calls = %d", sink.calls)
	}
}

func TestPlanTool_EntriesNotArray(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)

	result, err := tool.Execute(context.Background(), "tc6", map[string]any{
		"entries": "not an array",
	}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error for non-array entries")
	}
	if sink.calls != 0 {
		t.Fatalf("calls = %d, want 0", sink.calls)
	}
}

// --- publisher behaviour ---------------------------------------------

// TestPlanTool_InvokesPublisherOnUpdate pins the contract that on every
// successful plan mutation, the publisher (when non-nil) is invoked with
// the same title/entries/metadata as the sink, plus the tool-call id.
func TestPlanTool_InvokesPublisherOnUpdate(t *testing.T) {
	sink := &fakePlanSink{}
	var pubs []recordedPublish
	tool := NewPlanTool(sink, newRecordingPublisher(&pubs))

	_, err := tool.Execute(context.Background(), "tc-publish", map[string]any{
		"title": "Wire feature X",
		"metadata": map[string]any{
			"progress_metric": "endpoints migrated 1/3",
		},
		"entries": []any{
			map[string]any{"content": "design", "status": "completed", "priority": "high"},
			map[string]any{"content": "wire api", "status": "in_progress", "priority": "high"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(pubs) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(pubs))
	}
	p := pubs[0]
	if p.title != "Wire feature X" {
		t.Errorf("publisher title = %q", p.title)
	}
	if p.entryID != "tc-publish" {
		t.Errorf("publisher entryID = %q", p.entryID)
	}
	if len(p.entries) != 2 || p.entries[0].Content != "design" {
		t.Errorf("publisher entries = %+v", p.entries)
	}
	if p.metadata["progress_metric"] != "endpoints migrated 1/3" {
		t.Errorf("publisher metadata = %+v", p.metadata)
	}
}

// TestPlanTool_InvokesPublisherOnEmpty pins that clearing the plan
// still notifies the publisher — host code uses that signal to clear
// its own state.
func TestPlanTool_InvokesPublisherOnEmpty(t *testing.T) {
	sink := &fakePlanSink{}
	var pubs []recordedPublish
	tool := NewPlanTool(sink, newRecordingPublisher(&pubs))

	_, err := tool.Execute(context.Background(), "tc-clear", map[string]any{
		"entries": []any{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pubs) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(pubs))
	}
	if len(pubs[0].entries) != 0 {
		t.Errorf("publisher entries on clear = %d, want 0", len(pubs[0].entries))
	}
}

// TestPlanTool_NilPublisherIsSafe pins the design guarantee that the
// plan tool functions without a publisher — useful for embedded or
// non-fir consumers that have no card store.
func TestPlanTool_NilPublisherIsSafe(t *testing.T) {
	sink := &fakePlanSink{}
	tool := NewPlanTool(sink, nil)
	_, err := tool.Execute(context.Background(), "tc", map[string]any{
		"entries": []any{
			map[string]any{"content": "a", "status": "pending", "priority": "high"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("plan tool must tolerate nil publisher: %v", err)
	}
}

// TestPlanTool_DoesNotInvokePublisherOnParseError pins that the
// publisher is only called when the tool actually commits a plan.
func TestPlanTool_DoesNotInvokePublisherOnParseError(t *testing.T) {
	sink := &fakePlanSink{}
	var pubs []recordedPublish
	tool := NewPlanTool(sink, newRecordingPublisher(&pubs))

	_, err := tool.Execute(context.Background(), "tc", map[string]any{
		"entries": []any{
			map[string]any{"status": "pending", "priority": "high"}, // missing content
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pubs) != 0 {
		t.Errorf("publisher invoked despite parse error: %+v", pubs)
	}
	if sink.calls != 0 {
		t.Errorf("sink called despite parse error: %d", sink.calls)
	}
}
