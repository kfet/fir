// Package permq queues permission_request notifications from fir and
// drains them into the next Poe reply for the affected user.
//
// Fir emits notifications/claude/channel/permission_request when a tool
// needs approval. Since Poe has no server→user push, we queue the request
// and prepend it to the user's next SSE response.
package permq

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Request is one queued permission prompt.
type Request struct {
	RequestID   string    `json:"request_id"`
	ToolName    string    `json:"tool_name"`
	Description string    `json:"description"`
	QueuedAt    time.Time `json:"queued_at"`
}

// Queue holds pending permission requests per user_id. Thread-safe.
// Optionally persists to disk so requests survive bridge restarts.
type Queue struct {
	mu    sync.Mutex
	items map[string][]Request // keyed by user_id
	dir   string               // optional: directory for on-disk persistence
}

// New creates a Queue. If dir is non-empty, the queue persists to
// <dir>/permq/<user_id>.json. If dir is empty, the queue is in-memory only.
func New(dir string) *Queue {
	q := &Queue{items: make(map[string][]Request), dir: dir}
	if dir != "" {
		q.loadAll()
	}
	return q
}

// Enqueue adds a permission request for the given user.
func (q *Queue) Enqueue(userID string, r Request) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items[userID] = append(q.items[userID], r)
	q.flush(userID)
}

// Drain returns and removes all queued requests for the given user.
// Returns nil if there are none.
func (q *Queue) Drain(userID string) []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	reqs := q.items[userID]
	if len(reqs) == 0 {
		return nil
	}
	delete(q.items, userID)
	q.flush(userID)
	return reqs
}

// Len returns the number of queued requests for a user.
func (q *Queue) Len(userID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items[userID])
}

func (q *Queue) flush(userID string) {
	if q.dir == "" {
		return
	}
	d := filepath.Join(q.dir, "permq")
	os.MkdirAll(d, 0o700)
	p := filepath.Join(d, userID+".json")
	reqs := q.items[userID]
	if len(reqs) == 0 {
		os.Remove(p)
		return
	}
	raw, _ := json.MarshalIndent(reqs, "", "  ")
	os.WriteFile(p, raw, 0o600)
}

func (q *Queue) loadAll() {
	d := filepath.Join(q.dir, "permq")
	entries, err := os.ReadDir(d)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 6 || name[len(name)-5:] != ".json" {
			continue
		}
		userID := name[:len(name)-5]
		raw, err := os.ReadFile(filepath.Join(d, name))
		if err != nil {
			continue
		}
		var reqs []Request
		if json.Unmarshal(raw, &reqs) == nil && len(reqs) > 0 {
			q.items[userID] = reqs
		}
	}
}

// FormatDrain drains the queue for userID and returns a formatted text
// block suitable for prepending to an SSE text event. Returns "" if empty.
func (q *Queue) FormatDrain(userID string) string {
	reqs := q.Drain(userID)
	if len(reqs) == 0 {
		return ""
	}
	s := fmt.Sprintf("🔐 **%d pending permission request(s):**\n\n", len(reqs))
	for _, r := range reqs {
		s += fmt.Sprintf("- **%s**: %s (id: `%s`)\n", r.ToolName, r.Description, r.RequestID)
	}
	s += "\nReply `/allow <id>` or `/deny <id>` to respond.\n\n---\n\n"
	return s
}
