package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/sections"
	fmsg "github.com/kfet/fir/pkg/session/store"
)

// sectionsHarness builds a session the way `fir -p` does (CreateAgentSession)
// with a fake stream that records every LLM context it is sent.
type sectionsHarness struct {
	s     *AgentSession
	store *sections.Store
	mu    sync.Mutex
	calls [][]ai.Message
}

func newSectionsHarness(t *testing.T) *sectionsHarness {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	agentDir := t.TempDir()
	res, err := CreateAgentSession(context.Background(), CreateAgentSessionOptions{
		Cwd:      t.TempDir(),
		AgentDir: agentDir,
		Model:    &ai.Model{ID: "test-model", Provider: "anthropic", API: "anthropic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &sectionsHarness{s: res.Session, store: sections.NewStore(filepath.Join(agentDir, "sections"))}
	// Let the agent finish its run (and the transcript writes it triggers)
	// before Close, or a late write races t.TempDir's RemoveAll.
	t.Cleanup(func() {
		h.s.Agent.WaitForIdle()
		h.s.Close()
	})
	h.s.Agent.SetStreamFn(func(_ *ai.Model, llmCtx ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		h.mu.Lock()
		h.calls = append(h.calls, append([]ai.Message(nil), llmCtx.Messages...))
		h.mu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: &ai.AssistantMessage{
				Content:    []ai.AssistantContent{ai.NewTextContent("ok")},
				StopReason: ai.StopReasonStop,
			}})
			stream.End(nil)
		}()
		return stream
	})
	return h
}

func (h *sectionsHarness) prompt(t *testing.T, text string) []ai.Message {
	t.Helper()
	if err := h.s.Prompt(text); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.calls) == 0 {
		t.Fatal("no LLM call")
	}
	return h.calls[len(h.calls)-1]
}

func userText(m ai.Message) string {
	if u := m.AsUser(); u != nil {
		if s, ok := u.Content.(string); ok {
			return s
		}
	}
	return ""
}

func allowOnly(names ...string) func(string) bool {
	return func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
}

func TestSections_FirstPrintTurnSeesSections(t *testing.T) {
	h := newSectionsHarness(t)
	if err := h.store.Set("mood", "L1 lesson text"); err != nil {
		t.Fatal(err)
	}
	h.s.SetSectionFilter(allowOnly("mood"))

	msgs := h.prompt(t, "hello")
	first := userText(msgs[0])
	if !strings.HasPrefix(first, sections.BlockHeader) || !strings.Contains(first, "[section=mood]\nL1 lesson text") {
		t.Fatalf("first message is not the sections block: %q", first)
	}
	if got := userText(msgs[len(msgs)-1]); got != "hello" {
		t.Fatalf("last message = %q", got)
	}
	// The block is injected at conversion time, not stored in history.
	for _, m := range h.s.Agent.State().Messages {
		if u := m.Message.AsUser(); u != nil {
			if s, _ := u.Content.(string); strings.HasPrefix(s, sections.BlockHeader) {
				t.Fatal("startup block leaked into history")
			}
		}
	}
}

func TestSections_TrustGate(t *testing.T) {
	h := newSectionsHarness(t)
	_ = h.store.Set("mood", "trusted")
	_ = h.store.Set("rogue", "untrusted or disabled")
	_ = os.WriteFile(filepath.Join(h.store.Dir(), "orphan.md"), []byte("ownerless"), 0o644)
	h.s.SetSectionFilter(allowOnly("mood"))

	first := userText(h.prompt(t, "hi")[0])
	if !strings.Contains(first, "trusted") {
		t.Fatalf("eligible section missing: %q", first)
	}
	for _, bad := range []string{"rogue", "ownerless", "orphan"} {
		if strings.Contains(first, bad) {
			t.Fatalf("ineligible section %q emitted: %q", bad, first)
		}
	}
}

func TestSections_NoFilterNoBlock(t *testing.T) {
	h := newSectionsHarness(t)
	_ = h.store.Set("mood", "x")
	msgs := h.prompt(t, "hi")
	if len(msgs) != 1 || userText(msgs[0]) != "hi" {
		t.Fatalf("expected only the prompt, got %d msgs", len(msgs))
	}
}

func TestSections_HashChangeEmitsFullReplacement(t *testing.T) {
	h := newSectionsHarness(t)
	_ = h.store.Set("mood", "version one")
	h.s.SetSectionFilter(allowOnly("mood"))

	block := userText(h.prompt(t, "one")[0])

	// Unchanged: no update message.
	msgs := h.prompt(t, "two")
	if userText(msgs[0]) != block {
		t.Fatal("startup block not byte-identical across turns")
	}
	for _, m := range msgs[1:] {
		if strings.Contains(userText(m), "[SYS_EXT section=") {
			t.Fatalf("unexpected update without change: %q", userText(m))
		}
	}

	_ = h.store.Set("mood", "version two")
	msgs = h.prompt(t, "three")
	if userText(msgs[0]) != block {
		t.Fatal("startup block changed after a section update")
	}
	upd := userText(msgs[len(msgs)-2])
	if upd != "[SYS_EXT section=mood v2 replaces v1]\nversion two\n[/SYS_EXT]" {
		t.Fatalf("update = %q", upd)
	}
	if userText(msgs[len(msgs)-1]) != "three" {
		t.Fatal("update must precede the user prompt")
	}

	// Emitted once only.
	msgs = h.prompt(t, "four")
	n := 0
	for _, m := range msgs {
		if strings.HasPrefix(userText(m), "[SYS_EXT section=mood") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one update in history, got %d", n)
	}

	// Clearing emits a cleared replacement.
	_ = h.store.Clear("mood")
	msgs = h.prompt(t, "five")
	if got := userText(msgs[len(msgs)-2]); !strings.HasPrefix(got, "[SYS_EXT section=mood v3 replaces v2]\n(section cleared") {
		t.Fatalf("clear update = %q", got)
	}
}

func TestSections_ViewFormat(t *testing.T) {
	h := newSectionsHarness(t)
	_ = h.store.Set("mood", "abc")
	h.s.SetSectionFilter(allowOnly("mood"))
	if v := h.s.Sections(); v.Started || !strings.Contains(v.Format(), "Not injected yet") {
		t.Fatalf("pre-turn view wrong: %+v", v)
	}
	h.prompt(t, "hi")
	_ = h.store.Set("mood", "changed")
	out := h.s.Sections().Format()
	for _, want := range []string{"[section=mood]\nabc", "Changed since injection", "mood  ~"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSections_ResnapshotAfterHistoryReset(t *testing.T) {
	h := newSectionsHarness(t)
	_ = h.store.Set("mood", "version one")
	h.s.SetSectionFilter(allowOnly("mood"))
	h.prompt(t, "one")
	_ = h.store.Set("mood", "version two")
	h.prompt(t, "two") // emits the v2 replacement into history

	// A fresh history loses that replacement; the block must carry v2.
	if _, err := h.s.NewSessionCmd(); err != nil {
		t.Fatal(err)
	}
	msgs := h.prompt(t, "three")
	if len(msgs) != 2 || !strings.Contains(userText(msgs[0]), "[section=mood]\nversion two") {
		t.Fatalf("expected re-snapshotted block + prompt, got %d msgs: %q", len(msgs), userText(msgs[0]))
	}
}

func TestSectionTracker_DropsReplacementsPredatingSnapshot(t *testing.T) {
	store := sections.NewStore(t.TempDir())
	_ = store.Set("mood", "fresh")
	tr := newSectionTracker(store)
	tr.setAllowed(allowOnly("mood"))

	user := func(s string) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, 1)) }
	// Resumed history carrying a replacement from an earlier process.
	history := []agent.AgentMessage{user("old q"), user(sections.RenderUpdate("mood", "stale", 2, 1)), user("new q")}
	got, err := tr.convert(history, fmsg.ConvertToLLM)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !strings.Contains(userText(got[0]), "fresh") {
		t.Fatalf("stale replacement not dropped: %d msgs", len(got))
	}
	for _, m := range got {
		if strings.Contains(userText(m), "stale") {
			t.Fatal("stale replacement reached the LLM")
		}
	}

	// A replacement emitted after the snapshot is kept.
	_ = store.Set("mood", "newer")
	history = append(history, tr.updateMessages()...)
	got, _ = tr.convert(history, fmsg.ConvertToLLM)
	if !strings.Contains(userText(got[len(got)-1]), "newer") {
		t.Fatal("in-session replacement dropped")
	}
}
