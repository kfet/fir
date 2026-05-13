// Package providers — cacheguard provides a prefix-stability guard for LLM
// prompt caching. It detects when previously-sent messages are re-serialized
// differently between turns, which would break the provider's prompt cache.
//
// # How prompt caching works
//
// Providers like Anthropic cache the request prefix (system prompt + messages).
// On the next request, if the prefix is byte-identical, the cached portion is
// reused — saving both latency and cost. Any change to an earlier message
// invalidates everything after it.
//
// # What breaks the cache
//
//   - Changing the system prompt (e.g. date rollover, /reload with changed files)
//   - Mutating message content (e.g. non-deterministic timestamps in synthetic tool results)
//   - Reordering messages or tool definitions
//   - Stripping/modifying thinking signatures or redacted thinking blocks
//   - Converting thinking blocks to text on model switch (expected, unavoidable)
package providers

import (
	"crypto/sha256"
	"encoding/json"
	"sync"

	firlog "github.com/kfet/fir/pkg/log"
)

// PrefixGuard tracks the serialized prefix of previous requests and emits
// per-slot invalidation events at Trace level (run with -vv to surface them).
// Safe for concurrent use.
type PrefixGuard struct {
	mu             sync.Mutex
	prevHashes     []string // hash per message slot
	prevSystemHash string
}

// NewPrefixGuard creates a new prefix guard.
func NewPrefixGuard() *PrefixGuard {
	return &PrefixGuard{}
}

// Check compares the current request's system blocks and messages against the
// previous call. It logs a Trace event for each prefix slot that changed.
// Returns the number of prefix slots that were invalidated.
func (pg *PrefixGuard) Check(systemBlocks []map[string]any, messages []map[string]any) int {
	pg.mu.Lock()
	defer pg.mu.Unlock()

	invalidated := 0

	// Check system prompt
	sysHash := hashJSON(systemBlocks)
	if pg.prevSystemHash != "" && sysHash != pg.prevSystemHash {
		firlog.Trace("cache prefix invalidated: system prompt changed")
		invalidated++
	}
	pg.prevSystemHash = sysHash

	// Check message prefix (all messages that existed in the previous call)
	prefixLen := len(pg.prevHashes)
	if prefixLen > len(messages) {
		prefixLen = len(messages)
	}

	newHashes := make([]string, len(messages))
	for i := range messages {
		newHashes[i] = hashJSON(messages[i])
	}

	for i := 0; i < prefixLen; i++ {
		if newHashes[i] != pg.prevHashes[i] {
			firlog.Trace("cache prefix invalidated: message changed", "index", i)
			invalidated++
		}
	}

	pg.prevHashes = newHashes
	return invalidated
}

// Reset clears the stored prefix (e.g. on model switch or new session).
func (pg *PrefixGuard) Reset() {
	pg.mu.Lock()
	defer pg.mu.Unlock()
	pg.prevHashes = nil
	pg.prevSystemHash = ""
}

func hashJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return string(h[:])
}
