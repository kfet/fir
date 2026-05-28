// Package store — observable cards.
//
// Observable cards are a per-session sidecar of state summaries that
// producers (extensions, the plan tool, core) publish through a single
// primitive. See docs/design/observable-cards.md for the design;
// this file implements the storage half — addressing (Source, Key),
// host-stamped Ts/EntryID, atomic temp+rename file persistence, and
// rune-safe slug truncation. Trust seam (host-stamped source/EntryID)
// lives in pkg/extension/bridge.go.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// SlugMaxLen is the host-enforced upper bound on Card.Slug. Producers
// hand us free-form text; we truncate here so consumers can lay out
// fixed-width headers without re-trimming.
const SlugMaxLen = 24

// Card is a single observable state summary. It is intentionally tiny
// and append-friendly. See docs/design/observable-cards.md "Data model".
type Card struct {
	Source  string    `json:"source"`             // stamped by host
	Key     string    `json:"key"`                // producer-chosen, namespaced within source
	Slug    string    `json:"slug"`               // ≤ SlugMaxLen, host-rendered headers
	Detail  string    `json:"detail"`             // pre-rendered plain text
	Ts      time.Time `json:"ts"`                 // stamped by host
	EntryID string    `json:"entry_id,omitempty"` // transcript entry id, stamped by host
}

// ObservableStore is the session-scoped sidecar of cards. Cards are keyed
// by (Source, Key); Put overwrites in place, Clear removes. List returns
// a snapshot ordered by (Source asc, Ts desc) so consumers get a stable
// shape without re-sorting.
//
// All mutations are synchronously persisted to <sessionFile>.cards via
// atomic temp+rename. No debounce in MVP (see design doc).
//
// A nil *ObservableStore is a valid no-op — Put / Clear silently do
// nothing and List returns nil. Consumers don't have to guard.
type ObservableStore struct {
	mu    sync.RWMutex
	cards map[cardKey]Card
	path  string // "" → in-memory only (no persistence)

	// flushMu serialises file write+rename so concurrent Puts can't
	// reorder their renames and lose the latest snapshot to disk.
	// Without this, two flushes can race like: A snapshots [c1],
	// B snapshots [c1,c2], B renames first, A renames last — A's
	// older snapshot wins on disk and c2 vanishes even though it
	// stays in memory. flushMu makes the take-snapshot/write/rename
	// triple atomic with respect to other flushes; the main mu is
	// still released so Put/Clear/List remain non-blocking.
	flushMu sync.Mutex

	// lastTs tracks the most recent Card.Ts stamped by Put. Two Puts
	// in rapid succession can read the same wall-clock time (macOS
	// time resolution, fast hardware) and break List's "latest wins"
	// ordering. Bumping equal/earlier timestamps by 1ns gives Put
	// strictly monotonic Ts without exposing the bump to callers.
	// Guarded by mu.
	lastTs time.Time
}

type cardKey struct{ source, key string }

// NewObservableStore returns a store bound to path. If path is "", the
// store is in-memory only (no file I/O). If path points to an existing
// cards file, it is loaded so reexec / resume see last-known state
// before any bridge handshakes complete.
func NewObservableStore(path string) *ObservableStore {
	s := &ObservableStore{
		cards: make(map[cardKey]Card),
		path:  path,
	}
	if path != "" {
		s.loadFromFile()
	}
	return s
}

// CardsPath returns the suggested cards-file path for a given session
// transcript path (<sessionFile>.cards). Returns "" when sessionFile is
// empty (in-memory session).
func CardsPath(sessionFile string) string {
	if sessionFile == "" {
		return ""
	}
	return sessionFile + ".cards"
}

// Put inserts or replaces the card identified by (source, key). The
// caller is responsible for picking source — in-process core callers
// choose their own; extension RPCs go through bridge.go which stamps
// source from the calling extension's name. Same applies to entryID.
//
// Empty source or key is rejected — both are needed for addressing.
// Empty slug is allowed (set_status uses it to mean "clear footer
// text while still anchoring the card to an entry").
func (s *ObservableStore) Put(source, key, slug, detail, entryID string) {
	if s == nil || source == "" || key == "" {
		return
	}
	if len(slug) > SlugMaxLen {
		// Truncation respects rune boundaries so multibyte slugs (e.g.
		// emoji tags) don't end up with a torn final rune. Byte-slicing
		// would corrupt UTF-8 occasionally.
		slug = truncateRunes(slug, SlugMaxLen)
	}
	card := Card{
		Source:  source,
		Key:     key,
		Slug:    slug,
		Detail:  detail,
		Ts:      time.Now().UTC(),
		EntryID: entryID,
	}
	s.mu.Lock()
	if !card.Ts.After(s.lastTs) {
		card.Ts = s.lastTs.Add(time.Nanosecond)
	}
	s.lastTs = card.Ts
	s.cards[cardKey{source, key}] = card
	s.mu.Unlock()
	s.flush()
}

// Clear removes the card identified by (source, key). No-op if absent.
func (s *ObservableStore) Clear(source, key string) {
	if s == nil || source == "" || key == "" {
		return
	}
	s.mu.Lock()
	_, had := s.cards[cardKey{source, key}]
	if had {
		delete(s.cards, cardKey{source, key})
	}
	s.mu.Unlock()
	if had {
		s.flush()
	}
}

// List returns a snapshot of all cards, ordered by (Source asc, Ts desc)
// so consumers can iterate without re-sorting. Returns nil on a nil
// receiver and an empty slice on an empty store.
func (s *ObservableStore) List() []Card {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	out := make([]Card, 0, len(s.cards))
	for _, c := range s.cards {
		out = append(out, c)
	}
	s.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Ts.After(out[j].Ts)
	})
	return out
}

// loadFromFile populates the in-memory map from the file at s.path.
// Missing file → empty store, no error. Malformed file → empty store,
// no error (the next Put rewrites it). We deliberately swallow read
// errors: cards are a UI convenience, not load-bearing for the session.
func (s *ObservableStore) loadFromFile() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var cards []Card
	if err := json.Unmarshal(data, &cards); err != nil {
		return
	}
	m := make(map[cardKey]Card, len(cards))
	for _, c := range cards {
		if c.Source == "" || c.Key == "" {
			continue
		}
		m[cardKey{c.Source, c.Key}] = c
		if c.Ts.After(s.lastTs) {
			s.lastTs = c.Ts
		}
	}
	s.cards = m
}

// flush writes the current map to s.path atomically (temp + rename in
// the same directory). Read errors and write errors are silently
// dropped — see loadFromFile for rationale.
//
// flushMu serialises the take-snapshot/marshal/rename sequence so
// concurrent flushes can't reorder their renames and lose the latest
// snapshot. The main mu is released between snapshot and write so other
// Put/Clear/List calls still proceed; subsequent flushes will pick up
// any updates made during this one's IO.
//
// On-disk order is unspecified: consumers (observe.py, tests) sort on
// read if they need a particular shape. We don't pay for a sort in the
// hot path.
func (s *ObservableStore) flush() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.RLock()
	path := s.path
	cards := make([]Card, 0, len(s.cards))
	for _, c := range s.cards {
		cards = append(cards, c)
	}
	s.mu.RUnlock()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(cards, "", "  ")
	if err != nil {
		return
	}
	// Atomic temp+rename in the same directory so rename is a single
	// syscall on the same filesystem. os.CreateTemp gives the temp
	// file a unique random suffix so concurrent flushes (and sibling
	// processes in a fork scenario) don't clobber each other.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
}

// truncateRunes returns s truncated to at most maxRunes runes. Used by
// Put to enforce SlugMaxLen without splitting a multibyte rune.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}
