package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers"
)

// --- helpers ---

func asstWithCalls(ts int64, stop ai.StopReason, ids ...string) agent.AgentMessage {
	content := []ai.AssistantContent{
		{Text: &ai.TextContent{Type: ai.ContentTypeText, Text: "working"}},
	}
	for _, id := range ids {
		content = append(content, ai.AssistantContent{ToolCall: &ai.ToolCall{
			Type:      ai.ContentTypeToolCall,
			ID:        id,
			Name:      "bash",
			Arguments: map[string]any{"command": "git push"},
		}})
	}
	return agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    content,
		StopReason: stop,
		Timestamp:  ts,
	}))
}

func realResult(id string) agent.AgentMessage {
	return agent.NewAgentMessage(ai.NewToolResultMsg(ai.ToolResultMessage{
		Role:       ai.RoleToolResult,
		ToolCallID: id,
		ToolName:   "bash",
		Content:    []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
		Timestamp:  1,
	}))
}

func userMsg(text string) agent.AgentMessage {
	return agent.NewAgentMessage(ai.NewUserMsg(text, 1))
}

// roles renders the message list as a compact role sequence, marking
// synthesized results so ordering assertions read clearly.
func roles(msgs []agent.AgentMessage) []string {
	out := make([]string, 0, len(msgs))
	for i := range msgs {
		if tr := msgs[i].Message.AsToolResult(); tr != nil && IsInterruptedToolResult(tr) {
			out = append(out, "synth:"+tr.ToolCallID)
			continue
		}
		if tr := msgs[i].Message.AsToolResult(); tr != nil {
			out = append(out, "toolResult:"+tr.ToolCallID)
			continue
		}
		out = append(out, msgs[i].Role())
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("roles mismatch\n got: %v\nwant: %v", got, want)
	}
}

// --- unit tests ---

func TestSynthesizeSingleTailOrphan(t *testing.T) {
	in := []agent.AgentMessage{
		userMsg("hi"),
		asstWithCalls(7, ai.StopReasonToolUse, "call-1"),
	}
	out := SynthesizeInterruptedToolResults(in)
	eq(t, roles(out), []string{"user", "assistant", "synth:call-1"})

	tr := out[2].Message.AsToolResult()
	if !tr.IsError {
		t.Error("synthesized result must be marked as an error")
	}
	if tr.ToolName != "bash" {
		t.Errorf("tool name should be carried over, got %q", tr.ToolName)
	}
	if tr.Timestamp != 7 {
		t.Errorf("timestamp should be inherited from the assistant message, got %d", tr.Timestamp)
	}
	text := tr.Content[0].Text
	for _, want := range []string{"MAY OR MAY NOT", "UNKNOWN", "verify"} {
		if !strings.Contains(text, want) {
			t.Errorf("synthesized text missing %q: %s", want, text)
		}
	}
	// Distinguishable from a genuine failure.
	if !IsInterruptedToolResult(tr) {
		t.Error("synthesized result must be recognizable via IsInterruptedToolResult")
	}
	rr := realResult("call-1")
	if IsInterruptedToolResult(rr.Message.AsToolResult()) {
		t.Error("a real tool result must not be reported as interrupted")
	}
	// Input untouched.
	if len(in) != 2 {
		t.Error("input slice must not be mutated")
	}
}

func TestSynthesizeMultipleParallelOrphans(t *testing.T) {
	in := []agent.AgentMessage{
		userMsg("hi"),
		asstWithCalls(3, ai.StopReasonToolUse, "a", "b", "c"),
	}
	out := SynthesizeInterruptedToolResults(in)
	eq(t, roles(out), []string{"user", "assistant", "synth:a", "synth:b", "synth:c"})
}

func TestSynthesizePartiallyAnsweredParallelCalls(t *testing.T) {
	// Two parallel calls, only the first got a result before the kill.
	in := []agent.AgentMessage{
		userMsg("hi"),
		asstWithCalls(3, ai.StopReasonToolUse, "a", "b"),
		realResult("a"),
	}
	out := SynthesizeInterruptedToolResults(in)
	eq(t, roles(out), []string{"user", "assistant", "synth:b", "toolResult:a"})
}

func TestSynthesizeNoOrphanLeavesListUnchanged(t *testing.T) {
	in := []agent.AgentMessage{
		userMsg("hi"),
		asstWithCalls(3, ai.StopReasonToolUse, "a"),
		realResult("a"),
		asstWithCalls(4, ai.StopReasonStop),
	}
	out := SynthesizeInterruptedToolResults(in)
	eq(t, roles(out), []string{"user", "assistant", "toolResult:a", "assistant"})
	if len(out) != len(in) {
		t.Fatalf("expected no insertions, got %d messages from %d", len(out), len(in))
	}
}

func TestSynthesizeMidHistoryOrphan(t *testing.T) {
	// Orphan is not at the tail: the user typed again after the restart.
	in := []agent.AgentMessage{
		userMsg("hi"),
		asstWithCalls(3, ai.StopReasonToolUse, "dead"),
		userMsg("you still there?"),
		asstWithCalls(9, ai.StopReasonToolUse, "live"),
		realResult("live"),
	}
	out := SynthesizeInterruptedToolResults(in)
	eq(t, roles(out), []string{
		"user", "assistant", "synth:dead", "user", "assistant", "toolResult:live",
	})
}

func TestSynthesizeSkipsErroredAndAbortedAssistants(t *testing.T) {
	// providers.TransformMessages drops these assistant messages entirely;
	// synthesizing a result for them would leave an orphaned tool_result.
	for _, stop := range []ai.StopReason{ai.StopReasonError, ai.StopReasonAborted} {
		in := []agent.AgentMessage{
			userMsg("hi"),
			asstWithCalls(3, stop, "x"),
		}
		out := SynthesizeInterruptedToolResults(in)
		eq(t, roles(out), []string{"user", "assistant"})
	}
}

func TestSynthesizeIsIdempotent(t *testing.T) {
	in := []agent.AgentMessage{
		userMsg("hi"),
		asstWithCalls(3, ai.StopReasonToolUse, "a", "b"),
	}
	once := SynthesizeInterruptedToolResults(in)
	twice := SynthesizeInterruptedToolResults(once)
	eq(t, roles(twice), roles(once))
	if len(twice) != len(once) {
		t.Fatalf("second pass inserted more messages: %d vs %d", len(twice), len(once))
	}
}

func TestSynthesizeEmptyAndCustomMessages(t *testing.T) {
	if got := SynthesizeInterruptedToolResults(nil); got != nil {
		t.Error("nil input should stay nil")
	}
	in := []agent.AgentMessage{
		CreateCompactionSummaryMessage("summary", 10, time.Unix(1, 0)),
		asstWithCalls(3, ai.StopReasonToolUse, "a"),
	}
	out := SynthesizeInterruptedToolResults(in)
	if len(out) != 3 || !IsInterruptedToolResult(out[2].Message.AsToolResult()) {
		t.Fatalf("custom messages must pass through untouched, got %v", roles(out))
	}
}

// --- session-load integration ---

// writeSessionFile writes a raw JSONL transcript and returns its path.
func writeSessionFile(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const testHeaderLine = `{"type":"session","version":3,"id":"s1","timestamp":"2026-08-02T07:00:00Z","cwd":"/tmp"}`

func entryLine(t *testing.T, id, parent string, msg ai.Message) string {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	e := SessionEntry{
		Type:       "message",
		ID:         id,
		ParentID:   parent,
		Timestamp:  "2026-08-02T07:39:18Z",
		RawMessage: raw,
	}
	b, err := json.Marshal(&e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadSessionWithOrphanedToolCall(t *testing.T) {
	dir := t.TempDir()
	path := writeSessionFile(t, dir,
		testHeaderLine,
		entryLine(t, "e1", "", userMsg("push it").Message),
		entryLine(t, "e2", "e1", asstWithCalls(11, ai.StopReasonToolUse, "call-x", "call-y").Message),
		"", // trailing newline
	)

	ss, _ := OpenSessionStore(path, dir)
	defer ss.Close()

	ctx := ss.BuildSessionContext()
	eq(t, roles(ctx.Messages), []string{"user", "assistant", "synth:call-x", "synth:call-y"})

	// Deliberate downstream consequence, pinned here: the loaded context now
	// ends in a toolResult, which is what AgentSession.HasPendingWork() keys
	// on. That is the intended reading — the agent was interrupted mid-tool
	// and the next inference should start from "outcome unknown, verify".
	// HasPendingWork is only consulted post-compaction, so this does not make
	// `fir -c` fire a spontaneous request at startup.
	last := ctx.Messages[len(ctx.Messages)-1]
	if last.Role() != "toolResult" {
		t.Fatalf("expected loaded context to end in a toolResult, got %q", last.Role())
	}
}

func TestLoadSessionTruncatedFinalLine(t *testing.T) {
	dir := t.TempDir()
	good := entryLine(t, "e2", "e1", asstWithCalls(11, ai.StopReasonToolUse, "call-x").Message)
	partial := `{"type":"message","id":"e3","parentId":"e2","message":{"role":"assis`

	path := writeSessionFile(t, dir,
		testHeaderLine,
		entryLine(t, "e1", "", userMsg("push it").Message),
		good,
		partial, // SIGKILL mid-write: no trailing newline
	)

	ss, _ := OpenSessionStore(path, dir)
	defer ss.Close()

	// The malformed line must not fail the load...
	if got := len(ss.GetEntries()); got != 2 {
		t.Fatalf("expected 2 parsed entries, got %d", got)
	}
	// ...and the orphan repair still runs.
	ctx := ss.BuildSessionContext()
	eq(t, roles(ctx.Messages), []string{"user", "assistant", "synth:call-x"})

	// The next append must land on its own line, not glued to the partial one.
	ss.AppendAIMessage(userMsg("hello again").Message)
	ss.ForceFlush()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, partial) && len(line) > len(partial) {
			t.Fatalf("line %d glued an entry onto the truncated line: %s", i, line)
		}
	}

	// Reloading the repaired file sees every complete entry.
	ss2, _ := OpenSessionStore(path, dir)
	defer ss2.Close()
	if got := len(ss2.GetEntries()); got != 3 {
		t.Fatalf("expected 3 entries after reload, got %d", got)
	}
}

func TestRepairTrailingNewlineLeavesGoodFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := writeSessionFile(t, dir, testHeaderLine, "")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repairTrailingNewline(path)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("well-formed file was modified: %q -> %q", before, after)
	}
}

// TestNoDoubleSynthesisWithProviderTransform pins the layering: after the
// session-load repair, providers.TransformMessages (which has its own
// "No result provided" backstop for orphaned tool calls) must find nothing
// left to synthesize, so the model never sees two results for one call.
func TestNoDoubleSynthesisWithProviderTransform(t *testing.T) {
	loaded := SynthesizeInterruptedToolResults([]agent.AgentMessage{
		userMsg("push it"),
		asstWithCalls(11, ai.StopReasonToolUse, "call-x", "call-y"),
	})
	llmMsgs, err := ConvertToLLM(loaded)
	if err != nil {
		t.Fatal(err)
	}
	model := &ai.Model{Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, ID: "claude-test"}
	out := providers.TransformMessages(llmMsgs, model, nil)

	counts := map[string]int{}
	for i := range out {
		if tr := out[i].AsToolResult(); tr != nil {
			counts[tr.ToolCallID]++
			if strings.Contains(tr.Content[0].Text, "No result provided") {
				t.Errorf("provider backstop fired for %s — double synthesis", tr.ToolCallID)
			}
		}
	}
	for _, id := range []string{"call-x", "call-y"} {
		if counts[id] != 1 {
			t.Errorf("tool call %s got %d results, want exactly 1", id, counts[id])
		}
	}
}

// TestNoRepairWhenLockNotHeld pins the single-writer invariant: the repair
// writes at a captured offset, so it must never run against a file another
// process is actively appending to. When the flock cannot be acquired and no
// sessionDir is configured to fork into, the truncated file is left untouched.
func TestNoRepairWhenLockNotHeld(t *testing.T) {
	dir := t.TempDir()
	partial := `{"type":"message","id":"e2","parentId":"e1","message":{"role":"assis`
	path := writeSessionFile(t, dir,
		testHeaderLine,
		entryLine(t, "e1", "", userMsg("hi").Message),
		partial, // no trailing newline
	)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Hold the lock, as a live writer in another process would.
	held, ok := tryLockSession(path)
	if !ok {
		t.Fatal("could not acquire the session lock")
	}
	defer held.Close()

	// sessionDir empty → no fork path → store proceeds without the lock.
	ss := &SessionStore{
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	ss.setSessionFile(path)
	defer ss.Close()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file was modified without holding the lock:\n before: %q\n after:  %q", before, after)
	}
}
