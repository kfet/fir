// Package providers — side-query (advisor) prompt-cache specialisation.
//
// A "side query" is fir's ephemeral one-shot LLM call that replays the whole
// executor conversation and appends a single unique question (see
// session.AgentSession.SideQueryStream). It is the advisor/delegate path
// behind the `aside` tool.
//
// # Why it needs its own cache strategy
//
// The default Anthropic breakpoint layout puts one cache_control on the LAST
// user content block. On a normal executor turn that block recurs on the next
// turn, so the write is amortised. On a side query it never does: the tail is
// the one-off question. Every escalation therefore terminated its cache entry
// at a block that could never be read back, and — with more than the ~20-block
// automatic lookback of executor tool calls in between — each escalation paid
// a FULL cache write of the entire history.
//
// # The layout used instead
//
//   - system block breakpoint — unchanged.
//   - a breakpoint at the last STABLE history entry — the last entry before
//     the question whose role is "user" (a user turn, or a block of tool
//     results). This is the longest prefix that will still be a prefix of the
//     next side query, so it is what we want written; it is remembered as the
//     next call's anchor.
//   - a breakpoint at the REMEMBERED ANCHOR from the previous side query on
//     the same (session, model), so the request reads back exactly what the
//     previous one wrote instead of relying on lookback.
//   - NO breakpoint on the question itself. It caches nothing reusable, and a
//     within-call retry replays an identical request that already hits the
//     stable-history breakpoint.
//
// # Why not simply the last history message
//
// Because it is the one message guaranteed to change. SideQueryStream runs
// agent.StripUnmatchedToolCalls before appending the question, and the
// trailing assistant message of a side-query snapshot is always the executor
// turn still in flight — it carries the tool_use of the very `aside` call
// driving this query, which the strip removes. By the next escalation that
// message has been rewritten (tool_use restored) and its tool_result appended
// after it. Anchoring there produced a stale anchor and a full re-write on
// every single escalation; measured, not theorised. Assistant turns are
// therefore skipped, at a cost of one trailing message of delta.
//
// Anchors are keyed by the namespaced side-query session id, which already
// embeds the model id (see SideQuerySessionID) — a chain fallback to a
// different advisor model is a different cache namespace and must not reuse
// another model's anchor.
package providers

import (
	"sync"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// MetadataSideQuery is the ai.StreamOptions.Metadata key fir sets to mark a
// request as a side query. Metadata is never serialised to any provider wire
// format — it is a fir-internal channel that survives the trip through the
// external agent module untouched.
const MetadataSideQuery = "fir.sidequery"

// sideQuerySessionSuffix separates the executor session id from the
// side-query namespace. Keeping advisor traffic out of the executor's
// PrefixGuard slot history means each one's -vv invalidation trace describes
// only its own drift.
const sideQuerySessionSuffix = ":sidequery:"

// SideQuerySessionID derives the cache/guard namespace for a side query on a
// given model from the executor's session id. Returns "" when the executor
// has no session id, since an empty id disables both the guard and anchoring.
func SideQuerySessionID(sessionID, modelID string) string {
	if sessionID == "" {
		return ""
	}
	return sessionID + sideQuerySessionSuffix + modelID
}

// ApplySideQueryOptions returns a copy of opts specialised for a side query:
// a separate PrefixGuard/anchor namespace, long (1h) cache retention, and the
// metadata marker that switches breakpoint placement in the provider.
//
// The copy matters — the agent module reuses its options struct across its
// internal retry attempts, so mutation would leak across calls.
//
// Retention is requested, not asserted: models without long-retention support
// degrade to the default 5m TTL in cacheControlBlock. Asking for 1h here is
// per-call and never touches the executor's own cache writes, unlike the
// FIR_CACHE_RETENTION environment switch.
func ApplySideQueryOptions(model *ai.Model, opts *ai.SimpleStreamOptions) *ai.SimpleStreamOptions {
	out := &ai.SimpleStreamOptions{}
	if opts != nil {
		*out = *opts
	}

	modelID := ""
	if model != nil {
		modelID = model.ID
	}
	out.SessionID = SideQuerySessionID(out.SessionID, modelID)
	out.CacheRetention = ai.CacheLong

	metadata := make(map[string]any, len(out.Metadata)+1)
	for k, v := range out.Metadata {
		metadata[k] = v
	}
	metadata[MetadataSideQuery] = true
	out.Metadata = metadata

	return out
}

// isSideQuery reports whether these stream options belong to a side query.
func isSideQuery(options *ai.StreamOptions) bool {
	if options == nil {
		return false
	}
	b, _ := options.Metadata[MetadataSideQuery].(bool)
	return b
}

// --- anchor store ---------------------------------------------------------

// sideQueryAnchor remembers where the previous side query on a namespace put
// its write breakpoint. Hash pins the identity of that message (with
// cache_control stripped) so a compacted or rewritten history is detected and
// the stale anchor dropped rather than silently pointing at a different slot.
type sideQueryAnchor struct {
	index int
	hash  string
}

// maxSideQueryAnchors bounds the anchor store. One entry per
// (session, advisor model) pair; a long-lived fir process cycling through
// sessions must not accumulate them without limit. Oldest-inserted entries
// are evicted first — losing an anchor costs one extra cache write, never
// correctness.
const maxSideQueryAnchors = 64

// anchorStore is a bounded, FIFO-evicting map of anchors, shared across every
// session in the process (keys are namespaced, so sessions never collide).
//
// get-then-put is deliberately not atomic across a whole request: two
// concurrent side queries on the SAME namespace can interleave and one may
// overwrite the other's anchor. The cost is a single extra cache write on the
// next call — the hash check keeps a stale anchor from ever being used — and
// paying for that with a lock held across an entire provider request would be
// far worse.
type anchorStore struct {
	mu    sync.Mutex
	m     map[string]sideQueryAnchor
	order []string
}

var sideQueryAnchors = &anchorStore{m: make(map[string]sideQueryAnchor)}

func (s *anchorStore) get(key string) (sideQueryAnchor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[key]
	return a, ok
}

func (s *anchorStore) put(key string, a sideQueryAnchor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.m[key]; !exists {
		if len(s.order) >= maxSideQueryAnchors {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.m, oldest)
		}
		s.order = append(s.order, key)
	}
	s.m[key] = a
}

func (s *anchorStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = make(map[string]sideQueryAnchor)
	s.order = nil
}

// --- breakpoint placement -------------------------------------------------

// applySideQueryCacheControl places the anchored rolling breakpoints on an
// already-converted Anthropic message array. params must end with the
// side-query question; everything before it is replayed executor history.
//
// key is the namespaced side-query session id; an empty key disables
// anchoring (the write breakpoint is still placed, it just isn't remembered).
func applySideQueryCacheControl(params []map[string]any, model *ai.Model, retention ai.CacheRetention, key string) {
	if retention == ai.CacheNone {
		return
	}

	// The last stable history entry — see the package comment for why a
	// trailing assistant turn is skipped rather than anchored.
	write := lastStableHistoryIndex(params)
	if write < 0 {
		// Nothing but the question, or history with no stable entry — there
		// is nothing worth caching.
		return
	}
	writeHash := hashJSON(stripCacheControl(params[write]))

	// -1 means "no anchor breakpoint placed" in the diagnostic trace below.
	anchorPlaced := -1

	if key != "" {
		if anchor, ok := sideQueryAnchors.get(key); ok {
			switch {
			case anchor.index < 0 || anchor.index >= write:
				// Stale (history shrank) or coincident with the write point —
				// a coincident anchor is already covered by that breakpoint,
				// so spending a second one on it is waste.
				firlog.Trace("side-query cache anchor unusable",
					"key", key, "anchor", anchor.index, "write", write)
			case hashJSON(stripCacheControl(params[anchor.index])) != anchor.hash:
				firlog.Trace("side-query cache anchor stale: history rewritten",
					"key", key, "anchor", anchor.index)
			default:
				if setCacheControl(params[anchor.index], model, retention) {
					anchorPlaced = anchor.index
				}
			}
		}
	}

	placed := setCacheControl(params[write], model, retention)

	// Ground truth for diagnosing a cache miss: which slots this request
	// actually marked. Pairs with the PrefixGuard trace, which says which
	// slots moved. Run with -vv to see both.
	firlog.Trace("side-query cache breakpoints",
		"key", key, "anchor", anchorPlaced, "write", write, "messages", len(params))

	// Remember the anchor only when a breakpoint was really attached. An
	// entry we could not mark was never written, so pointing the next call
	// at it would burn a breakpoint on a guaranteed miss.
	if key != "" && placed {
		sideQueryAnchors.put(key, sideQueryAnchor{index: write, hash: writeHash})
	}
}

// lastStableHistoryIndex returns the index of the last converted entry before
// the question that is safe to anchor on, or -1 when there is none.
//
// "Safe" means role "user": a user turn or a block of tool results. Those are
// never touched by agent.StripUnmatchedToolCalls, whereas the trailing
// assistant turn of a side-query snapshot always is.
func lastStableHistoryIndex(params []map[string]any) int {
	for i := len(params) - 2; i >= 0; i-- {
		if role, _ := params[i]["role"].(string); role == "user" {
			return i
		}
	}
	return -1
}

// setCacheControl attaches a breakpoint to the last content block of a
// converted message, reporting whether one was placed. A message with no
// block-form content cannot carry a breakpoint.
func setCacheControl(msg map[string]any, model *ai.Model, retention ai.CacheRetention) bool {
	content, ok := msg["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		return false
	}
	content[len(content)-1]["cache_control"] = cacheControlBlock(model, retention)
	return true
}
