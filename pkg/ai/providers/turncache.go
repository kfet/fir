// Package providers — Anthropic turn-anchored 1h prompt caching.
//
// An interactive user takes longer than five minutes to reply more often
// than not. With only the default 5m breakpoints the whole conversation
// prefix has expired by then, and the next turn pays a full cache write of
// the history. Replaying 1,159 relay sessions showed that writing the
// turn's user message at a 1h TTL cuts input cost by ~9%.
//
// The TTL is fixed at write time: a later 1h breakpoint on a prefix that is
// already cached at 5m reads it but does not extend it (verified live). So
// the 1h marker must be on the first request of the turn, and it stays on
// the same message for every later request of that turn so the prefix and
// the breakpoint layout are identical.
//
// # Layout (when enabled)
//
//   - system / sections breakpoints — upgraded to 1h. The API requires every
//     1h breakpoint to precede every 5m one, and these precede the anchor.
//   - previous turn's anchor — 1h. After a long tool loop the old anchor is
//     usually more than 20 positions behind the new turn's breakpoints, past
//     the API's lookback window, so the 1h entry it wrote would never be
//     found. An explicit breakpoint on it reads it back exactly.
//   - this turn's anchor (the last user message that is not a block of tool
//     results) — 1h.
//   - the tail — the normal 5m breakpoint, unless it is the anchor itself.
//
// The four-breakpoint budget is enforced afterwards by trimSystemBreakpoints,
// which sheds system breakpoints first: by the second turn every message
// breakpoint already covers the system prompt as a prefix.
//
// The placement is stateless — it is derived from the message array alone —
// so retries and process restarts produce the same layout.
package providers

import (
	"os"
	"sync/atomic"

	"github.com/kfet/fir/pkg/ai"
)

// EnvTurnCache1h overrides the turn 1h cache: "0"/"false"/"off" disables it,
// "1"/"true"/"on" enables it, regardless of mode or settings.
const EnvTurnCache1h = "FIR_TURN_CACHE_1H"

var turnLongCache atomic.Bool

// SetTurnLongCache enables or disables turn-anchored 1h caching for this
// process. Interactive modes (TUI, ACP) enable it at startup; one-shot print
// runs leave it off — they never come back for a second turn.
func SetTurnLongCache(enabled bool) { turnLongCache.Store(enabled) }

// TurnLongCacheEnabled reports whether turn-anchored 1h caching is on,
// honouring the FIR_TURN_CACHE_1H override.
func TurnLongCacheEnabled() bool {
	switch os.Getenv(EnvTurnCache1h) {
	case "0", "false", "off":
		return false
	case "1", "true", "on":
		return true
	}
	return turnLongCache.Load()
}

// applyTurnLongCache places the turn-anchored 1h breakpoints described in the
// file comment. It is a no-op unless retention is short (long retention is
// already 1h everywhere) and the model supports a 1h TTL.
func applyTurnLongCache(system, msgs []map[string]any, model *ai.Model, retention ai.CacheRetention) {
	if retention != ai.CacheShort || !getAnthropicCompat(model).SupportsLongCacheRetention {
		return
	}
	anchor := lastUserTurnIndex(msgs, len(msgs))
	if anchor < 0 {
		return
	}
	long := cacheControlBlock(model, ai.CacheLong)

	setCacheControl(msgs[anchor], model, ai.CacheLong)
	if prev := lastUserTurnIndex(msgs, anchor); prev >= 0 {
		setCacheControl(msgs[prev], model, ai.CacheLong)
	}
	// Every breakpoint before the anchor must be 1h too (API rule: longer
	// TTLs first). Copies, so blocks never share one map.
	for _, b := range system {
		if _, ok := b["cache_control"]; ok {
			b["cache_control"] = copyCC(long)
		}
	}
	for i := 0; i < anchor; i++ {
		content, _ := msgs[i]["content"].([]map[string]any)
		for _, c := range content {
			if _, ok := c["cache_control"]; ok {
				c["cache_control"] = copyCC(long)
			}
		}
	}
}

func copyCC(cc map[string]any) map[string]any {
	out := make(map[string]any, len(cc))
	for k, v := range cc {
		out[k] = v
	}
	return out
}

// lastUserTurnIndex returns the index of the last user message before end
// that carries real user input (not a block of tool results), or -1.
func lastUserTurnIndex(msgs []map[string]any, end int) int {
	for i := end - 1; i >= 0; i-- {
		if role, _ := msgs[i]["role"].(string); role != "user" {
			continue
		}
		content, _ := msgs[i]["content"].([]map[string]any)
		if len(content) == 0 || isToolResultBlocks(content) {
			continue
		}
		return i
	}
	return -1
}

func isToolResultBlocks(content []map[string]any) bool {
	for _, c := range content {
		if t, _ := c["type"].(string); t == "tool_result" {
			return true
		}
	}
	return false
}
