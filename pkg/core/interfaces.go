package core

// This file defines minimal dependency-injection interfaces for AgentSession.
// Each interface captures exactly the methods AgentSession (and its internal
// helpers in compaction/runner.go and export.go) calls on a concrete type.
//
// Phase 4b will replace the concrete pointer fields in AgentSessionOptions
// with these interfaces. For now they serve as documentation and compile-time
// checks that the concrete types satisfy the contracts.

import (
	"encoding/json"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/session"
)

// ============================================================================
// SessionStore — session persistence + retrieval
// ============================================================================

// SessionStore abstracts the session persistence layer used by AgentSession.
// The concrete implementation is *session.SessionManager.
type SessionStore interface {
	// Identity
	GetSessionID() string
	GetSessionFile() string
	GetSessionName() string
	GetLeafID() string

	// Read
	GetBranch(fromID string) []*session.SessionEntry
	GetEntry(id string) *session.SessionEntry
	GetEntries() []*session.SessionEntry
	BuildSessionContext() session.SessionContext

	// Lifecycle
	NewSession(opts *session.NewSessionOptions) string
	SetSessionFile(filePath string)

	// Append operations
	AppendAgentMessage(msg agent.AgentMessage) string
	AppendCustomEntry(customType string, data json.RawMessage) string
	AppendModelChange(provider, modelID string) string
	AppendThinkingLevelChange(thinkingLevel string) string
	AppendCommandEntry(command, args string) string
	AppendSessionInfo(name string) string
	AppendPlanUpdate(title string, entries []agent.PlanEntry, metadata map[string]string) string
	AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, details json.RawMessage, fromHook bool) string

	// Branching
	Branch(branchFromID string)
	BranchWithSummary(branchFromID string, summary string, details json.RawMessage, fromHook bool) string
	CreateBranchedSession(leafID string) (string, error)
}

// ============================================================================
// SettingsReader — runtime settings access
// ============================================================================

// SettingsReader abstracts the subset of SettingsManager that AgentSession
// reads during normal operation. The concrete implementation is
// *config.SettingsManager.
type SettingsReader interface {
	GetEnableSkillCommands() bool
	GetShellCommandPrefix() string
	Reload()
}

// ============================================================================
// ModelFinder — model lookup
// ============================================================================

// ModelFinder abstracts the model registry lookup used by AgentSession.
// The concrete implementation is *models.ModelRegistry.
type ModelFinder interface {
	Find(provider, modelID string) *ai.Model
}

// ============================================================================
// Compile-time interface satisfaction checks
// ============================================================================

// These blank assignments ensure the concrete types continue to satisfy the
// interfaces above. They produce a clear compile error if a method is
// removed or renamed on the concrete type.
var (
	_ SessionStore   = (*session.SessionManager)(nil)
	_ SettingsReader = (*config.SettingsManager)(nil)
	_ ModelFinder    = (*models.ModelRegistry)(nil)
)
