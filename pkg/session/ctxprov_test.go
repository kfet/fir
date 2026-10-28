package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/kfet/ai"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/sections"
	"github.com/stretchr/testify/require"
)

func big(s string) string { return strings.Repeat(s+" ", 120) }

func ctxTestPrompt() core.Context {
	agents := big("go rules")
	sys := big("You are fir.") +
		"\n\nThe following skills provide specialized instructions for specific tasks.\n<available_skills>\n" + big("skill") + "</available_skills>" +
		ai.SystemPromptCacheBoundary +
		"\n\n# Project Context\n\nProject-specific instructions and guidelines:\n\n## /repo/AGENTS.md\n\n" + agents + "\n## Inner header\n\n" + big("more") + "\n\n" +
		"\nCurrent date: 2026-01-01\nCurrent working directory: /repo"
	block := sections.RenderBlock([]sections.Section{
		{Name: "mood", Text: "LESSONS\nL1 Always test.\n    bet: none\n" + big("mood text")},
		{Name: "mcp", Text: "tiny"},
	})
	read := core.NewAssistantMsg(core.AssistantMessage{Content: []core.AssistantContent{
		{ToolCall: &core.ToolCall{ID: "t1", Name: "read", Arguments: map[string]any{"path": "/s/x/SKILL.md"}}},
		{ToolCall: &core.ToolCall{ID: "t2", Name: "bash", Arguments: map[string]any{"command": "ls"}}},
	}})
	return core.Context{
		SystemPrompt: sys,
		Messages: []core.Message{
			core.NewUserMsg(block, 1),
			core.NewUserMsg(big("please do the thing"), 2),
			core.NewUserMsg("[SYS_EXT]\n"+big("injected"), 3),
			read,
			core.NewToolResultMsg(core.ToolResultMessage{ToolCallID: "t1", ToolName: "read", Content: []core.ToolResultContent{{Type: "text", Text: big("skill body")}}, Meta: map[string]string{"hash": "h"}}),
			core.NewToolResultMsg(core.ToolResultMessage{ToolCallID: "t2", ToolName: "bash", Content: []core.ToolResultContent{{Type: "text", Text: "ok"}}}),
		},
		Tools: []core.Tool{
			{Name: "mcp__gmail__search", Description: big("search mail")},
			{Name: "ctx_used", Description: "log"},
		},
	}
}

func findChunk(cs []CtxChunk, src, owner string) *CtxChunk {
	for i := range cs {
		if cs[i].Source == src && cs[i].Owner == owner {
			return &cs[i]
		}
	}
	return nil
}

func TestCtxChunks_SourcesOwnersAndTags(t *testing.T) {
	p := ctxTestPrompt()
	orig := p.Messages[4].AsToolResult().Meta
	out, cs := CtxChunks(p, map[string]string{"ctx_used": "ctx"})

	for _, want := range [][2]string{
		{CtxSrcSystem, "fir"}, {CtxSrcSkills, "skills"}, {CtxSrcAgents, "/repo/AGENTS.md"},
		{CtxSrcSection, "mood"}, {CtxSrcSection, "mcp"}, {CtxSrcLesson, "mood"},
		{CtxSrcUser, "user"}, {CtxSrcSysExt, "ext"}, {CtxSrcSkillRead, "/s/x/SKILL.md"},
		{CtxSrcToolResult, "bash"}, {CtxSrcToolSchema, "mcp:gmail"}, {CtxSrcToolSchema, "ctx"},
	} {
		require.NotNil(t, findChunk(cs, want[0], want[1]), "missing %v", want)
	}
	// Inner "## Inner header" stays inside the AGENTS.md chunk.
	require.Nil(t, findChunk(cs, CtxSrcAgents, "Inner header"))

	ag := findChunk(cs, CtxSrcAgents, "/repo/AGENTS.md")
	require.Contains(t, out.SystemPrompt, ctxTag(ag.Addr)+"\n## /repo/AGENTS.md")
	// Every tag sits on the right side of the cache boundary.
	stable, _ := ai.SplitSystemPrompt(out.SystemPrompt)
	require.Contains(t, stable, ctxTag(findChunk(cs, CtxSrcSkills, "skills").Addr))
	require.NotContains(t, stable, ctxTag(ag.Addr))
	origStable, _ := ai.SplitSystemPrompt(p.SystemPrompt)
	require.Equal(t, strings.Count(origStable, "You are fir."), strings.Count(stable, "You are fir."))

	block, _ := ctxUserText(*out.Messages[0].AsUser())
	les := findChunk(cs, CtxSrcLesson, "mood")
	require.Contains(t, block, "L1 Always test. "+ctxTag(les.Addr)+"\n")
	require.Contains(t, block, "[section=mood] "+ctxTag(findChunk(cs, CtxSrcSection, "mood").Addr)+"\n")
	require.Contains(t, block, "[section=mcp]\ntiny") // below floor: untagged

	u, _ := ctxUserText(*out.Messages[1].AsUser())
	require.True(t, strings.HasPrefix(u, ctxTag(findChunk(cs, CtxSrcUser, "user").Addr)+" "))

	tr := out.Messages[4].AsToolResult()
	require.Equal(t, findChunk(cs, CtxSrcSkillRead, "/s/x/SKILL.md").Addr, tr.Meta["ctx"])
	require.Equal(t, "h", tr.Meta["hash"])
	require.Empty(t, out.Messages[5].AsToolResult().Meta) // tiny result untagged

	require.Contains(t, out.Tools[0].Description, "[ctx:")
	require.Equal(t, "log", out.Tools[1].Description)

	// The input is never mutated.
	require.NotContains(t, p.SystemPrompt, "[ctx:")
	require.NotContains(t, p.Messages[0].AsUser().Content.(string), "[ctx:")
	require.Equal(t, orig, p.Messages[4].AsToolResult().Meta)
	require.NotContains(t, p.Tools[0].Description, "[ctx:")

	// Deterministic: same input, same output (cache-safe).
	out2, _ := CtxChunks(p, map[string]string{"ctx_used": "ctx"})
	require.Equal(t, out.SystemPrompt, out2.SystemPrompt)
}

func TestCtxChunks_DuplicateTextSharesAddr(t *testing.T) {
	text := big("same")
	_, cs := CtxChunks(core.Context{Messages: []core.Message{
		core.NewUserMsg(text, 1),
		core.NewToolResultMsg(core.ToolResultMessage{ToolName: "bash", Content: []core.ToolResultContent{{Type: "text", Text: text}}}),
	}}, nil)
	require.Len(t, cs, 2)
	require.Equal(t, cs[0].Addr, cs[1].Addr)
	require.NotEqual(t, cs[0].Source, cs[1].Source)
}

func TestCtxUserText_ContentBlocks(t *testing.T) {
	u := core.UserMessage{Content: []any{core.ImageContent{Type: "image"}, &core.TextContent{Type: "text", Text: big("hello")}}}
	nm, cs := chunkUser(u)
	require.Len(t, cs, 1)
	text, _ := ctxUserText(*nm.AsUser())
	require.True(t, strings.HasPrefix(text, "[ctx:"))
	_, isPtr := u.Content.([]any)[1].(*core.TextContent)
	require.True(t, isPtr)
	require.False(t, strings.HasPrefix(u.Content.([]any)[1].(*core.TextContent).Text, "[ctx:"))
}

func readCtxRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		rows = append(rows, m)
	}
	return rows
}

func TestCtxRecorder_WritesAndLatches(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sess.jsonl")
	on := true
	r := &ctxRecorder{
		target:  func() (string, string) { return file, "sid1" },
		enabled: func() bool { return on },
	}
	model := &core.Model{Provider: "anthropic", ID: "m1"}
	p := ctxTestPrompt()
	out := r.record(model, p)
	require.Contains(t, out.SystemPrompt, "[ctx:")
	on = false // latched: flipping later has no effect
	r.record(model, p)

	path := filepath.Join(dir, CtxFileName("sid1"))
	rows := readCtxRows(t, path)
	var srcs, turns int
	for _, row := range rows {
		switch row["kind"] {
		case "src":
			srcs++
			require.EqualValues(t, 1, row["turn"]) // first sight only
		case "turn":
			turns++
			require.Equal(t, "anthropic/m1", row["model"])
			require.NotEmpty(t, row["addrs"])
		}
	}
	require.Equal(t, 2, turns)
	require.Positive(t, srcs)

	// Resume: a fresh recorder continues the turn counter and dedupe set.
	r2 := &ctxRecorder{target: r.target, enabled: func() bool { return true }}
	r2.record(model, p)
	rows = readCtxRows(t, path)
	last := rows[len(rows)-1]
	require.EqualValues(t, 3, last["turn"])
	require.Equal(t, "turn", rows[len(rows)-2]["kind"])
}

func TestCtxRecorder_DisabledPassesThrough(t *testing.T) {
	dir := t.TempDir()
	r := &ctxRecorder{
		target:  func() (string, string) { return filepath.Join(dir, "s.jsonl"), "sid" },
		enabled: func() bool { return false },
	}
	p := ctxTestPrompt()
	out := r.record(nil, p)
	require.Equal(t, p.SystemPrompt, out.SystemPrompt)
	_, err := os.Stat(filepath.Join(dir, CtxFileName("sid")))
	require.True(t, os.IsNotExist(err))
}

func TestApplyContextProvenance_NoRecorder(t *testing.T) {
	p := ctxTestPrompt()
	require.Equal(t, p.SystemPrompt, applyContextProvenance("nobody", nil, p).SystemPrompt)
	require.Equal(t, p.SystemPrompt, applyContextProvenance("", nil, p).SystemPrompt)
}
