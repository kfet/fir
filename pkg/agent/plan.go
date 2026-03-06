package agent

// PlanEntryStatus represents the status of a plan entry.
type PlanEntryStatus string

const (
	PlanEntryStatusPending    PlanEntryStatus = "pending"
	PlanEntryStatusInProgress PlanEntryStatus = "in_progress"
	PlanEntryStatusCompleted  PlanEntryStatus = "completed"
)

// PlanEntryPriority represents the priority of a plan entry.
type PlanEntryPriority string

const (
	PlanEntryPriorityHigh   PlanEntryPriority = "high"
	PlanEntryPriorityMedium PlanEntryPriority = "medium"
	PlanEntryPriorityLow    PlanEntryPriority = "low"
)

// PlanEntry represents a single entry in a plan.
type PlanEntry struct {
	Content  string            `json:"content"`
	Status   PlanEntryStatus   `json:"status"`
	Priority PlanEntryPriority `json:"priority"`
}

// ValidStatus reports whether s is a known plan entry status.
func ValidStatus(s string) bool {
	switch PlanEntryStatus(s) {
	case PlanEntryStatusPending, PlanEntryStatusInProgress, PlanEntryStatusCompleted:
		return true
	}
	return false
}

// ValidPriority reports whether p is a known plan entry priority.
func ValidPriority(p string) bool {
	switch PlanEntryPriority(p) {
	case PlanEntryPriorityHigh, PlanEntryPriorityMedium, PlanEntryPriorityLow:
		return true
	}
	return false
}
