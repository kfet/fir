package usage

// SessionTracker adapts a Tracker to the core.UsageTracker interface.
type SessionTracker struct {
	t *Tracker
}

// NewSessionTracker wraps a Tracker for use as a core.UsageTracker.
func NewSessionTracker(t *Tracker) *SessionTracker {
	return &SessionTracker{t: t}
}

// RecordToolUse records a tool use event.
func (st *SessionTracker) RecordToolUse(toolName string) {
	st.t.Record(EventToolUse, toolName)
}

// RecordSlashCommand records a slash command event.
func (st *SessionTracker) RecordSlashCommand(name string) {
	st.t.Record(EventSlashCommand, name)
}
