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
	sysHash := hashJSON(stripCacheControlBlocks(systemBlocks))
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
		newHashes[i] = hashJSON(stripCacheControl(messages[i]))
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

// stripCacheControl returns a view of a converted message with any
// cache_control markers removed from its content blocks.
//
// Breakpoints move between calls by design — the tail breakpoint advances
// every turn, and the side-query path retires an old anchor as it places a
// new one. cache_control is request metadata, not cached content: Anthropic
// matches the content prefix, so a marker appearing or disappearing on an
// earlier message does not invalidate anything. Hashing it would make the
// guard report a prefix invalidation on every single turn — pure noise
// drowning the real drift the guard exists to find.
//
// Copies are shallow and made only for the blocks that actually carry a
// marker, so the common case allocates nothing.
func stripCacheControl(msg map[string]any) any {
	content, ok := msg["content"].([]map[string]any)
	if !ok || !hasCacheControl(content) {
		return msg
	}
	out := make(map[string]any, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	out["content"] = stripCacheControlBlocks(content)
	return out
}

// hasCacheControl reports whether any block carries a cache_control marker.
func hasCacheControl(blocks []map[string]any) bool {
	for _, b := range blocks {
		if _, ok := b["cache_control"]; ok {
			return true
		}
	}
	return false
}

// stripCacheControlBlocks returns blocks with cache_control removed. When no
// block carries one, the input slice is returned unchanged.
func stripCacheControlBlocks(blocks []map[string]any) []map[string]any {
	if !hasCacheControl(blocks) {
		return blocks
	}
	out := make([]map[string]any, len(blocks))
	for i, b := range blocks {
		if _, ok := b["cache_control"]; !ok {
			out[i] = b
			continue
		}
		cp := make(map[string]any, len(b))
		for k, v := range b {
			if k == "cache_control" {
				continue
			}
			cp[k] = v
		}
		out[i] = cp
	}
	return out
}
