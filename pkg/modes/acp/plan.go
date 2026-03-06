package acp

import (
	"context"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/kfet/fir/pkg/agent"
)

// planConn is the subset of acpConn needed by planTracker.
type planConn interface {
	SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error
}

// planTracker converts agent plan entries to ACP plan updates.
type planTracker struct {
	conn      planConn
	sessionID string
}

// update converts agent plan entries to ACP format and sends an UpdatePlan notification.
func (p *planTracker) update(entries []agent.PlanEntry) {
	if p == nil || p.conn == nil {
		return
	}
	acpEntries := make([]acpsdk.PlanEntry, len(entries))
	for i, e := range entries {
		acpEntries[i] = acpsdk.PlanEntry{
			Content:  e.Content,
			Status:   acpsdk.PlanEntryStatus(e.Status),
			Priority: acpsdk.PlanEntryPriority(e.Priority),
		}
	}
	_ = p.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(p.sessionID),
		Update:    acpsdk.UpdatePlan(acpEntries...),
	})
}

// clear sends an empty plan update to clear the plan.
func (p *planTracker) clear() {
	if p == nil || p.conn == nil {
		return
	}
	_ = p.conn.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: acpsdk.SessionId(p.sessionID),
		Update:    acpsdk.UpdatePlan(),
	})
}
