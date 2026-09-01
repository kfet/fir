package providers

import (
	"fmt"
	"sync"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// sideQueryModel is a plain Anthropic model with long-retention support.
func sideQueryModel(id string) *ai.Model {
	return &ai.Model{ID: id, Provider: ai.ProviderAnthropic, MaxTokens: 8192}
}

// sideQueryCtx builds a context whose messages are `turns` alternating
// user/assistant history messages followed by the unique question.
func sideQueryCtx(turns int, question string) ai.Context {
	msgs := make([]ai.Message, 0, turns+1)
	for i := 0; i < turns; i++ {
		msgs = append(msgs, ai.NewUserMsg(fmt.Sprintf("history %d", i), int64(i)))
		msgs = append(msgs, ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{ai.NewTextContent(fmt.Sprintf("reply %d", i))},
		}))
	}
	msgs = append(msgs, ai.NewUserMsg(question, 9999))
	return ai.Context{SystemPrompt: "Be helpful.", Messages: msgs}
}

func sideQueryOptions(sessionID, modelID string) *ai.StreamOptions {
	return &ai.StreamOptions{
		SessionID:      SideQuerySessionID(sessionID, modelID),
		CacheRetention: ai.CacheLong,
		Metadata:       map[string]any{MetadataSideQuery: true},
	}
}

// cacheBreakpoints returns the indices of converted messages carrying a
// cache_control marker, plus the ttl seen on each.
func cacheBreakpoints(t *testing.T, params map[string]any) (idx []int, ttls []any) {
	t.Helper()
	msgs, ok := params["messages"].([]map[string]any)
	if !ok {
		t.Fatalf("messages not []map[string]any, got %T", params["messages"])
	}
	for i, m := range msgs {
		content, ok := m["content"].([]map[string]any)
		if !ok {
			continue
		}
		for _, b := range content {
			cc, ok := b["cache_control"].(map[string]any)
			if !ok {
				continue
			}
			idx = append(idx, i)
			ttls = append(ttls, cc["ttl"])
		}
	}
	return idx, ttls
}

// stableIndex is the index applySideQueryCacheControl should write at for the
// sideQueryCtx fixture: the last "user" entry before the question, i.e. one
// back from the trailing assistant turn.
func stableIndex(t *testing.T, params map[string]any) int {
	t.Helper()
	msgs, _ := params["messages"].([]map[string]any)
	for i := len(msgs) - 2; i >= 0; i-- {
		if role, _ := msgs[i]["role"].(string); role == "user" {
			return i
		}
	}
	return -1
}

func messageCount(t *testing.T, params map[string]any) int {
	t.Helper()
	msgs, _ := params["messages"].([]map[string]any)
	return len(msgs)
}

func TestSideQuerySessionID(t *testing.T) {
	if got := SideQuerySessionID("sess", "claude-x"); got != "sess:sidequery:claude-x" {
		t.Errorf("unexpected namespace: %q", got)
	}
	// No executor session id → no namespace (anchoring and the guard both
	// key off a non-empty id).
	if got := SideQuerySessionID("", "claude-x"); got != "" {
		t.Errorf("empty session id must stay empty, got %q", got)
	}
	// Different models must never share a namespace — a chain fallback to
	// another advisor model is a different cache.
	if SideQuerySessionID("sess", "a") == SideQuerySessionID("sess", "b") {
		t.Error("distinct models must produce distinct namespaces")
	}
}

func TestApplySideQueryOptions(t *testing.T) {
	orig := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			SessionID:      "sess",
			CacheRetention: ai.CacheShort,
			Metadata:       map[string]any{"other": 1},
		},
		Reasoning: ai.ThinkingHigh,
	}
	out := ApplySideQueryOptions(sideQueryModel("claude-x"), orig)

	if out == orig {
		t.Fatal("must return a copy, not the same pointer")
	}
	if out.SessionID != "sess:sidequery:claude-x" {
		t.Errorf("SessionID = %q", out.SessionID)
	}
	if out.CacheRetention != ai.CacheLong {
		t.Errorf("CacheRetention = %q, want long", out.CacheRetention)
	}
	if !isSideQuery(&out.StreamOptions) {
		t.Error("marker metadata not set")
	}
	if out.Reasoning != ai.ThinkingHigh {
		t.Error("unrelated fields must be preserved")
	}
	if out.Metadata["other"] != 1 {
		t.Error("pre-existing metadata must be preserved")
	}

	// The agent module reuses its options struct across internal retries —
	// mutating the caller's copy would leak the side-query namespace into
	// the executor's own requests.
	if orig.SessionID != "sess" || orig.CacheRetention != ai.CacheShort {
		t.Error("caller's options were mutated")
	}
	if _, ok := orig.Metadata[MetadataSideQuery]; ok {
		t.Error("caller's metadata map was mutated")
	}

	// nil options must still yield a usable, marked struct.
	nilOut := ApplySideQueryOptions(sideQueryModel("claude-x"), nil)
	if !isSideQuery(&nilOut.StreamOptions) || nilOut.CacheRetention != ai.CacheLong {
		t.Error("nil options not specialised")
	}
	if nilOut.SessionID != "" {
		t.Errorf("no executor session id must stay empty, got %q", nilOut.SessionID)
	}
}

func TestIsSideQuery(t *testing.T) {
	if isSideQuery(nil) {
		t.Error("nil options must not be a side query")
	}
	if isSideQuery(&ai.StreamOptions{}) {
		t.Error("no metadata must not be a side query")
	}
	if isSideQuery(&ai.StreamOptions{Metadata: map[string]any{MetadataSideQuery: false}}) {
		t.Error("false marker must not be a side query")
	}
	if isSideQuery(&ai.StreamOptions{Metadata: map[string]any{MetadataSideQuery: "yes"}}) {
		t.Error("non-bool marker must not be a side query")
	}
}

// The executor path must be entirely unaffected: one breakpoint, on the tail.
func TestSideQuery_ExecutorPathUnchanged(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	params := buildAnthropicParams(model, sideQueryCtx(3, "question"), false,
		&ai.StreamOptions{SessionID: "sess", CacheRetention: ai.CacheShort})

	idx, _ := cacheBreakpoints(t, params)
	tail := messageCount(t, params) - 1
	if len(idx) != 1 || idx[0] != tail {
		t.Errorf("executor path breakpoints = %v, want [%d]", idx, tail)
	}
}

// First escalation: no anchor yet. The question gets NO breakpoint; the last
// history message gets one and becomes the anchor.
func TestSideQuery_FirstCallAnchorsLastStableHistoryEntry(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	params := buildAnthropicParams(model, sideQueryCtx(3, "question"), false,
		sideQueryOptions("sess", "claude-x"))

	write := stableIndex(t, params)
	idx, ttls := cacheBreakpoints(t, params)
	if len(idx) != 1 || idx[0] != write {
		t.Fatalf("breakpoints = %v, want [%d] (last stable history entry)", idx, write)
	}
	// The trailing assistant turn — the one StripUnmatchedToolCalls mangles,
	// and the one message guaranteed to be rewritten before the next side
	// query — must NOT carry the breakpoint.
	if write != messageCount(t, params)-3 {
		t.Errorf("write index %d should skip the trailing assistant turn", write)
	}
	if ttls[0] != "1h" {
		t.Errorf("side query should request 1h retention, got ttl=%v", ttls[0])
	}

	anchor, ok := sideQueryAnchors.get(SideQuerySessionID("sess", "claude-x"))
	if !ok || anchor.index != write {
		t.Errorf("anchor = %+v (ok=%v), want index %d", anchor, ok, write)
	}

	// System block keeps its own breakpoint at the same retention — a
	// request must not mix a long TTL after a short one.
	system := params["system"].([]map[string]any)
	cc, ok := system[len(system)-1]["cache_control"].(map[string]any)
	if !ok || cc["ttl"] != "1h" {
		t.Errorf("system block cache_control = %v, want ttl 1h", system[len(system)-1]["cache_control"])
	}
}

// Second escalation a few executor turns later: the remembered anchor is read
// back and a new anchor is written at the new tail.
func TestSideQuery_SecondCallReadsAnchorAndAdvances(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	key := SideQuerySessionID("sess", "claude-x")

	first := buildAnthropicParams(model, sideQueryCtx(3, "q1"), false, sideQueryOptions("sess", "claude-x"))
	firstAnchor, _ := sideQueryAnchors.get(key)
	firstWrite := stableIndex(t, first)

	// Three more executor turns happened, then a second escalation.
	second := buildAnthropicParams(model, sideQueryCtx(6, "q2"), false, sideQueryOptions("sess", "claude-x"))
	write := stableIndex(t, second)
	idx, ttls := cacheBreakpoints(t, second)

	if len(idx) != 2 {
		t.Fatalf("expected 2 message breakpoints (anchor + tail), got %v", idx)
	}
	if idx[0] != firstAnchor.index {
		t.Errorf("first breakpoint = %d, want remembered anchor %d", idx[0], firstAnchor.index)
	}
	if idx[1] != write {
		t.Errorf("second breakpoint = %d, want new write point %d", idx[1], write)
	}
	for _, ttl := range ttls {
		if ttl != "1h" {
			t.Errorf("all side-query breakpoints should share the 1h ttl, got %v", ttl)
		}
	}
	if firstAnchor.index != firstWrite {
		t.Errorf("sanity: first anchor %d != first write point %d", firstAnchor.index, firstWrite)
	}

	// The anchor advanced to the new write point for the next call.
	newAnchor, _ := sideQueryAnchors.get(key)
	if newAnchor.index != write {
		t.Errorf("anchor did not advance: %d, want %d", newAnchor.index, write)
	}
}

// Back-to-back escalations with no executor turns in between: the anchor
// coincides with the tail, so only one breakpoint is spent.
func TestSideQuery_AnchorCoincidesWithWritePoint(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")

	buildAnthropicParams(model, sideQueryCtx(3, "q1"), false, sideQueryOptions("sess", "claude-x"))
	second := buildAnthropicParams(model, sideQueryCtx(3, "q2"), false, sideQueryOptions("sess", "claude-x"))

	idx, _ := cacheBreakpoints(t, second)
	write := stableIndex(t, second)
	if len(idx) != 1 || idx[0] != write {
		t.Errorf("breakpoints = %v, want single breakpoint [%d]", idx, write)
	}
}

// History rewritten under the anchor (compaction, edited transcript): the
// stale anchor is dropped rather than pointing at a different message.
func TestSideQuery_StaleAnchorDropped(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	key := SideQuerySessionID("sess", "claude-x")

	buildAnthropicParams(model, sideQueryCtx(3, "q1"), false, sideQueryOptions("sess", "claude-x"))

	// Same message COUNT, different content at the anchor slot.
	rewritten := sideQueryCtx(6, "q2")
	anchor, _ := sideQueryAnchors.get(key)
	rewritten.Messages[anchor.index] = ai.NewUserMsg("something else entirely", 42)

	second := buildAnthropicParams(model, rewritten, false, sideQueryOptions("sess", "claude-x"))
	idx, _ := cacheBreakpoints(t, second)
	write := stableIndex(t, second)
	if len(idx) != 1 || idx[0] != write {
		t.Errorf("stale anchor should be dropped; breakpoints = %v, want [%d]", idx, write)
	}
}

// History shrank below the anchor index: drop it, don't index out of range.
func TestSideQuery_AnchorBeyondHistoryDropped(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")

	buildAnthropicParams(model, sideQueryCtx(8, "q1"), false, sideQueryOptions("sess", "claude-x"))
	second := buildAnthropicParams(model, sideQueryCtx(2, "q2"), false, sideQueryOptions("sess", "claude-x"))

	idx, _ := cacheBreakpoints(t, second)
	write := stableIndex(t, second)
	if len(idx) != 1 || idx[0] != write {
		t.Errorf("breakpoints = %v, want [%d]", idx, write)
	}
}

// A chain fallback to a different advisor model is a different cache
// namespace and must not reuse the first model's anchor.
func TestSideQuery_DifferentModelIsolatesAnchor(t *testing.T) {
	sideQueryAnchors.reset()

	buildAnthropicParams(sideQueryModel("claude-a"), sideQueryCtx(3, "q1"), false,
		sideQueryOptions("sess", "claude-a"))

	other := buildAnthropicParams(sideQueryModel("claude-b"), sideQueryCtx(6, "q2"), false,
		sideQueryOptions("sess", "claude-b"))

	idx, _ := cacheBreakpoints(t, other)
	write := stableIndex(t, other)
	if len(idx) != 1 || idx[0] != write {
		t.Errorf("model b must start without an anchor; breakpoints = %v, want [%d]", idx, write)
	}
	if _, ok := sideQueryAnchors.get(SideQuerySessionID("sess", "claude-a")); !ok {
		t.Error("model a's anchor must survive model b's call")
	}
}

// No executor session id → no namespace → no anchoring, but the tail
// breakpoint is still placed and nothing panics.
func TestSideQuery_NoSessionIDStillPlacesWriteBreakpoint(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	opts := &ai.StreamOptions{
		CacheRetention: ai.CacheLong,
		Metadata:       map[string]any{MetadataSideQuery: true},
	}

	params := buildAnthropicParams(model, sideQueryCtx(3, "q"), false, opts)
	idx, _ := cacheBreakpoints(t, params)
	write := stableIndex(t, params)
	if len(idx) != 1 || idx[0] != write {
		t.Errorf("breakpoints = %v, want [%d]", idx, write)
	}
	if len(sideQueryAnchors.m) != 0 {
		t.Error("no anchor should be stored without a session id")
	}
}

// A side query with no history at all (just the question) has nothing worth
// caching in the messages.
func TestSideQuery_QuestionOnlyHasNoMessageBreakpoint(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	params := buildAnthropicParams(model, sideQueryCtx(0, "just the question"), false,
		sideQueryOptions("sess", "claude-x"))

	if idx, _ := cacheBreakpoints(t, params); len(idx) != 0 {
		t.Errorf("question-only side query should carry no message breakpoint, got %v", idx)
	}
}

// A model without long-retention support degrades to the default 5m TTL
// instead of sending an unsupported ttl.
func TestSideQuery_RetentionDegradesForUnsupportedModel(t *testing.T) {
	sideQueryAnchors.reset()
	model := &ai.Model{
		ID:        "proxy-claude",
		Provider:  ai.ProviderAnthropic,
		BaseURL:   "https://custom.proxy.example",
		MaxTokens: 8192,
		Compat:    &ai.AnthropicMessagesCompat{SupportsLongCacheRetention: ai.BoolPtr(false)},
	}
	params := buildAnthropicParams(model, sideQueryCtx(3, "q"), false,
		sideQueryOptions("sess", "proxy-claude"))

	_, ttls := cacheBreakpoints(t, params)
	if len(ttls) != 1 {
		t.Fatalf("expected 1 breakpoint, got %d", len(ttls))
	}
	if ttls[0] != nil {
		t.Errorf("unsupported model must not carry a ttl, got %v", ttls[0])
	}
	system := params["system"].([]map[string]any)
	cc := system[len(system)-1]["cache_control"].(map[string]any)
	if cc["ttl"] != nil {
		t.Errorf("system block must degrade too, got ttl=%v", cc["ttl"])
	}
}

// The anchor store is bounded: a process cycling through many sessions must
// not grow it without limit.
func TestSideQueryAnchorStoreIsBounded(t *testing.T) {
	sideQueryAnchors.reset()
	for i := 0; i < maxSideQueryAnchors+10; i++ {
		sideQueryAnchors.put(fmt.Sprintf("key-%d", i), sideQueryAnchor{index: i, hash: "h"})
	}
	if got := len(sideQueryAnchors.m); got != maxSideQueryAnchors {
		t.Errorf("store size = %d, want %d", got, maxSideQueryAnchors)
	}
	if _, ok := sideQueryAnchors.get("key-0"); ok {
		t.Error("oldest entry should have been evicted")
	}
	if _, ok := sideQueryAnchors.get(fmt.Sprintf("key-%d", maxSideQueryAnchors+9)); !ok {
		t.Error("newest entry should be present")
	}

	// Re-putting an existing key updates in place without consuming a slot.
	before := len(sideQueryAnchors.order)
	sideQueryAnchors.put("key-70", sideQueryAnchor{index: 99, hash: "h2"})
	if len(sideQueryAnchors.order) != before {
		t.Errorf("re-put changed order length: %d -> %d", before, len(sideQueryAnchors.order))
	}
	if a, _ := sideQueryAnchors.get("key-70"); a.index != 99 {
		t.Errorf("re-put did not update: %+v", a)
	}
}

// toolResult builds a tool_result message for the regression fixture below.
func toolResult(id, text string) ai.Message {
	return ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: id,
		ToolName:   "read",
		Content:    []ai.ToolResultContent{{Type: "text", Text: text}},
	})
}

func assistantWithCall(text, id, name string) ai.Message {
	return ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewTextContent(text),
			ai.NewToolCallContent(id, name, map[string]any{"q": "?"}),
		},
	})
}

func assistantText(text string) ai.Message {
	return ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent(text)},
	})
}

// Regression for the failure caught by live measurement against Anthropic:
// two escalations a few executor turns apart both paid a full cache write.
//
// SideQueryStream runs agent.StripUnmatchedToolCalls before appending the
// question, so the trailing assistant turn of the snapshot is the executor
// turn still in flight — its `aside` tool_use is removed for this request and
// restored (with a tool_result after it) by the time the next escalation runs.
// Anchoring on "the last history message" therefore anchored on the one
// message guaranteed to be rewritten, and the hash check dropped it every
// time. The breakpoint must land on the last "user" entry instead.
func TestSideQuery_AnchorSurvivesStrippedTrailingAssistantTurn(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	key := SideQuerySessionID("sess", "claude-x")

	// Escalation 1 — the aside tool_use has been stripped off message 3.
	first := ai.Context{SystemPrompt: "sys", Messages: []ai.Message{
		ai.NewUserMsg("do the task", 1),
		assistantWithCall("reading the file", "call-read", "read"),
		toolResult("call-read", "a large file body"),
		assistantText("consulting the advisor"),
		ai.NewUserMsg("advisor question 1", 2),
	}}
	firstParams := buildAnthropicParams(model, first, false, sideQueryOptions("sess", "claude-x"))

	idx, _ := cacheBreakpoints(t, firstParams)
	if len(idx) != 1 || idx[0] != 2 {
		t.Fatalf("first escalation breakpoints = %v, want [2] (the tool_result block)", idx)
	}
	if a, _ := sideQueryAnchors.get(key); a.index != 2 {
		t.Fatalf("anchor = %d, want 2", a.index)
	}

	// Escalation 2 — message 3 is back to its real form, its tool_result now
	// follows, two more executor turns happened, and a fresh trailing
	// assistant turn has been stripped.
	second := ai.Context{SystemPrompt: "sys", Messages: []ai.Message{
		ai.NewUserMsg("do the task", 1),
		assistantWithCall("reading the file", "call-read", "read"),
		toolResult("call-read", "a large file body"),
		assistantWithCall("consulting the advisor", "call-aside", "aside"),
		toolResult("call-aside", "advisor said things"),
		assistantWithCall("running ls", "call-bash", "bash"),
		toolResult("call-bash", "file listing"),
		assistantText("consulting the advisor again"),
		ai.NewUserMsg("advisor question 2", 3),
	}}
	secondParams := buildAnthropicParams(model, second, false, sideQueryOptions("sess", "claude-x"))

	idx, _ = cacheBreakpoints(t, secondParams)
	if len(idx) != 2 {
		t.Fatalf("second escalation breakpoints = %v, want 2 (anchor read + delta write)", idx)
	}
	if idx[0] != 2 {
		t.Errorf("anchor breakpoint = %d, want 2 — the previous call's write point", idx[0])
	}
	if idx[1] != 6 {
		t.Errorf("write breakpoint = %d, want 6 (last tool_result, skipping the stripped turn)", idx[1])
	}
	if a, _ := sideQueryAnchors.get(key); a.index != 6 {
		t.Errorf("anchor did not advance to 6, got %d", a.index)
	}
}

// An all-assistant history (no user entry before the question) must not
// index out of range.
func TestSideQuery_NoStableEntry(t *testing.T) {
	sideQueryAnchors.reset()
	ctx := ai.Context{SystemPrompt: "sys", Messages: []ai.Message{
		assistantText("assistant only"),
		ai.NewUserMsg("question", 1),
	}}
	params := buildAnthropicParams(sideQueryModel("claude-x"), ctx, false,
		sideQueryOptions("sess", "claude-x"))
	if idx, _ := cacheBreakpoints(t, params); len(idx) != 0 {
		t.Errorf("no stable history entry should mean no breakpoint, got %v", idx)
	}
	if len(sideQueryAnchors.m) != 0 {
		t.Error("no anchor should be stored when nothing was written")
	}
}

// The one-off question must not enter the guard's slot history. It occupies
// the tail slot and is replaced wholesale next time, so hashing it reports a
// "message changed" on every single side query — noise that says nothing
// about prefix stability.
func TestSideQuery_GuardIgnoresTheQuestion(t *testing.T) {
	sideQueryAnchors.reset()
	anthropicPrefixGuards.Delete(SideQuerySessionID("guard-sess", "claude-x"))
	model := sideQueryModel("claude-x")
	opts := func() *ai.StreamOptions { return sideQueryOptions("guard-sess", "claude-x") }

	buildAnthropicParams(model, sideQueryCtx(3, "first question"), false, opts())

	v, ok := anthropicPrefixGuards.Load(SideQuerySessionID("guard-sess", "claude-x"))
	if !ok {
		t.Fatal("no guard was created for the side-query namespace")
	}
	guard := v.(*PrefixGuard)

	// The same system blocks the real call fed it (cache_control is stripped
	// before hashing, so the bare text block is equivalent).
	system := []map[string]any{{"type": "text", "text": "Be helpful."}}

	// Identical history, brand new question. Nothing in the prefix moved.
	msgs := convertAnthropicMessages(sideQueryCtx(3, "second question").Messages, model, false, ai.CacheNone)
	if n := guard.Check(system, msgs[:len(msgs)-1]); n != 0 {
		t.Errorf("guard reported %d invalidations for an unchanged history, want 0", n)
	}

	// Sanity: including the question would have reported one.
	fresh := NewPrefixGuard()
	first := convertAnthropicMessages(sideQueryCtx(3, "first question").Messages, model, false, ai.CacheNone)
	fresh.Check(system, first)
	if n := fresh.Check(system, msgs); n != 1 {
		t.Errorf("including the question should report 1 invalidation, got %d", n)
	}
}

// A write point that cannot carry a breakpoint was never written, so it must
// not be remembered as an anchor — that would burn a breakpoint on a
// guaranteed miss next time.
func TestSideQuery_UnmarkableWritePointIsNotRemembered(t *testing.T) {
	sideQueryAnchors.reset()
	model := sideQueryModel("claude-x")
	key := SideQuerySessionID("sess", "claude-x")

	params := []map[string]any{
		// A user entry whose content is not in block form — setCacheControl
		// cannot attach anything to it.
		{"role": "user", "content": "plain string content"},
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "question"}}},
	}
	applySideQueryCacheControl(params, model, ai.CacheLong, key)

	if _, ok := params[0]["content"].([]map[string]any); ok {
		t.Fatal("fixture is wrong: content should not be block form")
	}
	if _, ok := sideQueryAnchors.get(key); ok {
		t.Error("an unmarkable write point must not be stored as an anchor")
	}
}

// The store is shared process-wide and reached from concurrent provider
// requests. Interleaving must not corrupt it (the race detector is the real
// assertion here).
func TestSideQueryAnchorStoreConcurrent(t *testing.T) {
	sideQueryAnchors.reset()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i%4)
			sideQueryAnchors.put(key, sideQueryAnchor{index: i, hash: "h"})
			sideQueryAnchors.get(key)
		}(i)
	}
	wg.Wait()
	if got := len(sideQueryAnchors.m); got != 4 {
		t.Errorf("store size = %d, want 4", got)
	}
	if got := len(sideQueryAnchors.order); got != 4 {
		t.Errorf("order length = %d, want 4 — a re-put must not consume a slot", got)
	}
}
