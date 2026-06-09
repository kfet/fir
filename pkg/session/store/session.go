// Ported from: packages/coding-agent/src/core/session-manager.ts
// Upstream hash: 1caadb2e
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// CurrentSessionVersion is the latest session file format version.
const CurrentSessionVersion = 3

// --- Session header ---

// SessionHeader is the first entry in a session JSONL file.
type SessionHeader struct {
	Type          string `json:"type"`                    // always "session"
	Version       int    `json:"version"`                 // CurrentSessionVersion
	ID            string `json:"id"`                      //
	Timestamp     string `json:"timestamp"`               //
	Cwd           string `json:"cwd"`                     //
	ParentSession string `json:"parentSession,omitempty"` //

	// FirVersion records the fir binary (agent) version that created this
	// session, written once at session start. This is distinct from the
	// Version field above, which is the transcript SCHEMA version. Absent on
	// sessions created before this field shipped.
	FirVersion string `json:"firVersion,omitempty"`

	// Commit is the VCS revision the binary was built from, when the build
	// carried a VCS stamp. Omitted when unavailable (e.g. `go test` builds).
	Commit string `json:"commit,omitempty"`

	// Invocation records the user-intent runtime config (--mcp-config flags,
	// --extension allowlist, model selection, ...) that was passed when this
	// session was first created. Stamped once at creation; never rewritten on
	// resume. Read on `fir -c` / `/resume` to restore the same config so the
	// resumed session has the same tool/extension/MCP set as the original.
	// Absent on sessions created before this feature shipped.
	Invocation *SessionInvocation `json:"invocation,omitempty"`
}

// --- Session entry ---

// SessionEntry represents a single entry in a session JSONL file (not the header).
type SessionEntry struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ParentID  string `json:"parentId"`
	Timestamp string `json:"timestamp"`

	// message
	RawMessage json.RawMessage `json:"message,omitempty"`

	// thinking_level_change
	ThinkingLevel string `json:"thinkingLevel,omitempty"`

	// model_change
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`

	// agent_version — records the fir binary version in effect at this point.
	// Emitted on resume when the running binary differs from the version that
	// last wrote to the session, so a session spanning two fir versions is
	// visible as a delta. Never sent to the LLM (see BuildSessionContext).
	FirVersion string `json:"firVersion,omitempty"`
	Commit     string `json:"commit,omitempty"`

	// compaction
	Summary          string          `json:"summary,omitempty"`
	FirstKeptEntryID string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int             `json:"tokensBefore,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
	FromHook         bool            `json:"fromHook,omitempty"`

	// branch_summary
	FromID string `json:"fromId,omitempty"`

	// custom / custom_message
	CustomType string          `json:"customType,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Display    bool            `json:"display,omitempty"`

	// label
	TargetID string `json:"targetId,omitempty"`
	Label    string `json:"label,omitempty"`

	// session_info
	Name string `json:"name,omitempty"`

	// command — records a slash command or bash invocation for audit/metering.
	// These entries are never included in the LLM context (see BuildSessionContextFromEntries).
	Command string `json:"command,omitempty"`
	Args    string `json:"args,omitempty"`

	// plan_update
	PlanEntries  json.RawMessage   `json:"planEntries,omitempty"`
	PlanTitle    string            `json:"planTitle,omitempty"`
	PlanMetadata map[string]string `json:"planMetadata,omitempty"`
}

// GetParentID returns the parent entry ID, or empty string for root entries.
func (e *SessionEntry) GetParentID() string {
	return e.ParentID
}

// --- Session tree node ---

// SessionTreeNode is a tree node for GetTree().
type SessionTreeNode struct {
	Entry    *SessionEntry
	Children []*SessionTreeNode
	Label    string
}

// --- Session context ---

// SessionContext is the resolved context for the LLM from a session.
type SessionContext struct {
	Messages      []agent.AgentMessage
	ThinkingLevel string
	Model         *SessionModelRef
	PlanEntries   []agent.PlanEntry
	PlanTitle     string
	PlanMetadata  map[string]string
}

// SessionModelRef identifies a model from the session.
type SessionModelRef struct {
	Provider string
	ModelID  string
}

// --- Session list info ---

// SessionListInfo holds metadata about a session for listing/display.
type SessionListInfo struct {
	Path              string
	ID                string
	Cwd               string
	Name              string
	ParentSessionPath string
	Created           time.Time
	Modified          time.Time
	MessageCount      int
	FirstMessage      string
}

// --- NewSessionOptions ---

// NewSessionOptions configures session creation.
type NewSessionOptions struct {
	ParentSession string
}

// --- SessionStore ---

// SessionStore manages conversation sessions as append-only trees stored in JSONL files.
type SessionStore struct {
	mu          sync.RWMutex
	sessionID   string
	sessionFile string
	sessionDir  string
	cwd         string
	persist     bool
	flushed     bool
	header      *SessionHeader
	entries     []*SessionEntry
	byID        map[string]*SessionEntry
	labelsById  map[string]string
	leafID      string       // empty = before first entry
	lock        *sessionLock // flock on .meta.json; nil if in-memory or lock failed
	resumed     bool         // true iff opened an existing session file (header loaded from disk)

	// observables is the session-scoped sidecar of cards exposed to
	// extensions and observers. Bound to <sessionFile>.cards (or
	// in-memory for non-persisted stores). Owned by the SessionStore
	// so it follows the session across newSession / setSessionFile /
	// CreateBranchedSession. Never nil for a constructed store.
	observables *ObservableStore
}

// NewSessionStore creates a persisted session.
func NewSessionStore(cwd, sessionDir string) *SessionStore {
	ss := &SessionStore{
		cwd:        cwd,
		sessionDir: sessionDir,
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	if sessionDir != "" {
		os.MkdirAll(sessionDir, 0755)
	}
	ss.newSession(nil)
	return ss
}

// InMemorySessionStore creates a non-persisted session.
func InMemorySessionStore(cwd ...string) *SessionStore {
	c := ""
	if len(cwd) > 0 {
		c = cwd[0]
	}
	ss := &SessionStore{
		cwd:        c,
		persist:    false,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	ss.newSession(nil)
	return ss
}

// OpenSessionStore opens a specific session file. Returns the SessionStore
// and whether the session was forked (because it was active in another process).
func OpenSessionStore(filePath string, sessionDir ...string) (*SessionStore, bool) {
	dir := ""
	if len(sessionDir) > 0 {
		dir = sessionDir[0]
	} else {
		dir = filepath.Dir(filePath)
	}

	ss := &SessionStore{
		sessionDir: dir,
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	forked := ss.setSessionFile(filePath)
	return ss, forked
}

// ContinueRecentSession continues the most recent session, or creates new.
// Returns the SessionStore and whether the session was forked (because it
// was active in another process).
func ContinueRecentSession(cwd, sessionDir string) (*SessionStore, bool) {
	if most := findMostRecentSession(sessionDir); most != "" {
		ss := &SessionStore{
			cwd:        cwd,
			sessionDir: sessionDir,
			persist:    true,
			byID:       make(map[string]*SessionEntry),
			labelsById: make(map[string]string),
		}
		forked := ss.setSessionFile(most)
		return ss, forked
	}
	return NewSessionStore(cwd, sessionDir), false
}

// SetSessionFile switches to a different session file, loading its entries.
// If the session is locked by another process, it forks the session to
// preserve history. Returns true if the session was forked.
func (ss *SessionStore) SetSessionFile(filePath string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.setSessionFile(filePath)
}

// setSessionFile loads a session file, forking if locked by another process.
// Returns true if the session was forked.
func (ss *SessionStore) setSessionFile(filePath string) bool {
	absPath, _ := filepath.Abs(filePath)

	// Release any previously held lock before switching files.
	if ss.lock != nil {
		ss.lock.Close()
		ss.lock = nil
	}

	// Try to acquire the flock. If locked by another process, fork.
	forked := false
	if ss.persist {
		lock, ok := tryLockSession(absPath)
		if ok {
			ss.lock = lock
		} else if ss.sessionDir != "" {
			// Session is active in another process — fork it.
			forkSS, err := ForkFrom(absPath, ss.cwd, ss.sessionDir)
			if err == nil {
				absPath = forkSS.GetSessionFile()
				forked = true
				firlog.Info("session forked from locked file", "original", filePath, "forked", absPath)
				// Acquire lock on the forked file.
				if lock, ok := tryLockSession(absPath); ok {
					ss.lock = lock
				}
			} else {
				firlog.Warn("session fork failed, proceeding without lock", "err", err)
			}
		}
	}

	ss.sessionFile = absPath
	firlog.Debug("loading session file", "path", absPath)

	if _, err := os.Stat(absPath); err == nil {
		header, entries := loadEntriesFromFile(absPath)

		if header == nil {
			// Corrupted - start fresh at this path
			ss.newSession(nil)
			ss.sessionFile = absPath
			ss.rewriteFile()
			ss.flushed = true
			ss.observables = NewObservableStore(CardsPath(absPath))
			return forked
		}

		ss.header = header
		ss.sessionID = header.ID
		ss.cwd = header.Cwd
		ss.entries = entries
		ss.buildIndex()
		ss.flushed = true
		ss.resumed = true
		firlog.Debug("session loaded", "sessionID", header.ID, "entries", len(entries))
	} else {
		ss.newSession(nil)
		ss.sessionFile = absPath
	}

	// Bind the observable cards store to this session file.
	// NewObservableStore reads existing cards from disk, so a resumed
	// session sees last-known state before any producer re-Puts — the
	// /reexec story.
	ss.observables = NewObservableStore(CardsPath(ss.sessionFile))
	return forked
}

// NewSession starts a new session. Returns the session file path (empty if in-memory).
func (ss *SessionStore) NewSession(opts *NewSessionOptions) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.newSession(opts)
}

func (ss *SessionStore) newSession(opts *NewSessionOptions) string {
	ss.sessionID = uuid.New().String()
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	ss.header = &SessionHeader{
		Type:       "session",
		Version:    CurrentSessionVersion,
		ID:         ss.sessionID,
		Timestamp:  ts,
		Cwd:        ss.cwd,
		FirVersion: currentFirVersion(),
		Commit:     firCommit(),
	}
	if opts != nil {
		ss.header.ParentSession = opts.ParentSession
	}

	ss.entries = nil
	ss.byID = make(map[string]*SessionEntry)
	ss.labelsById = make(map[string]string)
	ss.leafID = ""
	ss.flushed = false
	ss.resumed = false

	if ss.persist && ss.sessionDir != "" {
		// Release any previously held lock before creating a new session file.
		if ss.lock != nil {
			ss.lock.Close()
			ss.lock = nil
		}
		fileTs := strings.NewReplacer(":", "-", ".", "-").Replace(ts)
		ss.sessionFile = filepath.Join(ss.sessionDir, fmt.Sprintf("%s_%s.jsonl", fileTs, ss.sessionID))
		// Acquire flock on the new session file.
		if lock, ok := tryLockSession(ss.sessionFile); ok {
			ss.lock = lock
		}
		// Write the header immediately so observers (e.g. `fir observe`)
		// can tail the file from byte 0 without missing the first turn.
		// This also flips us into append-only mode for subsequent entries:
		// persistEntry no longer takes the rewriteFile path on first
		// persist, which would have been non-atomic vs concurrent readers.
		ss.writeHeaderOnly()
	}

	// Fresh session: brand-new cards store at the new file's path.
	ss.observables = NewObservableStore(CardsPath(ss.sessionFile))

	firlog.Debug("new session created", "sessionID", ss.sessionID, "file", ss.sessionFile)
	return ss.sessionFile
}

func (ss *SessionStore) buildIndex() {
	ss.byID = make(map[string]*SessionEntry)
	ss.labelsById = make(map[string]string)
	ss.leafID = ""

	for _, entry := range ss.entries {
		ss.byID[entry.ID] = entry
		ss.leafID = entry.ID

		if entry.Type == "label" {
			if entry.Label != "" {
				ss.labelsById[entry.TargetID] = entry.Label
			} else {
				delete(ss.labelsById, entry.TargetID)
			}
		}
	}
}

func (ss *SessionStore) generateID() string {
	for i := 0; i < 100; i++ {
		id := uuid.New().String()[:12]
		if _, ok := ss.byID[id]; !ok {
			return id
		}
	}
	// Exhausted retries (practically impossible with 48-bit IDs).
	// Fall back to full UUID but truncate to same length for consistency.
	return uuid.New().String()[:12]
}

// writeHeaderOnly writes just the session header to the session file,
// creating it. Used at session creation so observers can tail from byte 0.
// After this call, persistEntry will use the append path (ss.flushed=true).
func (ss *SessionStore) writeHeaderOnly() {
	if !ss.persist || ss.sessionFile == "" || ss.header == nil {
		return
	}
	data, err := json.Marshal(ss.header)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: marshal header: %v\n", err)
		return
	}
	// O_EXCL: never clobber an existing file. Session creation is the only
	// caller and it always picks a fresh path.
	f, err := os.OpenFile(ss.sessionFile, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		// File already exists (e.g. continue/reopen path). Not an error.
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "session: write header to %s: %v\n", ss.sessionFile, err)
		return
	}
	if _, err := f.WriteString("\n"); err != nil {
		fmt.Fprintf(os.Stderr, "session: write header newline: %v\n", err)
		return
	}
	ss.flushed = true
	ss.updateSidecar()
}

func (ss *SessionStore) rewriteFile() {
	if !ss.persist || ss.sessionFile == "" {
		return
	}
	var lines []string
	data, err := json.Marshal(ss.header)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: marshal header: %v\n", err)
		return
	}
	lines = append(lines, string(data))
	for _, e := range ss.entries {
		data, err := json.Marshal(e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "session: marshal entry %s: %v\n", e.ID, err)
			continue
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(ss.sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "session: write %s: %v\n", ss.sessionFile, err)
	}
	ss.updateSidecar()
}

// StampInvocation stores the user-intent runtime config on the session header
// so a later `fir -c` / `/resume` can re-apply it. Safe to call only at
// session creation, before any entries have been appended; calls after that
// are no-ops (preserving the original stamp). Calls on resumed sessions are
// no-ops too — Invocation is stamped exactly once per session, never
// overwritten. Rewrites the header on disk if the session is persistent.
func (ss *SessionStore) StampInvocation(inv *SessionInvocation) {
	if inv == nil || inv.IsEmpty() {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.header == nil {
		return
	}
	if ss.header.Invocation != nil {
		// Already stamped — never overwrite.
		return
	}
	if len(ss.entries) > 0 {
		// Too late: would invalidate readers that already saw the header.
		return
	}
	ss.header.Invocation = inv
	if ss.persist && ss.sessionFile != "" {
		// The header has already been written by newSession(); rewrite it
		// so the on-disk header reflects the stamped invocation.
		ss.rewriteFile()
	}
}

// WasResumed reports whether this store opened an already-existing session
// file (header loaded from disk) versus created a fresh one. False for
// in-memory stores and for stores that fell back to newSession() because
// the target path didn't exist or its header was corrupted. Used by the
// CLI startup path to decide between stamping a new SessionInvocation and
// restoring the persisted one.
func (ss *SessionStore) WasResumed() bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.resumed
}

// GetInvocation returns the persisted invocation from the loaded session
// header, or nil if none is stamped (e.g. legacy session or no flags worth
// recording).
func (ss *SessionStore) GetInvocation() *SessionInvocation {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if ss.header == nil {
		return nil
	}
	if ss.header.Invocation.IsEmpty() {
		return nil
	}
	return ss.header.Invocation
}

// ForceFlush writes the session to disk regardless of whether an assistant
// message exists. Used before /reexec to ensure metadata (e.g. session name)
// survives across process replacement.
func (ss *SessionStore) ForceFlush() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if !ss.persist || ss.sessionFile == "" || len(ss.entries) == 0 {
		return
	}
	ss.rewriteFile()
	ss.flushed = true
}

func (ss *SessionStore) persistEntry(entry *SessionEntry) {
	if !ss.persist || ss.sessionFile == "" {
		return
	}

	// Append-only fast path. The header was written at session creation
	// (writeHeaderOnly), and ss.flushed is always true on persisted
	// sessions, so we always take the append branch here in normal
	// operation. The rewriteFile fallback exists for code paths that
	// reset ss.flushed (e.g. compaction-driven rewrites).
	if !ss.flushed {
		ss.rewriteFile()
		ss.flushed = true
		return
	}
	f, err := os.OpenFile(ss.sessionFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: open %s: %v\n", ss.sessionFile, err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: marshal entry %s: %v\n", entry.ID, err)
		return
	}
	if _, err := f.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "session: write entry %s: %v\n", entry.ID, err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		fmt.Fprintf(os.Stderr, "session: write newline: %v\n", err)
	}
	ss.updateSidecar()
}

// updateSidecar rebuilds and writes the metadata sidecar for the current
// session file. Called after every disk write so listing never needs a full
// parse on warm runs. Errors are silently ignored — listing must never fail
// because of a sidecar write failure.
func (ss *SessionStore) updateSidecar() {
	if !ss.persist || ss.sessionFile == "" || ss.header == nil {
		return
	}
	stat, err := os.Stat(ss.sessionFile)
	if err != nil {
		return
	}
	var messageCount int
	var firstMessage string
	var name string
	for _, e := range ss.entries {
		if e.Type == "session_info" && e.Name != "" {
			name = e.Name
		}
		if e.Type != "message" {
			continue
		}
		messageCount++
		if firstMessage == "" && len(e.RawMessage) > 0 {
			var probe struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			}
			if json.Unmarshal(e.RawMessage, &probe) == nil && probe.Role == "user" {
				firstMessage = extractTextFromAny(probe.Content)
			}
		}
	}
	if firstMessage == "" {
		firstMessage = "(no messages)"
	}
	created, _ := time.Parse(time.RFC3339Nano, ss.header.Timestamp)
	writeSidecar(ss.sessionFile, &MetaSidecar{
		Name:              name,
		FirstMessage:      firstMessage,
		Cwd:               ss.header.Cwd,
		ID:                ss.header.ID,
		ParentSessionPath: ss.header.ParentSession,
		Created:           created,
		MessageCount:      messageCount,
		ModTime:           stat.ModTime(),
	})
}

func (ss *SessionStore) appendEntry(entry *SessionEntry) string {
	ss.entries = append(ss.entries, entry)
	ss.byID[entry.ID] = entry
	ss.leafID = entry.ID
	ss.persistEntry(entry)
	return entry.ID
}

// --- Accessors ---
// All read accessors hold RLock to prevent data races with Append* methods.

func (ss *SessionStore) GetCwd() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.cwd
}
func (ss *SessionStore) GetSessionDir() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.sessionDir
}

// Close releases the session lock. Safe to call multiple times.
func (ss *SessionStore) Close() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.lock != nil {
		ss.lock.Close()
		ss.lock = nil
	}
}

func (ss *SessionStore) GetSessionID() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.sessionID
}
func (ss *SessionStore) GetSessionFile() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.sessionFile
}

// Observables returns the per-session observable cards store. Always
// non-nil for a constructed SessionStore; backing file (if any) is
// <sessionFile>.cards. See docs/design/observable-cards.md.
func (ss *SessionStore) Observables() *ObservableStore {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.observables
}
func (ss *SessionStore) IsPersisted() bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.persist
}
func (ss *SessionStore) GetLeafID() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.leafID
}

func (ss *SessionStore) GetEntry(id string) *SessionEntry {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.byID[id]
}

func (ss *SessionStore) GetEntries() []*SessionEntry {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	result := make([]*SessionEntry, len(ss.entries))
	copy(result, ss.entries)
	return result
}

func (ss *SessionStore) GetSessionName() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	for i := len(ss.entries) - 1; i >= 0; i-- {
		e := ss.entries[i]
		if e.Type == "session_info" && e.Name != "" {
			return e.Name
		}
	}
	return ""
}

func (ss *SessionStore) GetTree() []*SessionTreeNode {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	nodeMap := make(map[string]*SessionTreeNode)
	var roots []*SessionTreeNode

	for _, e := range ss.entries {
		label := ss.labelsById[e.ID]
		nodeMap[e.ID] = &SessionTreeNode{Entry: e, Label: label}
	}

	for _, e := range ss.entries {
		node := nodeMap[e.ID]
		if e.ParentID == "" || e.ParentID == e.ID {
			roots = append(roots, node)
		} else if parent, ok := nodeMap[e.ParentID]; ok {
			parent.Children = append(parent.Children, node)
		} else {
			roots = append(roots, node) // orphan
		}
	}

	return roots
}

// --- Append methods ---

// AppendAIMessage appends an agent message. Returns entry ID.
func (ss *SessionStore) AppendAIMessage(msg ai.Message) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	rawMsg, _ := json.Marshal(msg)
	entry := &SessionEntry{
		Type:       "message",
		ID:         ss.generateID(),
		ParentID:   ss.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		RawMessage: rawMsg,
	}
	return ss.appendEntry(entry)
}

// AppendAgentMessage appends an agent message (with custom support). Returns entry ID.
func (ss *SessionStore) AppendAgentMessage(msg agent.AgentMessage) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	rawMsg, _ := json.Marshal(msg)
	entry := &SessionEntry{
		Type:       "message",
		ID:         ss.generateID(),
		ParentID:   ss.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		RawMessage: rawMsg,
	}
	return ss.appendEntry(entry)
}

func (ss *SessionStore) AppendThinkingLevelChange(thinkingLevel string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:          "thinking_level_change",
		ID:            ss.generateID(),
		ParentID:      ss.leafID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		ThinkingLevel: thinkingLevel,
	}
	return ss.appendEntry(entry)
}

func (ss *SessionStore) AppendModelChange(provider, modelID string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:      "model_change",
		ID:        ss.generateID(),
		ParentID:  ss.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Provider:  provider,
		ModelID:   modelID,
	}
	return ss.appendEntry(entry)
}

// MaybeRecordAgentVersionChange appends an "agent_version" entry when this
// store resumed an existing session whose header (or most recent agent_version
// entry) was written by a different fir binary than the one now running. This
// makes a session that spans two fir versions visible as a delta, mirroring
// how model_change records a mid-session model switch. It is a no-op for fresh
// sessions (the header already carries the current version), in-memory stores,
// and resumes where the version is unchanged. Returns the new entry ID, or ""
// if nothing was appended.
func (ss *SessionStore) MaybeRecordAgentVersionChange() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if !ss.resumed || ss.header == nil {
		return ""
	}
	current := currentFirVersion()
	if current == "" {
		return ""
	}
	// The last-recorded version is the most recent agent_version entry, or the
	// header's FirVersion if no such entry exists yet.
	last := ss.header.FirVersion
	for _, e := range ss.entries {
		if e.Type == "agent_version" && e.FirVersion != "" {
			last = e.FirVersion
		}
	}
	if last == current {
		return ""
	}

	entry := &SessionEntry{
		Type:       "agent_version",
		ID:         ss.generateID(),
		ParentID:   ss.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		FirVersion: current,
		Commit:     firCommit(),
	}
	return ss.appendEntry(entry)
}

func (ss *SessionStore) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, details json.RawMessage, fromHook bool) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:             "compaction",
		ID:               ss.generateID(),
		ParentID:         ss.leafID,
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     tokensBefore,
		Details:          details,
		FromHook:         fromHook,
	}
	return ss.appendEntry(entry)
}

func (ss *SessionStore) AppendCustomEntry(customType string, data json.RawMessage) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:       "custom",
		ID:         ss.generateID(),
		ParentID:   ss.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		CustomType: customType,
		Data:       data,
	}
	return ss.appendEntry(entry)
}

func (ss *SessionStore) AppendSessionInfo(name string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:      "session_info",
		ID:        ss.generateID(),
		ParentID:  ss.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Name:      strings.TrimSpace(name),
	}
	return ss.appendEntry(entry)
}

func (ss *SessionStore) AppendLabelChange(targetID, label string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:      "label",
		ID:        ss.generateID(),
		ParentID:  ss.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		TargetID:  targetID,
		Label:     label,
	}
	ss.appendEntry(entry)

	if label != "" {
		ss.labelsById[targetID] = label
	} else {
		delete(ss.labelsById, targetID)
	}
	return entry.ID
}

// AppendPlanUpdate records the current plan state. These entries are never
// included in the LLM context but are used to restore the plan on resume.
// An empty/nil entries slice records a cleared plan.
func (ss *SessionStore) AppendPlanUpdate(title string, entries []agent.PlanEntry, metadata map[string]string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	data, _ := json.Marshal(entries)
	entry := &SessionEntry{
		Type:         "plan_update",
		ID:           ss.generateID(),
		ParentID:     ss.leafID,
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		PlanEntries:  data,
		PlanTitle:    title,
		PlanMetadata: metadata,
	}
	return ss.appendEntry(entry)
}

// AppendCommandEntry records a user-initiated command (slash command or bash
// invocation) for audit/metering purposes. These entries are never included in
// the LLM context — see BuildSessionContextFromEntries.
//
// command is the command name without the leading slash (e.g. "model", "compact").
// args is any additional argument string relevant for metering (may be empty).
func (ss *SessionStore) AppendCommandEntry(command, args string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	entry := &SessionEntry{
		Type:      "command",
		ID:        ss.generateID(),
		ParentID:  ss.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Command:   command,
		Args:      args,
	}
	return ss.appendEntry(entry)
}

// --- Branching ---

func (ss *SessionStore) Branch(branchFromID string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.leafID = branchFromID
}

func (ss *SessionStore) BranchWithSummary(branchFromID string, summary string, details json.RawMessage, fromHook bool) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.leafID = branchFromID
	entry := &SessionEntry{
		Type:      "branch_summary",
		ID:        ss.generateID(),
		ParentID:  branchFromID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		FromID:    branchFromID,
		Summary:   summary,
		Details:   details,
		FromHook:  fromHook,
	}
	return ss.appendEntry(entry)
}

// CreateBranchedSession creates a new session file by copying the branch from root to leafId.
// Returns the new session file path (empty if in-memory) and any error.
func (ss *SessionStore) CreateBranchedSession(leafId string) (string, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	previousSessionFile := ss.sessionFile
	path := ss.getBranchUnlocked(leafId)
	if len(path) == 0 {
		return "", fmt.Errorf("entry %s not found", leafId)
	}

	// Filter out label entries - we'll recreate them
	var pathWithoutLabels []*SessionEntry
	for _, e := range path {
		if e.Type != "label" {
			pathWithoutLabels = append(pathWithoutLabels, e)
		}
	}

	newSessionID := uuid.New().String()
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	header := &SessionHeader{
		Type:       "session",
		Version:    CurrentSessionVersion,
		ID:         newSessionID,
		Timestamp:  ts,
		Cwd:        ss.cwd,
		FirVersion: currentFirVersion(),
		Commit:     firCommit(),
	}
	if ss.persist {
		header.ParentSession = previousSessionFile
	}

	// Collect labels for entries in the path
	pathEntryIDs := make(map[string]bool)
	for _, e := range pathWithoutLabels {
		pathEntryIDs[e.ID] = true
	}
	type labelPair struct {
		targetID string
		label    string
	}
	var labelsToWrite []labelPair
	for targetID, label := range ss.labelsById {
		if pathEntryIDs[targetID] {
			labelsToWrite = append(labelsToWrite, labelPair{targetID, label})
		}
	}

	// Generate label entries
	lastEntryID := ""
	if len(pathWithoutLabels) > 0 {
		lastEntryID = pathWithoutLabels[len(pathWithoutLabels)-1].ID
	}
	parentID := lastEntryID
	var labelEntries []*SessionEntry
	for _, lp := range labelsToWrite {
		id := ss.generateIDExcluding(pathEntryIDs)
		labelEntry := &SessionEntry{
			Type:      "label",
			ID:        id,
			ParentID:  parentID,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			TargetID:  lp.targetID,
			Label:     lp.label,
		}
		pathEntryIDs[id] = true
		labelEntries = append(labelEntries, labelEntry)
		parentID = id
	}

	if ss.persist && ss.sessionDir != "" {
		fileTs := strings.NewReplacer(":", "-", ".", "-").Replace(ts)
		newSessionFile := filepath.Join(ss.sessionDir, fmt.Sprintf("%s_%s.jsonl", fileTs, newSessionID))

		// Write header + entries + labels
		var lines []string
		data, _ := json.Marshal(header)
		lines = append(lines, string(data))
		for _, e := range pathWithoutLabels {
			data, _ := json.Marshal(e)
			lines = append(lines, string(data))
		}
		for _, e := range labelEntries {
			data, _ := json.Marshal(e)
			lines = append(lines, string(data))
		}
		if err := os.WriteFile(newSessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			log.Printf("session: failed to write branched session file %s: %v", newSessionFile, err)
		}

		ss.header = header
		ss.sessionID = newSessionID
		ss.sessionFile = newSessionFile
		ss.entries = append(pathWithoutLabels, labelEntries...)
		ss.flushed = true
		ss.buildIndex()
		// Branched session has its own cards file; parent cards stay
		// behind (the branch is a separate session — its producers re-Put).
		ss.observables = NewObservableStore(CardsPath(newSessionFile))
		return newSessionFile, nil
	}

	// In-memory mode
	ss.header = header
	ss.sessionID = newSessionID
	ss.entries = append(pathWithoutLabels, labelEntries...)
	ss.buildIndex()
	return "", nil
}

// getBranchUnlocked is the non-locking version of GetBranch for internal use.
func (ss *SessionStore) getBranchUnlocked(fromID string) []*SessionEntry {
	startID := fromID
	if startID == "" {
		startID = ss.leafID
	}
	var path []*SessionEntry
	current := ss.byID[startID]
	for current != nil {
		path = append([]*SessionEntry{current}, path...)
		if current.ParentID == "" {
			break
		}
		current = ss.byID[current.ParentID]
	}
	return path
}

// generateIDExcluding generates a unique ID not in the exclude set.
func (ss *SessionStore) generateIDExcluding(exclude map[string]bool) string {
	for i := 0; i < 100; i++ {
		id := uuid.New().String()[:12]
		if _, ok := ss.byID[id]; !ok && !exclude[id] {
			return id
		}
	}
	return uuid.New().String()[:12]
}

// --- Context building ---

func (ss *SessionStore) GetBranch(fromID string) []*SessionEntry {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.getBranchUnlocked(fromID)
}

func (ss *SessionStore) BuildSessionContext() SessionContext {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return BuildSessionContextFromEntries(ss.entries, ss.leafID, ss.byID)
}

// BuildSessionContextFromEntries builds session context from entries.
func BuildSessionContextFromEntries(entries []*SessionEntry, leafID string, byID map[string]*SessionEntry) SessionContext {
	if byID == nil {
		byID = make(map[string]*SessionEntry)
		for _, e := range entries {
			byID[e.ID] = e
		}
	}

	if leafID == "" && len(entries) == 0 {
		return SessionContext{ThinkingLevel: "off"}
	}

	// Find leaf
	var leaf *SessionEntry
	if leafID != "" {
		leaf = byID[leafID]
	}
	if leaf == nil && len(entries) > 0 {
		leaf = entries[len(entries)-1]
	}
	if leaf == nil {
		return SessionContext{ThinkingLevel: "off"}
	}

	// Walk from leaf to root
	var path []*SessionEntry
	current := leaf
	for current != nil {
		path = append([]*SessionEntry{current}, path...)
		if current.ParentID == "" {
			break
		}
		current = byID[current.ParentID]
	}

	// Extract settings and find compaction
	thinkingLevel := "off"
	var model *SessionModelRef
	var compaction *SessionEntry
	var lastPlanRaw json.RawMessage
	var lastPlanTitle string
	var lastPlanMetadata map[string]string

	for _, entry := range path {
		switch entry.Type {
		case "thinking_level_change":
			thinkingLevel = entry.ThinkingLevel
		case "model_change":
			model = &SessionModelRef{Provider: entry.Provider, ModelID: entry.ModelID}
		case "message":
			if len(entry.RawMessage) > 0 {
				var probe struct {
					Role     string `json:"role"`
					Provider string `json:"provider"`
					Model    string `json:"model"`
				}
				if json.Unmarshal(entry.RawMessage, &probe) == nil && probe.Role == "assistant" {
					model = &SessionModelRef{Provider: probe.Provider, ModelID: probe.Model}
				}
			}
		case "compaction":
			compaction = entry
		case "plan_update":
			lastPlanRaw = entry.PlanEntries
			lastPlanTitle = entry.PlanTitle
			lastPlanMetadata = entry.PlanMetadata
		}
	}

	// Decode the most recent plan state (nil raw → empty plan).
	var planEntries []agent.PlanEntry
	if len(lastPlanRaw) > 0 {
		_ = json.Unmarshal(lastPlanRaw, &planEntries)
	}

	// Build messages
	var messages []agent.AgentMessage

	appendMsg := func(entry *SessionEntry) {
		switch entry.Type {
		case "message":
			if len(entry.RawMessage) > 0 {
				var msg ai.Message
				if err := json.Unmarshal(entry.RawMessage, &msg); err == nil {
					messages = append(messages, agent.AgentMessage{Message: msg})
				}
			}
		case "branch_summary":
			if entry.Summary != "" {
				ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
				messages = append(messages, CreateBranchSummaryMessage(entry.Summary, entry.FromID, ts))
			}
		case "compaction":
			// Compaction summary is handled separately
		case "command":
			// Command entries are audit/metering records only — never sent to the LLM.
		case "plan_update":
			// Plan entries are metadata — never sent to the LLM.
		}
	}

	if compaction != nil {
		// Emit compaction summary first
		ts, _ := time.Parse(time.RFC3339Nano, compaction.Timestamp)
		messages = append(messages, CreateCompactionSummaryMessage(compaction.Summary, compaction.TokensBefore, ts))

		// Find compaction index in path
		compactionIdx := -1
		for i, e := range path {
			if e.Type == "compaction" && e.ID == compaction.ID {
				compactionIdx = i
				break
			}
		}

		// Emit kept messages before compaction
		foundFirstKept := false
		for i := 0; i < compactionIdx; i++ {
			if path[i].ID == compaction.FirstKeptEntryID {
				foundFirstKept = true
			}
			if foundFirstKept {
				appendMsg(path[i])
			}
		}

		// Emit messages after compaction
		for i := compactionIdx + 1; i < len(path); i++ {
			appendMsg(path[i])
		}
	} else {
		for _, entry := range path {
			appendMsg(entry)
		}
	}

	return SessionContext{
		Messages:      messages,
		ThinkingLevel: thinkingLevel,
		Model:         model,
		PlanEntries:   planEntries,
		PlanTitle:     lastPlanTitle,
		PlanMetadata:  lastPlanMetadata,
	}
}

// --- File operations ---

func loadEntriesFromFile(filePath string) (*SessionHeader, []*SessionEntry) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, nil
	}

	var header *SessionHeader
	var entries []*SessionEntry

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &probe) != nil {
			continue
		}

		if probe.Type == "session" {
			var h SessionHeader
			if json.Unmarshal([]byte(line), &h) == nil && h.ID != "" {
				header = &h
			}
			continue
		}

		var entry SessionEntry
		if json.Unmarshal([]byte(line), &entry) == nil {
			entries = append(entries, &entry)
		}
	}

	return header, entries
}

func isValidSessionFile(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}

	firstLine := string(buf[:n])
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}

	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal([]byte(firstLine), &header); err != nil {
		return false
	}
	return header.Type == "session" && header.ID != ""
}

func findMostRecentSession(sessionDir string) string {
	dirEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		return ""
	}

	type fileInfo struct {
		path  string
		mtime time.Time
	}
	var files []fileInfo

	for _, de := range dirEntries {
		if !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sessionDir, de.Name())
		if !isValidSessionFile(path) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		// Skip sessions with no messages (header-only "stillborn" sessions
		// left by cancelled new-session attempts or crashes). Selecting one
		// on --continue would resume into an empty session and bury real
		// history, so they must never win the most-recent race.
		if !sessionFileHasMessages(path, info.ModTime()) {
			continue
		}
		files = append(files, fileInfo{path: path, mtime: info.ModTime()})
	}

	if len(files) == 0 {
		return ""
	}

	best := files[0]
	for _, f := range files[1:] {
		if f.mtime.After(best.mtime) {
			best = f
		}
	}
	return best.path
}

// sessionFileHasMessages reports whether a session .jsonl contains at least
// one conversation message. It prefers the metadata sidecar when current and
// otherwise scans the file for a message entry. Used to keep empty/header-only
// sessions from winning the most-recent-session selection.
func sessionFileHasMessages(path string, mtime time.Time) bool {
	if m := readSidecar(path, mtime); m != nil {
		return m.MessageCount > 0
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if bytes.Contains(scanner.Bytes(), []byte(`"type":"message"`)) {
			return true
		}
	}
	return false
}

// ListSessions lists all sessions in a directory, sorted by modified time (most recent first).
func ListSessions(_ /* cwd */, sessionDir string) ([]SessionListInfo, error) {
	listStart := time.Now()
	dirEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Collect .jsonl filenames. ReadDir returns them in lexicographic order,
	// which equals chronological order for our TIMESTAMP_UUID.jsonl naming
	// scheme. Reverse to get most-recent-first, then cap at 200 so we bound
	// I/O even on a cold cache.
	const maxSessions = 200
	var paths []string
	for _, de := range dirEntries {
		if strings.HasSuffix(de.Name(), ".jsonl") {
			paths = append(paths, filepath.Join(sessionDir, de.Name()))
		}
	}
	// Reverse (ReadDir is ascending; we want most-recent first).
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	if len(paths) > maxSessions {
		paths = paths[:maxSessions]
	}

	firlog.Info("ListSessions: readdir done", "files", len(paths), "dir", sessionDir, "elapsed_ms", time.Since(listStart).Milliseconds())

	// Load concurrently with a bounded worker pool.
	const workers = 8
	type result struct {
		info *SessionListInfo
	}
	results := make([]result, len(paths))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			results[idx] = result{info: buildSessionListInfo(path)}
			<-sem
		}(i, p)
	}
	wg.Wait()
	firlog.Info("ListSessions: workers done", "files", len(paths), "elapsed_ms", time.Since(listStart).Milliseconds())

	var sessions []SessionListInfo
	for _, r := range results {
		if r.info != nil {
			sessions = append(sessions, *r.info)
		}
	}

	// Sort by modified time (most recent first).
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Modified.After(sessions[j].Modified)
	})

	return sessions, nil
}

func buildSessionListInfo(filePath string) *SessionListInfo {
	buildStart := time.Now()
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil
	}

	// Fast path: use the metadata sidecar if it is current.
	if m := readSidecar(filePath, stat.ModTime()); m != nil {
		elapsed := time.Since(buildStart).Milliseconds()
		if elapsed > 50 {
			firlog.Info("buildSessionListInfo: slow sidecar read", "file", filepath.Base(filePath), "elapsed_ms", elapsed)
		}
		return &SessionListInfo{
			Path:              filePath,
			ID:                m.ID,
			Cwd:               m.Cwd,
			Name:              m.Name,
			ParentSessionPath: m.ParentSessionPath,
			Created:           m.Created,
			Modified:          stat.ModTime(),
			MessageCount:      m.MessageCount,
			FirstMessage:      m.FirstMessage,
		}
	}

	// Slow path: full parse, then write sidecar for next time.
	firlog.Info("buildSessionListInfo: slow path (no sidecar)", "file", filepath.Base(filePath))
	header, entries := loadEntriesFromFile(filePath)
	if header == nil {
		return nil
	}

	var messageCount int
	var firstMessage string
	var name string

	for _, entry := range entries {
		if entry.Type == "session_info" && entry.Name != "" {
			name = entry.Name
		}
		if entry.Type != "message" {
			continue
		}
		messageCount++

		if firstMessage == "" && len(entry.RawMessage) > 0 {
			var probe struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			}
			if json.Unmarshal(entry.RawMessage, &probe) == nil && probe.Role == "user" {
				firstMessage = extractTextFromAny(probe.Content)
			}
		}
	}

	if firstMessage == "" {
		firstMessage = "(no messages)"
	}

	created, _ := time.Parse(time.RFC3339Nano, header.Timestamp)

	info := &SessionListInfo{
		Path:              filePath,
		ID:                header.ID,
		Cwd:               header.Cwd,
		Name:              name,
		ParentSessionPath: header.ParentSession,
		Created:           created,
		Modified:          stat.ModTime(),
		MessageCount:      messageCount,
		FirstMessage:      firstMessage,
	}

	writeSidecar(filePath, &MetaSidecar{
		Name:              name,
		FirstMessage:      firstMessage,
		Cwd:               header.Cwd,
		ID:                header.ID,
		ParentSessionPath: header.ParentSession,
		Created:           created,
		MessageCount:      messageCount,
		ModTime:           stat.ModTime(),
	})

	return info
}

func extractTextFromAny(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
		}
		return strings.Join(texts, " ")
	}
	return ""
}

// DefaultSessionDir computes the default session directory for a cwd.
func DefaultSessionDir(agentDir, cwd string) string {
	dir := SessionDirForCwd(agentDir, cwd)
	os.MkdirAll(dir, 0755)
	return dir
}

// SessionDirForCwd computes the session directory path for a cwd without
// creating it. Use this when you only need the path for listing/checking
// (e.g. legacy directory lookups) and don't want the mkdir side effect.
func SessionDirForCwd(agentDir, cwd string) string {
	safePath := "--" + strings.TrimLeft(cwd, "/\\")
	safePath = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(safePath) + "--"
	return filepath.Join(agentDir, "sessions", safePath)
}

// ForkFrom creates a new session from a source session file, copying all entries.
// The new session is created in the targetCwd's session directory.
func ForkFrom(sourcePath, targetCwd, sessionDir string) (*SessionStore, error) {
	header, entries := loadEntriesFromFile(sourcePath)
	if header == nil {
		return nil, fmt.Errorf("cannot fork: source session file is empty or invalid: %s", sourcePath)
	}

	if sessionDir == "" {
		return nil, fmt.Errorf("sessionDir is required for ForkFrom")
	}

	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create session directory: %w", err)
	}

	newSessionID := uuid.New().String()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	fileTs := strings.NewReplacer(":", "-", ".", "-").Replace(ts)
	newSessionFile := filepath.Join(sessionDir, fmt.Sprintf("%s_%s.jsonl", fileTs, newSessionID))

	newHeader := &SessionHeader{
		Type:          "session",
		Version:       CurrentSessionVersion,
		ID:            newSessionID,
		Timestamp:     ts,
		Cwd:           targetCwd,
		ParentSession: sourcePath,
		FirVersion:    currentFirVersion(),
		Commit:        firCommit(),
	}

	var lines []string
	data, _ := json.Marshal(newHeader)
	lines = append(lines, string(data))
	for _, e := range entries {
		data, _ := json.Marshal(e)
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(newSessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("cannot write forked session: %w", err)
	}

	ss, _ := OpenSessionStore(newSessionFile, sessionDir)
	return ss, nil
}

// SessionsDir returns the parent directory that contains all per-project session directories.
func SessionsDir(agentDir string) string {
	return filepath.Join(agentDir, "sessions")
}

// MergeSessions merges two session lists, deduplicating by path, and returns
// the result sorted by modified time (most recent first).
func MergeSessions(a, b []SessionListInfo) []SessionListInfo {
	seen := make(map[string]bool, len(a))
	merged := make([]SessionListInfo, 0, len(a)+len(b))
	for _, s := range a {
		key := s.Path
		if resolved, err := filepath.EvalSymlinks(key); err == nil {
			key = resolved
		}
		if !seen[key] {
			seen[key] = true
			merged = append(merged, s)
		}
	}
	for _, s := range b {
		key := s.Path
		if resolved, err := filepath.EvalSymlinks(key); err == nil {
			key = resolved
		}
		if !seen[key] {
			seen[key] = true
			merged = append(merged, s)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Modified.After(merged[j].Modified)
	})
	return merged
}

// ListAllSessions lists sessions across all project directories, sorted by modified time.
// Additional agent dirs (e.g. ~/.pi/agent) can be passed to merge sessions from multiple sources.
func ListAllSessions(agentDir string, extraAgentDirs ...string) ([]SessionListInfo, error) {
	dirs := append([]string{agentDir}, extraAgentDirs...)

	// Collect all subdirectories to scan.
	type subDir struct{ path string }
	var subDirs []subDir
	for _, dir := range dirs {
		sessionsDir := SessionsDir(dir)
		dirEntries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if de.IsDir() {
				subDirs = append(subDirs, subDir{filepath.Join(sessionsDir, de.Name())})
			}
		}
	}

	// Load each subdirectory concurrently.
	type subdirResult struct {
		sessions []SessionListInfo
	}
	results := make([]subdirResult, len(subDirs))
	const workers = 8
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, sd := range subDirs {
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			sessions, _ := ListSessions("", path)
			results[idx] = subdirResult{sessions: sessions}
			<-sem
		}(i, sd.path)
	}
	wg.Wait()

	seen := make(map[string]bool)
	var all []SessionListInfo
	for _, r := range results {
		for _, s := range r.sessions {
			key := s.Path
			if resolved, err := filepath.EvalSymlinks(key); err == nil {
				key = resolved
			}
			if !seen[key] {
				seen[key] = true
				all = append(all, s)
			}
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Modified.After(all[j].Modified)
	})

	return all, nil
}
