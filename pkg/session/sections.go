package session

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/sections"
)

// sectionTracker owns what fir injects from persistent extension sections.
//
// The startup block is snapshotted once (lazily, before the first LLM call)
// and prepended to every LLM context as a single [SYS_EXT] message right
// after the system prompt; it is never stored in history, so it stays
// byte-identical for the session. Later hash changes are emitted as full
// replacement messages appended to history at the next turn boundary.
type sectionTracker struct {
	store *sections.Store

	mu      sync.Mutex
	allowed func(name string) bool
	started bool
	block   string
	base    int // history length when the block was snapshotted
	emitted map[string]emittedSection
}

type emittedSection struct {
	hash string
	ver  int
}

func newSectionTracker(store *sections.Store) *sectionTracker {
	return &sectionTracker{store: store, emitted: map[string]emittedSection{}}
}

// setAllowed installs the owner filter. Only effective before the snapshot.
func (t *sectionTracker) setAllowed(fn func(string) bool) {
	t.mu.Lock()
	t.allowed = fn
	t.mu.Unlock()
}

// current reads the eligible sections from disk. Caller holds t.mu.
func (t *sectionTracker) current() []sections.Section {
	if t.allowed == nil {
		return nil
	}
	all, _ := t.store.ReadAll()
	return sections.Filter(all, t.allowed)
}

// ensureStarted snapshots the startup block. Caller holds t.mu.
func (t *sectionTracker) ensureStarted() {
	if t.started {
		return
	}
	t.started = true
	secs := t.current()
	t.block = sections.RenderBlock(secs)
	for _, s := range secs {
		t.emitted[s.Name] = emittedSection{hash: s.Hash(), ver: 1}
	}
}

// reset forgets the snapshot after history is replaced (new/switched session,
// tree navigation, compaction): earlier replacement messages may be gone, so
// the next LLM call re-snapshots every section from disk.
func (t *sectionTracker) reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.started = false
	t.block = ""
	t.base = 0
	t.emitted = map[string]emittedSection{}
	t.mu.Unlock()
}

// updates returns a replacement message for every section whose content
// hash changed since it was last emitted (including new and cleared
// sections), and records them as emitted.
func (t *sectionTracker) updates() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return nil // the next LLM call snapshots everything
	}
	secs := t.current()
	var out []string
	seen := make(map[string]bool, len(secs))
	for _, s := range secs {
		seen[s.Name] = true
		h := s.Hash()
		prev, ok := t.emitted[s.Name]
		if ok && prev.hash == h {
			continue
		}
		ver := prev.ver + 1
		out = append(out, sections.RenderUpdate(s.Name, s.Text, ver, prev.ver))
		t.emitted[s.Name] = emittedSection{hash: h, ver: ver}
	}
	for _, name := range sortedKeys(t.emitted) {
		prev := t.emitted[name]
		if seen[name] || prev.hash == "" {
			continue
		}
		ver := prev.ver + 1
		out = append(out, sections.RenderUpdate(name, "", ver, prev.ver))
		t.emitted[name] = emittedSection{hash: "", ver: ver}
	}
	return out
}

func sortedKeys(m map[string]emittedSection) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// convert is the session's ConvertToLLM: it drops replacement messages that
// predate the current snapshot (stale after resume, tree navigation or
// compaction — the fresh block supersedes them), converts, and prepends the
// startup block as the first message.
func (t *sectionTracker) convert(messages []agent.AgentMessage, conv func([]agent.AgentMessage) ([]ai.Message, error)) ([]ai.Message, error) {
	t.mu.Lock()
	if !t.started {
		t.ensureStarted()
		t.base = len(messages)
	}
	block, base := t.block, min(t.base, len(messages))
	t.mu.Unlock()

	kept := messages
	for i := 0; i < base; i++ {
		if isSectionUpdate(messages[i]) {
			kept = make([]agent.AgentMessage, 0, len(messages))
			for j, m := range messages {
				if j >= base || !isSectionUpdate(m) {
					kept = append(kept, m)
				}
			}
			break
		}
	}
	msgs, err := conv(kept)
	if err != nil || block == "" {
		return msgs, err
	}
	out := make([]ai.Message, 0, len(msgs)+1)
	out = append(out, ai.NewUserMsg(block, 0))
	return append(out, msgs...), nil
}

func isSectionUpdate(m agent.AgentMessage) bool {
	if u := m.Message.AsUser(); u != nil {
		if text, ok := u.Content.(string); ok {
			return strings.HasPrefix(text, sections.UpdatePrefix)
		}
	}
	return false
}

// updateMessages wraps pending updates as agent messages for the next turn.
func (t *sectionTracker) updateMessages() []agent.AgentMessage {
	if t == nil {
		return nil
	}
	ups := t.updates()
	if len(ups) == 0 {
		return nil
	}
	ts := time.Now().UnixMilli()
	out := make([]agent.AgentMessage, len(ups))
	for i, u := range ups {
		out[i] = agent.NewAgentMessage(ai.NewUserMsg(u, ts))
	}
	return out
}

// SectionsView describes what fir injects from extension sections.
type SectionsView struct {
	Dir     string             // sections directory
	Started bool               // startup block snapshotted (first turn ran)
	Block   string             // startup block as injected ("" = none)
	Current []sections.Section // eligible sections on disk right now
	Pending []string           // owners whose replacement the next turn will emit
}

// SetSectionFilter installs the owner filter deciding which sections may be
// emitted. Must be called before the first turn to affect the startup block.
func (s *AgentSession) SetSectionFilter(allowed func(name string) bool) {
	s.sections.setAllowed(allowed)
}

// SectionStore returns the store this session injects sections from.
// Extension writes must target the same store.
func (s *AgentSession) SectionStore() *sections.Store {
	return s.sections.store
}

// Sections reports the injected startup block and current on-disk state
// without consuming pending updates.
func (s *AgentSession) Sections() SectionsView {
	t := s.sections
	t.mu.Lock()
	defer t.mu.Unlock()
	v := SectionsView{Dir: t.store.Dir(), Started: t.started, Block: t.block, Current: t.current()}
	if !t.started {
		// Nothing injected yet: the first turn will snapshot everything.
		return v
	}
	for _, sec := range v.Current {
		if prev, ok := t.emitted[sec.Name]; !ok || prev.hash != sec.Hash() {
			v.Pending = append(v.Pending, sec.Name)
		}
	}
	for _, name := range sortedKeys(t.emitted) {
		if t.emitted[name].hash != "" && !containsSection(v.Current, name) {
			v.Pending = append(v.Pending, name+" (cleared)")
		}
	}
	return v
}

func containsSection(secs []sections.Section, name string) bool {
	for _, s := range secs {
		if s.Name == name {
			return true
		}
	}
	return false
}

// Format renders the view for the /sections command.
func (v SectionsView) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Extension sections (dir: %s)\n", v.Dir)
	if !v.Started {
		b.WriteString("\nNot injected yet — the first turn snapshots eligible sections.\n")
	} else if v.Block == "" {
		b.WriteString("\nStartup block: none injected.\n")
	} else {
		fmt.Fprintf(&b, "\nStartup block (~%d tokens, after the system prompt):\n\n%s\n", sections.Tokens(v.Block), v.Block)
	}
	if len(v.Pending) > 0 {
		fmt.Fprintf(&b, "\nChanged since injection (replaced on next turn): %s\n", strings.Join(v.Pending, ", "))
	}
	if len(v.Current) == 0 {
		b.WriteString("\nNo eligible sections on disk.\n")
	} else {
		b.WriteString("\nEligible on disk:\n")
		for _, s := range v.Current {
			fmt.Fprintf(&b, "  %s  ~%d tokens  %s\n", s.Name, sections.Tokens(s.Text), s.Hash())
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
