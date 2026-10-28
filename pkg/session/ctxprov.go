package session

// Context provenance ("ctx"): a per-call record of every chunk fir puts into
// the prompt, and a short address tag on the provider-bound copy so the model
// can cite chunks it used (via the ctx extension's ctx_used tool).
//
// The hook is the default stream function (defaultstream.go): it sees the
// exact core.Context sent to the provider. Only the per-call copy is tagged —
// history, transcript and compaction input are never touched. Tags derive
// from chunk text alone, so a chunk carries the same tag on every call and
// provider prompt caches stay valid. Tags go after a leading cache boundary,
// never before, so a volatile chunk can't leak into the stable prefix.
//
// Records go to ctx-<session-id>.jsonl next to the session transcript:
//
//	{"kind":"src","turn":N,"addr":"a3f09","source":"...","owner":"...","tokens":T,"hash":"..."}
//	{"kind":"turn","turn":N,"ts":"...","model":"provider/id","tokens":T,"addrs":[...]}
//
// A src row is written the first time (addr, source, owner) appears in the
// file; a turn row lists every addr present on that call, so a cite
// {turn, addr} joins to its turn row and through addr to its src rows.
// tokens is an estimate (bytes/4). Duplicates are one addr (same text) seen
// under two sources/owners.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	core "github.com/kfet/ai"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/sections"
)

// CtxExtensionName is the extension whose presence turns provenance on.
const CtxExtensionName = "ctx"

// ctxTagMinTokens: chunks below this are logged but not tagged, so the tags
// don't add the bloat they exist to measure. Lessons are always tagged.
const ctxTagMinTokens = 100

// Chunk sources.
const (
	CtxSrcSystem     = "system prompt"
	CtxSrcAgents     = "AGENTS.md"
	CtxSrcSection    = "SYS_EXT section"
	CtxSrcSysExt     = "SYS_EXT"
	CtxSrcSkills     = "skills index"
	CtxSrcToolSchema = "tool schema"
	CtxSrcLesson     = "lesson"
	CtxSrcSkillRead  = "skill read"
	CtxSrcToolResult = "tool result"
	CtxSrcUser       = "user msg"
)

// CtxChunk is one logged prompt chunk.
type CtxChunk struct {
	Addr   string `json:"addr"`
	Source string `json:"source"`
	Owner  string `json:"owner"`
	Tokens int    `json:"tokens"`
	Hash   string `json:"hash"`
}

// CtxFileName returns the provenance log name for a session id.
func CtxFileName(sessionID string) string { return "ctx-" + sessionID + ".jsonl" }

// ctxRecorder appends provenance rows for one agent session.
type ctxRecorder struct {
	// target returns the current transcript path and store session id; the
	// store may switch sessions (/new, /resume) under one agent.
	target func() (file, sid string)
	// enabled is consulted once, on the first call, then latched: tags must
	// not appear or vanish mid-session (cache break, unjoinable rows).
	enabled   func() bool
	toolOwner func() map[string]string // tool name -> extension name

	mu      sync.Mutex
	latched int // 0 undecided, 1 on, -1 off
	path    string
	turn    int
	seen    map[string]bool
}

var ctxRecorders sync.Map // agent session id -> *ctxRecorder

// EnableContextProvenance installs the provenance recorder for this session.
// enabled is evaluated on the first provider call and latched; toolOwner maps
// tool names to the extension that registered them (nil: none).
func (s *AgentSession) EnableContextProvenance(enabled func() bool, toolOwner func() map[string]string) {
	if s == nil || s.Agent == nil || s.SessionStore == nil {
		return
	}
	st := s.SessionStore
	ctxRecorders.Store(s.Agent.GetSessionID(), &ctxRecorder{
		target:    func() (string, string) { return st.GetSessionFile(), st.GetSessionID() },
		enabled:   enabled,
		toolOwner: toolOwner,
	})
}

func dropCtxRecorder(sessionID string) { ctxRecorders.Delete(sessionID) }

// applyContextProvenance records prompt's chunks and returns the tagged copy.
// Returns prompt unchanged when no recorder is active for sessionID.
func applyContextProvenance(sessionID string, model *core.Model, prompt core.Context) core.Context {
	if sessionID == "" {
		return prompt
	}
	v, ok := ctxRecorders.Load(sessionID)
	if !ok {
		return prompt
	}
	return v.(*ctxRecorder).record(model, prompt)
}

func (r *ctxRecorder) record(model *core.Model, prompt core.Context) core.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latched == 0 {
		r.latched = -1
		if r.enabled != nil && r.enabled() {
			r.latched = 1
		}
	}
	if r.latched < 0 {
		return prompt
	}
	file, sid := r.target()
	if file == "" || sid == "" {
		return prompt
	}
	var owners map[string]string
	if r.toolOwner != nil {
		owners = r.toolOwner()
	}
	tagged, chunks := CtxChunks(prompt, owners)

	path := filepath.Join(filepath.Dir(file), CtxFileName(sid))
	if path != r.path {
		r.path = path
		r.turn, r.seen = loadCtxState(path)
	}
	r.turn++
	r.write(model, chunks)
	return tagged
}

func (r *ctxRecorder) write(model *core.Model, chunks []CtxChunk) {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	addrs := make([]string, 0, len(chunks))
	present := map[string]bool{}
	total := 0
	for _, c := range chunks {
		total += c.Tokens
		if !present[c.Addr] {
			present[c.Addr] = true
			addrs = append(addrs, c.Addr)
		}
		k := c.Addr + "\x00" + c.Source + "\x00" + c.Owner
		if r.seen[k] {
			continue
		}
		r.seen[k] = true
		_ = enc.Encode(struct {
			Kind string `json:"kind"`
			Turn int    `json:"turn"`
			CtxChunk
		}{"src", r.turn, c})
	}
	m := ""
	if model != nil {
		m = string(model.Provider) + "/" + model.ID
	}
	_ = enc.Encode(map[string]any{
		"kind": "turn", "turn": r.turn, "ts": time.Now().UTC().Format(time.RFC3339),
		"model": m, "tokens": total, "addrs": addrs,
	})
	_ = w.Flush()
}

// loadCtxState resumes the turn counter and seen set from an existing log.
func loadCtxState(path string) (int, map[string]bool) {
	seen := map[string]bool{}
	turn := 0
	f, err := os.Open(path)
	if err != nil {
		return 0, seen
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var row struct {
			Kind string `json:"kind"`
			Turn int    `json:"turn"`
			CtxChunk
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		switch row.Kind {
		case "turn":
			turn = max(turn, row.Turn)
		case "src":
			seen[row.Addr+"\x00"+row.Source+"\x00"+row.Owner] = true
		}
	}
	return turn, seen
}

// ---------------------------------------------------------------------------
// Chunking (pure)

func ctxAddr(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:5]
}

func ctxHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

func ctxTokens(text string) int { return (len(text) + 3) / 4 }

func ctxTag(addr string) string { return "[ctx:" + addr + "]" }

func newChunk(text, source, owner string) CtxChunk {
	return CtxChunk{Addr: ctxAddr(text), Source: source, Owner: owner, Tokens: ctxTokens(text), Hash: ctxHash(text)}
}

// CtxChunks splits prompt into provenance chunks and returns a copy with
// address tags on chunks worth citing. owners maps tool name -> extension.
func CtxChunks(prompt core.Context, owners map[string]string) (core.Context, []CtxChunk) {
	var chunks []CtxChunk
	out := prompt

	sys, sc := chunkSystemPrompt(prompt.SystemPrompt)
	out.SystemPrompt = sys
	chunks = append(chunks, sc...)

	// tool call id -> read path, for "skill read" attribution.
	readPaths := map[string]string{}
	for _, m := range prompt.Messages {
		if a := m.AsAssistant(); a != nil {
			for _, c := range a.Content {
				if tc := c.ToolCall; tc != nil && strings.EqualFold(tc.Name, "read") {
					if p, _ := tc.Arguments["path"].(string); p != "" {
						readPaths[tc.ID] = p
					}
				}
			}
		}
	}

	out.Messages = make([]core.Message, len(prompt.Messages))
	for i, m := range prompt.Messages {
		out.Messages[i] = m
		switch {
		case m.AsUser() != nil:
			nm, mc := chunkUser(*m.AsUser())
			if nm != nil {
				out.Messages[i] = *nm
			}
			chunks = append(chunks, mc...)
		case m.AsToolResult() != nil:
			tr := *m.AsToolResult()
			var b strings.Builder
			for _, c := range tr.Content {
				if c.IsText() {
					b.WriteString(c.Text)
				}
			}
			text := b.String()
			src, owner := CtxSrcToolResult, tr.ToolName
			if p := readPaths[tr.ToolCallID]; strings.HasSuffix(p, "SKILL.md") {
				src, owner = CtxSrcSkillRead, p
			}
			c := newChunk(text, src, owner)
			chunks = append(chunks, c)
			if c.Tokens >= ctxTagMinTokens {
				meta := make(map[string]string, len(tr.Meta)+1)
				for k, v := range tr.Meta {
					meta[k] = v
				}
				meta["ctx"] = c.Addr
				tr.Meta = meta
				out.Messages[i] = core.NewToolResultMsg(tr)
			}
		}
	}

	if len(prompt.Tools) > 0 {
		out.Tools = make([]core.Tool, len(prompt.Tools))
		for i, t := range prompt.Tools {
			params, _ := json.Marshal(t.Parameters)
			c := newChunk(t.Name+"\n"+t.Description+"\n"+string(params), CtxSrcToolSchema, toolOwner(t.Name, owners))
			chunks = append(chunks, c)
			if c.Tokens >= ctxTagMinTokens {
				t.Description += "\n" + ctxTag(c.Addr)
			}
			out.Tools[i] = t
		}
	}
	return out, chunks
}

func toolOwner(name string, owners map[string]string) string {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		srv, _, _ := strings.Cut(rest, "__")
		return "mcp:" + srv
	}
	if o := owners[name]; o != "" {
		return o
	}
	return "core"
}

const (
	skillsMarker  = "\n\nThe following skills provide specialized instructions"
	skillsEnd     = "</available_skills>"
	projectMarker = "\n\n# Project Context\n\nProject-specific instructions and guidelines:\n\n"
	dateMarker    = "\nCurrent date: "
)

var agentsHeader = regexp.MustCompile(`(?m)^## ((?:/|[A-Za-z]:\\|~)[^\n]*)\n\n`)

type span struct {
	start, end    int
	source, owner string
}

// chunkSystemPrompt splits the system prompt at the skills index and the
// per-file Project Context entries; what remains is fir's own prompt.
func chunkSystemPrompt(sp string) (string, []CtxChunk) {
	if sp == "" {
		return sp, nil
	}
	var spans []span
	if i := strings.Index(sp, skillsMarker); i >= 0 {
		if j := strings.Index(sp[i:], skillsEnd); j >= 0 {
			spans = append(spans, span{i, i + j + len(skillsEnd), CtxSrcSkills, "skills"})
		}
	}
	if i := strings.Index(sp, projectMarker); i >= 0 {
		body := i + len(projectMarker)
		end := strings.LastIndex(sp, dateMarker)
		if end < body {
			end = len(sp)
		}
		locs := agentsHeader.FindAllStringSubmatchIndex(sp[body:end], -1)
		for k, l := range locs {
			s, e := body+l[0], end
			if k+1 < len(locs) {
				e = body + locs[k+1][0]
			}
			spans = append(spans, span{s, e, CtxSrcAgents, sp[body+l[2] : body+l[3]]})
		}
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	// Fill gaps with fir's own prompt.
	var all []span
	pos := 0
	for _, s := range spans {
		if s.start < pos {
			continue // overlap: malformed, keep the earlier span
		}
		if s.start > pos {
			all = append(all, span{pos, s.start, CtxSrcSystem, "fir"})
		}
		all = append(all, s)
		pos = s.end
	}
	if pos < len(sp) {
		all = append(all, span{pos, len(sp), CtxSrcSystem, "fir"})
	}

	var b strings.Builder
	var chunks []CtxChunk
	for _, s := range all {
		seg := sp[s.start:s.end]
		text := strings.TrimSpace(ai.FlattenSystemPrompt(seg))
		if text == "" {
			b.WriteString(seg)
			continue
		}
		c := newChunk(text, s.source, s.owner)
		chunks = append(chunks, c)
		if c.Tokens < ctxTagMinTokens {
			b.WriteString(seg)
			continue
		}
		// Insert after leading whitespace and any cache boundary, so the tag
		// of a volatile chunk stays in the volatile half.
		at := 0
		for {
			rest := seg[at:]
			trimmed := strings.TrimLeft(rest, " \t\n")
			at += len(rest) - len(trimmed)
			if !strings.HasPrefix(trimmed, ai.SystemPromptCacheBoundary) {
				break
			}
			at += len(ai.SystemPromptCacheBoundary)
		}
		b.WriteString(seg[:at])
		b.WriteString(ctxTag(c.Addr) + "\n")
		b.WriteString(seg[at:])
	}
	return b.String(), chunks
}

var (
	sectionRe       = regexp.MustCompile(`\n\[section=([^\]\n]+)\]\n`)
	sectionUpdateRe = regexp.MustCompile(`^\[SYS_EXT section=([^\s\]]+)`)
	lessonRe        = regexp.MustCompile(`(?m)^L\d+ [^\n]*(?:\n[ \t]+[^\n]*)*`)
)

// ctxUserText returns the text of a user message and a rebuild function that
// returns the message with prefix prepended to its first text block.
func ctxUserText(u core.UserMessage) (string, func(newText func(string) string) core.Message) {
	switch c := u.Content.(type) {
	case string:
		return c, func(f func(string) string) core.Message { return core.NewUserMsg(f(c), u.Timestamp) }
	case []any:
		var b strings.Builder
		first := -1
		for i, it := range c {
			var t string
			switch v := it.(type) {
			case core.TextContent:
				t = v.Text
			case *core.TextContent:
				t = v.Text
			default:
				continue
			}
			if first < 0 {
				first = i
			}
			b.WriteString(t)
		}
		return b.String(), func(f func(string) string) core.Message {
			if first < 0 {
				return core.NewUserMsg(c, u.Timestamp)
			}
			cp := make([]any, len(c))
			copy(cp, c)
			var tc core.TextContent
			switch v := c[first].(type) {
			case core.TextContent:
				tc = v
			case *core.TextContent:
				tc = *v
			}
			tc.Text = f(tc.Text)
			cp[first] = tc
			return core.NewUserMsg(cp, u.Timestamp)
		}
	}
	return "", nil
}

func chunkUser(u core.UserMessage) (*core.Message, []CtxChunk) {
	text, rebuild := ctxUserText(u)
	if text == "" || rebuild == nil {
		return nil, nil
	}
	if strings.HasPrefix(text, sections.BlockHeader) {
		return chunkSectionsBlock(text, rebuild)
	}
	src, owner := CtxSrcUser, "user"
	if m := sectionUpdateRe.FindStringSubmatch(text); m != nil {
		src, owner = CtxSrcSection, m[1]
	} else if strings.HasPrefix(text, "[SYS_EXT") {
		src, owner = CtxSrcSysExt, "ext"
	}
	c := newChunk(text, src, owner)
	if c.Tokens < ctxTagMinTokens {
		return nil, []CtxChunk{c}
	}
	nm := rebuild(func(s string) string { return ctxTag(c.Addr) + " " + s })
	return &nm, []CtxChunk{c}
}

type insertion struct {
	at   int
	text string
}

// chunkSectionsBlock splits the startup [SYS_EXT sections] block per section,
// and lessons (lines "L<n> ...") out of their section.
func chunkSectionsBlock(text string, rebuild func(func(string) string) core.Message) (*core.Message, []CtxChunk) {
	var chunks []CtxChunk
	var ins []insertion
	locs := sectionRe.FindAllStringSubmatchIndex(text, -1)
	for _, l := range locs {
		name := text[l[2]:l[3]]
		bodyStart := l[1]
		bodyEnd := strings.Index(text[bodyStart:], "\n[/section]")
		if bodyEnd < 0 {
			continue
		}
		body := text[bodyStart : bodyStart+bodyEnd]
		lessonTok := 0
		for _, ll := range lessonRe.FindAllStringIndex(body, -1) {
			lesson := body[ll[0]:ll[1]]
			first, _, _ := strings.Cut(lesson, "\n")
			c := CtxChunk{Addr: ctxAddr(first), Source: CtxSrcLesson, Owner: name, Tokens: ctxTokens(lesson), Hash: ctxHash(lesson)}
			chunks = append(chunks, c)
			lessonTok += c.Tokens
			ins = append(ins, insertion{bodyStart + ll[0] + len(first), " " + ctxTag(c.Addr)})
		}
		c := newChunk(body, CtxSrcSection, name)
		c.Tokens = max(0, c.Tokens-lessonTok) // lessons are counted on their own
		chunks = append(chunks, c)
		if c.Tokens >= ctxTagMinTokens { // "[section=NAME]" -> "[section=NAME] [ctx:x]"
			ins = append(ins, insertion{l[1] - 1, " " + ctxTag(c.Addr)})
		}
	}
	if len(ins) == 0 {
		return nil, chunks
	}
	sort.Slice(ins, func(a, b int) bool { return ins[a].at > ins[b].at })
	nm := rebuild(func(s string) string {
		if s != text { // multi-block content: tag only when the block is whole
			return s
		}
		for _, in := range ins {
			s = s[:in.at] + in.text + s[in.at:]
		}
		return s
	})
	return &nm, chunks
}
