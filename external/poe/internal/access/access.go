// Package access manages the per-worktree allowlist and pairing state for
// the poe channel bridge. State is persisted to a JSON file in the bridge's
// state directory so it survives restarts.
//
// Terminology:
//   - allowFrom: list of Poe user_ids that are permitted to interact with
//     this bridge instance. Initially empty; populated via the pairing flow.
//   - pending: map of 6-char pairing codes → {user_id, expires_at}. A code
//     is generated when an unknown user sends their first message.
//   - The terminal-side `poe-access pair <code>` skill consumes a pending
//     entry and promotes the user_id to allowFrom.
package access

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// codeTTL is how long a pairing code remains valid.
const codeTTL = 10 * time.Minute

// ErrCodeNotFound is returned by Pair when the code doesn't exist or has
// expired.
var ErrCodeNotFound = errors.New("access: pairing code not found or expired")

// ErrAlreadyPaired is returned by Pair when the user_id behind the code is
// already in allowFrom.
var ErrAlreadyPaired = errors.New("access: user already paired")

// PendingEntry tracks one outstanding pairing code.
type PendingEntry struct {
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// stateFile is the JSON shape persisted to disk.
type stateFile struct {
	AllowFrom []string                `json:"allowFrom"`
	Pending   map[string]PendingEntry `json:"pending"`
}

// Store is the in-memory + on-disk access state. All methods are safe for
// concurrent use.
type Store struct {
	mu   sync.Mutex
	path string // path to access.json
	data stateFile
}

// NewStore loads (or initialises) the access state from the given directory.
// The file `access.json` is created inside dir if it doesn't exist.
func NewStore(dir string) (*Store, error) {
	p := filepath.Join(dir, "access.json")
	s := &Store{
		path: p,
		data: stateFile{
			AllowFrom: []string{},
			Pending:   map[string]PendingEntry{},
		},
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.flush()
	}
	if err != nil {
		return nil, fmt.Errorf("access: read %s: %w", p, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("access: parse %s: %w", p, err)
	}
	if s.data.AllowFrom == nil {
		s.data.AllowFrom = []string{}
	}
	if s.data.Pending == nil {
		s.data.Pending = map[string]PendingEntry{}
	}
	return s, nil
}

// IsAllowed returns true if userID is in the allowlist.
func (s *Store) IsAllowed(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.data.AllowFrom {
		if id == userID {
			return true
		}
	}
	return false
}

// GenerateCode creates a fresh 6-character hex pairing code for the given
// userID, stores it with a TTL, persists to disk, and returns the code.
// If a pending code already exists for this userID (and hasn't expired),
// the existing code is returned instead of generating a new one.
func (s *Store) GenerateCode(userID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reuse existing unexpired code for the same user.
	now := time.Now()
	for code, entry := range s.data.Pending {
		if entry.UserID == userID && entry.ExpiresAt.After(now) {
			return code, nil
		}
	}

	// Purge expired codes while we're here.
	for code, entry := range s.data.Pending {
		if entry.ExpiresAt.Before(now) {
			delete(s.data.Pending, code)
		}

	}

	b := make([]byte, 3) // 3 bytes = 6 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("access: random: %w", err)
	}
	code := hex.EncodeToString(b)
	s.data.Pending[code] = PendingEntry{
		UserID:    userID,
		ExpiresAt: now.Add(codeTTL),
	}
	return code, s.flush()
}

// Pair consumes a pairing code and promotes the associated user_id to the
// allowlist. Returns ErrCodeNotFound if the code is missing or expired,
// ErrAlreadyPaired if the user is already allowed.
func (s *Store) Pair(code string) (userID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.data.Pending[code]
	if !ok || entry.ExpiresAt.Before(time.Now()) {
		if ok {
			delete(s.data.Pending, code)
		}
		return "", ErrCodeNotFound
	}
	delete(s.data.Pending, code)

	// Check if already paired.
	for _, id := range s.data.AllowFrom {
		if id == entry.UserID {
			return entry.UserID, ErrAlreadyPaired
		}
	}
	s.data.AllowFrom = append(s.data.AllowFrom, entry.UserID)
	return entry.UserID, s.flush()
}

// AllowFrom returns a copy of the current allowlist.
func (s *Store) AllowFrom() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.data.AllowFrom))
	copy(out, s.data.AllowFrom)
	return out
}

// PendingCount returns the number of pending (potentially expired) codes.
func (s *Store) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Pending)
}

// flush writes the current state to disk. Caller must hold s.mu.
func (s *Store) flush() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}
