// Artifact is a neutral, store-agnostic view of a session entry passed to
// compaction (and other session consumers). It hides the session-store
// representation behind a stable shape so that the compaction package does
// not need to import store types just to walk the conversation.
//
// EntryID is the stable session-store ID; later phases of the compaction
// rework use it as the pointer-stub key when masking large/old observations.
package session

import (
	"encoding/json"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// ArtifactKind classifies an Artifact for cut-point and serialization logic.
type ArtifactKind int

const (
	ArtifactUnknown ArtifactKind = iota
	ArtifactUser
	ArtifactAssistant
	ArtifactToolResult
	ArtifactBashExecution
	ArtifactBranchSummary
	ArtifactCompactionSummary
	ArtifactCustom
)

// Artifact is a neutral representation of a session entry suitable for
// consumption by compaction and other session features.
//
// Message is set for everything that maps to an agent.AgentMessage. For
// compaction summaries, Summary/TokensBefore/Details carry the entry's
// structured fields.
type Artifact struct {
	// EntryID is the stable session-store ID. Used as the pointer-stub key.
	EntryID string

	// Kind classifies the artifact.
	Kind ArtifactKind

	// Message is the corresponding agent message, when applicable.
	// Zero-valued for compaction/branch_summary entries that don't reify
	// into a message until consumed.
	Message agent.AgentMessage

	// ToolName is set for ToolResult/Assistant artifacts when a single
	// tool name is identifiable; empty otherwise.
	ToolName string

	// Bytes is a rough size of the artifact's textual payload; used for
	// stub-vs-keep decisions in later phases. 0 means "unknown".
	Bytes int
}

// CompactionOutput is the result of generating a compaction summary,
// before persistence. ApplyCompaction consumes this to write the
// compaction entry and rebuild the in-memory message list.
type CompactionOutput struct {
	Summary          string
	FirstKeptEntryID string
	TokensBefore     int
	// DetailsJSON is the marshalled CompactionDetails (read/modified files).
	// Pass nil if no details are tracked.
	DetailsJSON []byte
	// FromHook indicates the compaction was produced by an extension hook
	// rather than the runner.
	FromHook bool
}

// CompactionArtifacts returns the current branch's entries as a flat list
// of neutral Artifacts plus the previous compaction entry's summary and its
// index in the artifact slice (or -1 if there isn't one yet).
//
// Compaction-entry boundary markers themselves are represented as
// ArtifactCompactionSummary artifacts so callers can detect them by Kind
// without importing store.
func (s *AgentSession) CompactionArtifacts() (artifacts []Artifact, prevSummary string, prevCompactionIdx int) {
	prevCompactionIdx = -1
	if s == nil || s.SessionStore == nil {
		return nil, "", -1
	}
	entries := s.SessionStore.GetBranch("")
	artifacts = make([]Artifact, 0, len(entries))
	for _, e := range entries {
		a := artifactFromEntry(e)
		if a.Kind == ArtifactUnknown {
			continue
		}
		if e.Type == "compaction" {
			prevCompactionIdx = len(artifacts)
			prevSummary = e.Summary
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, prevSummary, prevCompactionIdx
}

// artifactFromEntry maps a store.SessionEntry to an Artifact. This is the
// single place that knows about store.SessionEntry shape.
func artifactFromEntry(e *store.SessionEntry) Artifact {
	a := Artifact{EntryID: e.ID}
	switch e.Type {
	case "message":
		if len(e.RawMessage) == 0 {
			return Artifact{}
		}
		var msg ai.Message
		if err := json.Unmarshal(e.RawMessage, &msg); err != nil {
			return Artifact{}
		}
		am := agent.NewAgentMessage(msg)
		a.Message = am
		switch am.Role() {
		case "user":
			a.Kind = ArtifactUser
		case "assistant":
			a.Kind = ArtifactAssistant
			if asst := am.Message.AsAssistant(); asst != nil {
				for _, b := range asst.Content {
					if b.ToolCall != nil {
						a.ToolName = b.ToolCall.Name
						break
					}
				}
			}
		case "toolResult":
			a.Kind = ArtifactToolResult
		case "bashExecution":
			a.Kind = ArtifactBashExecution
		case "branchSummary":
			a.Kind = ArtifactBranchSummary
		case "compactionSummary":
			a.Kind = ArtifactCompactionSummary
		default:
			a.Kind = ArtifactCustom
		}
	case "custom_message":
		if len(e.Content) == 0 || e.CustomType == "" {
			return Artifact{}
		}
		ts, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		var content any
		_ = json.Unmarshal(e.Content, &content)
		cm := &store.CustomMessage{
			Role:       "custom",
			CustomType: e.CustomType,
			Content:    content,
			Display:    e.Display,
			Timestamp:  ts.UnixMilli(),
		}
		a.Kind = ArtifactCustom
		a.Message = agent.AgentMessage{Custom: cm}
	case "branch_summary":
		if e.Summary == "" {
			return Artifact{}
		}
		ts, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		a.Kind = ArtifactBranchSummary
		a.Message = store.CreateBranchSummaryMessage(e.Summary, e.FromID, ts)
	case "compaction":
		if e.Summary == "" {
			return Artifact{}
		}
		ts, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		a.Kind = ArtifactCompactionSummary
		a.Message = store.CreateCompactionSummaryMessage(e.Summary, e.TokensBefore, ts)
	default:
		return Artifact{}
	}
	return a
}

// ApplyCompaction persists a compaction entry to the session store and
// rebuilds the agent's in-memory messages from the new context window.
//
// This is the canonical write path for compaction; callers should not
// touch SessionStore.AppendCompaction or Agent.ReplaceMessages directly.
func (s *AgentSession) ApplyCompaction(out CompactionOutput) error {
	if s == nil || s.SessionStore == nil {
		return nil
	}
	s.SessionStore.AppendCompaction(
		out.Summary,
		out.FirstKeptEntryID,
		out.TokensBefore,
		out.DetailsJSON,
		out.FromHook,
	)
	if s.Agent != nil {
		ctx := s.SessionStore.BuildSessionContext()
		s.Agent.ReplaceMessages(ctx.Messages)
	}
	return nil
}
