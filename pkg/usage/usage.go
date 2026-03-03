// Package usage tracks local feature usage counters.
//
// Data is stored in a JSON file (default: ~/.fir/agent/usage.json).
// All writes are atomic (write-to-temp + rename). Reads tolerate missing
// or corrupt files gracefully.
package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is a usage event category.
type Event string

const (
	EventSlashCommand Event = "slash_command" // /help, /model, etc.
	EventCLIFlag      Event = "cli_flag"      // --model, --print, etc.
	EventToolUse      Event = "tool_use"      // read, bash, edit, write
	EventSession      Event = "session"       // new, continue, resume
	EventMode         Event = "mode"          // text, json, rpc, acp
)

// Counter holds a count and the last-used timestamp.
type Counter struct {
	Count    int64     `json:"count"`
	LastUsed time.Time `json:"last_used"`
}

// Data is the on-disk format.
type Data struct {
	// Events maps event category → feature name → counter.
	// e.g. Events["slash_command"]["/help"] = Counter{Count: 5, ...}
	Events map[Event]map[string]*Counter `json:"events"`
}

// Tracker records feature usage to a local JSON file.
type Tracker struct {
	mu   sync.Mutex
	path string
	data *Data
}

// New creates a tracker that persists to the given file path.
// The file and parent directories are created on first write.
func New(path string) *Tracker {
	return &Tracker{path: path}
}

// DefaultPath returns the default usage file path for the given agent dir.
func DefaultPath(agentDir string) string {
	return filepath.Join(agentDir, "usage.json")
}

// Record increments the counter for the given event and feature name.
func (t *Tracker) Record(event Event, name string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.data == nil {
		t.data = t.load()
	}

	if t.data.Events == nil {
		t.data.Events = make(map[Event]map[string]*Counter)
	}
	m := t.data.Events[event]
	if m == nil {
		m = make(map[string]*Counter)
		t.data.Events[event] = m
	}
	c := m[name]
	if c == nil {
		c = &Counter{}
		m[name] = c
	}
	c.Count++
	c.LastUsed = time.Now().UTC().Truncate(time.Second)

	_ = t.save() // best-effort
}

// Get returns the counter for the given event and name, or nil.
func (t *Tracker) Get(event Event, name string) *Counter {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.data == nil {
		t.data = t.load()
	}
	if m := t.data.Events[event]; m != nil {
		return m[name]
	}
	return nil
}

// All returns a snapshot of all usage data.
func (t *Tracker) All() *Data {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.data == nil {
		t.data = t.load()
	}
	// Return a shallow copy.
	cp := &Data{Events: make(map[Event]map[string]*Counter, len(t.data.Events))}
	for ev, m := range t.data.Events {
		cm := make(map[string]*Counter, len(m))
		for k, v := range m {
			vc := *v
			cm[k] = &vc
		}
		cp.Events[ev] = cm
	}
	return cp
}

func (t *Tracker) load() *Data {
	d := &Data{Events: make(map[Event]map[string]*Counter)}
	f, err := os.ReadFile(t.path)
	if err != nil {
		return d
	}
	_ = json.Unmarshal(f, d) // tolerate corrupt data
	if d.Events == nil {
		d.Events = make(map[Event]map[string]*Counter)
	}
	return d
}

func (t *Tracker) save() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}
