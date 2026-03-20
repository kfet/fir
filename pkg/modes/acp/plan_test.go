package acp

import (
	"context"
	"sync"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/kfet/fir/pkg/agent"
)

// recordingConn captures SessionUpdate calls for testing.
type recordingConn struct {
	mu      sync.Mutex
	updates []acpsdk.SessionNotification
}

func (r *recordingConn) SessionUpdate(_ context.Context, n acpsdk.SessionNotification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, n)
	return nil
}

func (r *recordingConn) last() acpsdk.SessionNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updates[len(r.updates)-1]
}

func TestPlanTracker_Update(t *testing.T) {
	conn := &recordingConn{}
	pt := &planTracker{conn: conn, sessionID: "s1"}

	entries := []agent.PlanEntry{
		{Content: "Step 1", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Step 2", Status: agent.PlanEntryStatusCompleted, Priority: agent.PlanEntryPriorityMedium},
	}
	pt.update(entries)

	got := conn.last()
	if string(got.SessionId) != "s1" {
		t.Fatalf("session id = %q, want s1", got.SessionId)
	}
	if got.Update.Plan == nil {
		t.Fatal("expected plan update, got nil")
	}
	if len(got.Update.Plan.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Update.Plan.Entries))
	}
	if got.Update.Plan.Entries[0].Content != "Step 1" {
		t.Fatalf("content = %q, want Step 1", got.Update.Plan.Entries[0].Content)
	}
	if got.Update.Plan.Entries[0].Status != acpsdk.PlanEntryStatusPending {
		t.Fatalf("status = %q, want pending", got.Update.Plan.Entries[0].Status)
	}
	if got.Update.Plan.Entries[1].Priority != acpsdk.PlanEntryPriorityMedium {
		t.Fatalf("priority = %q, want medium", got.Update.Plan.Entries[1].Priority)
	}
}

func TestPlanTracker_Clear(t *testing.T) {
	conn := &recordingConn{}
	pt := &planTracker{conn: conn, sessionID: "s1"}

	pt.clear()

	got := conn.last()
	if got.Update.Plan == nil {
		t.Fatal("expected plan update, got nil")
	}
	if got.Update.Plan.Entries == nil {
		t.Fatal("entries must not be nil (would serialize as null)")
	}
	if len(got.Update.Plan.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(got.Update.Plan.Entries))
	}
}

func TestPlanTracker_NilSafe(t *testing.T) {
	// Should not panic.
	var pt *planTracker
	pt.update([]agent.PlanEntry{{Content: "x"}})
	pt.clear()
}
