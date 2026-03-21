// Ported from: packages/coding-agent/src/core/session-manager.ts
// Upstream hash: 1caadb2e
package store

import (
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

// --- SessionManager ---

// SessionManager manages conversation sessions as append-only trees stored in JSONL files.
type SessionManager struct {
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
	leafID      string // empty = before first entry
}

// NewSessionManager creates a persisted session.
func NewSessionManager(cwd, sessionDir string) *SessionManager {
	sm := &SessionManager{
		cwd:        cwd,
		sessionDir: sessionDir,
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	if sessionDir != "" {
		os.MkdirAll(sessionDir, 0755)
	}
	sm.newSession(nil)
	return sm
}

// InMemorySessionManager creates a non-persisted session.
func InMemorySessionManager(cwd ...string) *SessionManager {
	c := ""
	if len(cwd) > 0 {
		c = cwd[0]
	}
	sm := &SessionManager{
		cwd:        c,
		persist:    false,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	sm.newSession(nil)
	return sm
}

// OpenSessionManager opens a specific session file.
func OpenSessionManager(filePath string, sessionDir ...string) *SessionManager {
	dir := ""
	if len(sessionDir) > 0 {
		dir = sessionDir[0]
	} else {
		dir = filepath.Dir(filePath)
	}

	sm := &SessionManager{
		sessionDir: dir,
		persist:    true,
		byID:       make(map[string]*SessionEntry),
		labelsById: make(map[string]string),
	}
	sm.setSessionFile(filePath)
	return sm
}

// ContinueRecentSession continues the most recent session, or creates new.
func ContinueRecentSession(cwd, sessionDir string) *SessionManager {
	if most := findMostRecentSession(sessionDir); most != "" {
		sm := &SessionManager{
			cwd:        cwd,
			sessionDir: sessionDir,
			persist:    true,
			byID:       make(map[string]*SessionEntry),
			labelsById: make(map[string]string),
		}
		sm.setSessionFile(most)
		return sm
	}
	return NewSessionManager(cwd, sessionDir)
}

// SetSessionFile switches to a different session file, loading its entries.
func (sm *SessionManager) SetSessionFile(filePath string) {
	sm.setSessionFile(filePath)
}

func (sm *SessionManager) setSessionFile(filePath string) {
	absPath, _ := filepath.Abs(filePath)
	sm.sessionFile = absPath
	firlog.Debug("loading session file", "path", absPath)

	if _, err := os.Stat(absPath); err == nil {
		header, entries := loadEntriesFromFile(absPath)

		if header == nil {
			// Corrupted - start fresh at this path
			sm.newSession(nil)
			sm.sessionFile = absPath
			sm.rewriteFile()
			sm.flushed = true
			return
		}

		sm.header = header
		sm.sessionID = header.ID
		sm.cwd = header.Cwd
		sm.entries = entries
		sm.buildIndex()
		sm.flushed = true
		firlog.Debug("session loaded", "sessionID", header.ID, "entries", len(entries))
	} else {
		sm.newSession(nil)
		sm.sessionFile = absPath
	}
}

// NewSession starts a new session. Returns the session file path (empty if in-memory).
func (sm *SessionManager) NewSession(opts *NewSessionOptions) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.newSession(opts)
}

func (sm *SessionManager) newSession(opts *NewSessionOptions) string {
	sm.sessionID = uuid.New().String()
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	sm.header = &SessionHeader{
		Type:      "session",
		Version:   CurrentSessionVersion,
		ID:        sm.sessionID,
		Timestamp: ts,
		Cwd:       sm.cwd,
	}
	if opts != nil {
		sm.header.ParentSession = opts.ParentSession
	}

	sm.entries = nil
	sm.byID = make(map[string]*SessionEntry)
	sm.labelsById = make(map[string]string)
	sm.leafID = ""
	sm.flushed = false

	if sm.persist && sm.sessionDir != "" {
		fileTs := strings.NewReplacer(":", "-", ".", "-").Replace(ts)
		sm.sessionFile = filepath.Join(sm.sessionDir, fmt.Sprintf("%s_%s.jsonl", fileTs, sm.sessionID))
	}

	firlog.Debug("new session created", "sessionID", sm.sessionID, "file", sm.sessionFile)
	return sm.sessionFile
}

func (sm *SessionManager) buildIndex() {
	sm.byID = make(map[string]*SessionEntry)
	sm.labelsById = make(map[string]string)
	sm.leafID = ""

	for _, entry := range sm.entries {
		sm.byID[entry.ID] = entry
		sm.leafID = entry.ID

		if entry.Type == "label" {
			if entry.Label != "" {
				sm.labelsById[entry.TargetID] = entry.Label
			} else {
				delete(sm.labelsById, entry.TargetID)
			}
		}
	}
}

func (sm *SessionManager) generateID() string {
	for i := 0; i < 100; i++ {
		id := uuid.New().String()[:12]
		if _, ok := sm.byID[id]; !ok {
			return id
		}
	}
	// Exhausted retries (practically impossible with 48-bit IDs).
	// Fall back to full UUID but truncate to same length for consistency.
	return uuid.New().String()[:12]
}

func (sm *SessionManager) rewriteFile() {
	if !sm.persist || sm.sessionFile == "" {
		return
	}
	var lines []string
	data, err := json.Marshal(sm.header)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session: marshal header: %v\n", err)
		return
	}
	lines = append(lines, string(data))
	for _, e := range sm.entries {
		data, err := json.Marshal(e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "session: marshal entry %s: %v\n", e.ID, err)
			continue
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(sm.sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "session: write %s: %v\n", sm.sessionFile, err)
	}
	sm.updateSidecar()
}

// ForceFlush writes the session to disk regardless of whether an assistant
// message exists. Used before /reexec to ensure metadata (e.g. session name)
// survives across process replacement.
func (sm *SessionManager) ForceFlush() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.persist || sm.sessionFile == "" || len(sm.entries) == 0 {
		return
	}
	sm.rewriteFile()
	sm.flushed = true
}

func (sm *SessionManager) persistEntry(entry *SessionEntry) {
	if !sm.persist || sm.sessionFile == "" {
		return
	}

	// Don't write until we have an assistant message
	hasAssistant := false
	for _, e := range sm.entries {
		if e.Type == "message" && len(e.RawMessage) > 0 {
			var probe struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(e.RawMessage, &probe) == nil && probe.Role == "assistant" {
				hasAssistant = true
				break
			}
		}
	}

	if !hasAssistant {
		sm.flushed = false
		return
	}

	if !sm.flushed {
		sm.rewriteFile()
		sm.flushed = true
	} else {
		f, err := os.OpenFile(sm.sessionFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "session: open %s: %v\n", sm.sessionFile, err)
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
		sm.updateSidecar()
	}
}

// updateSidecar rebuilds and writes the metadata sidecar for the current
// session file. Called after every disk write so listing never needs a full
// parse on warm runs. Errors are silently ignored — listing must never fail
// because of a sidecar write failure.
func (sm *SessionManager) updateSidecar() {
	if !sm.persist || sm.sessionFile == "" || sm.header == nil {
		return
	}
	stat, err := os.Stat(sm.sessionFile)
	if err != nil {
		return
	}
	var messageCount int
	var firstMessage string
	var name string
	for _, e := range sm.entries {
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
	created, _ := time.Parse(time.RFC3339Nano, sm.header.Timestamp)
	writeSidecar(sm.sessionFile, &MetaSidecar{
		Name:              name,
		FirstMessage:      firstMessage,
		Cwd:               sm.header.Cwd,
		ID:                sm.header.ID,
		ParentSessionPath: sm.header.ParentSession,
		Created:           created,
		MessageCount:      messageCount,
		ModTime:           stat.ModTime(),
	})
}

func (sm *SessionManager) appendEntry(entry *SessionEntry) string {
	sm.entries = append(sm.entries, entry)
	sm.byID[entry.ID] = entry
	sm.leafID = entry.ID
	sm.persistEntry(entry)
	return entry.ID
}

// --- Accessors ---
// All read accessors hold RLock to prevent data races with Append* methods.

func (sm *SessionManager) GetCwd() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.cwd
}
func (sm *SessionManager) GetSessionDir() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessionDir
}
func (sm *SessionManager) GetSessionID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessionID
}
func (sm *SessionManager) GetSessionFile() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessionFile
}
func (sm *SessionManager) IsPersisted() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.persist
}
func (sm *SessionManager) GetLeafID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.leafID
}

func (sm *SessionManager) GetEntry(id string) *SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.byID[id]
}

func (sm *SessionManager) GetEntries() []*SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]*SessionEntry, len(sm.entries))
	copy(result, sm.entries)
	return result
}

func (sm *SessionManager) GetSessionName() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for i := len(sm.entries) - 1; i >= 0; i-- {
		e := sm.entries[i]
		if e.Type == "session_info" && e.Name != "" {
			return e.Name
		}
	}
	return ""
}

func (sm *SessionManager) GetTree() []*SessionTreeNode {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	nodeMap := make(map[string]*SessionTreeNode)
	var roots []*SessionTreeNode

	for _, e := range sm.entries {
		label := sm.labelsById[e.ID]
		nodeMap[e.ID] = &SessionTreeNode{Entry: e, Label: label}
	}

	for _, e := range sm.entries {
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
func (sm *SessionManager) AppendAIMessage(msg ai.Message) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	rawMsg, _ := json.Marshal(msg)
	entry := &SessionEntry{
		Type:       "message",
		ID:         sm.generateID(),
		ParentID:   sm.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		RawMessage: rawMsg,
	}
	return sm.appendEntry(entry)
}

// AppendAgentMessage appends an agent message (with custom support). Returns entry ID.
func (sm *SessionManager) AppendAgentMessage(msg agent.AgentMessage) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	rawMsg, _ := json.Marshal(msg)
	entry := &SessionEntry{
		Type:       "message",
		ID:         sm.generateID(),
		ParentID:   sm.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		RawMessage: rawMsg,
	}
	return sm.appendEntry(entry)
}

func (sm *SessionManager) AppendThinkingLevelChange(thinkingLevel string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:          "thinking_level_change",
		ID:            sm.generateID(),
		ParentID:      sm.leafID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		ThinkingLevel: thinkingLevel,
	}
	return sm.appendEntry(entry)
}

func (sm *SessionManager) AppendModelChange(provider, modelID string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:      "model_change",
		ID:        sm.generateID(),
		ParentID:  sm.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Provider:  provider,
		ModelID:   modelID,
	}
	return sm.appendEntry(entry)
}

func (sm *SessionManager) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int, details json.RawMessage, fromHook bool) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:             "compaction",
		ID:               sm.generateID(),
		ParentID:         sm.leafID,
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     tokensBefore,
		Details:          details,
		FromHook:         fromHook,
	}
	return sm.appendEntry(entry)
}

func (sm *SessionManager) AppendCustomEntry(customType string, data json.RawMessage) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:       "custom",
		ID:         sm.generateID(),
		ParentID:   sm.leafID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		CustomType: customType,
		Data:       data,
	}
	return sm.appendEntry(entry)
}

func (sm *SessionManager) AppendSessionInfo(name string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:      "session_info",
		ID:        sm.generateID(),
		ParentID:  sm.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Name:      strings.TrimSpace(name),
	}
	return sm.appendEntry(entry)
}

func (sm *SessionManager) AppendLabelChange(targetID, label string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:      "label",
		ID:        sm.generateID(),
		ParentID:  sm.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		TargetID:  targetID,
		Label:     label,
	}
	sm.appendEntry(entry)

	if label != "" {
		sm.labelsById[targetID] = label
	} else {
		delete(sm.labelsById, targetID)
	}
	return entry.ID
}

// AppendPlanUpdate records the current plan state. These entries are never
// included in the LLM context but are used to restore the plan on resume.
// An empty/nil entries slice records a cleared plan.
func (sm *SessionManager) AppendPlanUpdate(title string, entries []agent.PlanEntry, metadata map[string]string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, _ := json.Marshal(entries)
	entry := &SessionEntry{
		Type:         "plan_update",
		ID:           sm.generateID(),
		ParentID:     sm.leafID,
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		PlanEntries:  data,
		PlanTitle:    title,
		PlanMetadata: metadata,
	}
	return sm.appendEntry(entry)
}

// AppendCommandEntry records a user-initiated command (slash command or bash
// invocation) for audit/metering purposes. These entries are never included in
// the LLM context — see BuildSessionContextFromEntries.
//
// command is the command name without the leading slash (e.g. "model", "compact").
// args is any additional argument string relevant for metering (may be empty).
func (sm *SessionManager) AppendCommandEntry(command, args string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry := &SessionEntry{
		Type:      "command",
		ID:        sm.generateID(),
		ParentID:  sm.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Command:   command,
		Args:      args,
	}
	return sm.appendEntry(entry)
}

// --- Branching ---

func (sm *SessionManager) Branch(branchFromID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.leafID = branchFromID
}

func (sm *SessionManager) BranchWithSummary(branchFromID string, summary string, details json.RawMessage, fromHook bool) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.leafID = branchFromID
	entry := &SessionEntry{
		Type:      "branch_summary",
		ID:        sm.generateID(),
		ParentID:  branchFromID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		FromID:    branchFromID,
		Summary:   summary,
		Details:   details,
		FromHook:  fromHook,
	}
	return sm.appendEntry(entry)
}

// CreateBranchedSession creates a new session file by copying the branch from root to leafId.
// Returns the new session file path (empty if in-memory) and any error.
func (sm *SessionManager) CreateBranchedSession(leafId string) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	previousSessionFile := sm.sessionFile
	path := sm.getBranchUnlocked(leafId)
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
		Type:      "session",
		Version:   CurrentSessionVersion,
		ID:        newSessionID,
		Timestamp: ts,
		Cwd:       sm.cwd,
	}
	if sm.persist {
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
	for targetID, label := range sm.labelsById {
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
		id := sm.generateIDExcluding(pathEntryIDs)
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

	if sm.persist && sm.sessionDir != "" {
		fileTs := strings.NewReplacer(":", "-", ".", "-").Replace(ts)
		newSessionFile := filepath.Join(sm.sessionDir, fmt.Sprintf("%s_%s.jsonl", fileTs, newSessionID))

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

		sm.header = header
		sm.sessionID = newSessionID
		sm.sessionFile = newSessionFile
		sm.entries = append(pathWithoutLabels, labelEntries...)
		sm.flushed = true
		sm.buildIndex()
		return newSessionFile, nil
	}

	// In-memory mode
	sm.header = header
	sm.sessionID = newSessionID
	sm.entries = append(pathWithoutLabels, labelEntries...)
	sm.buildIndex()
	return "", nil
}

// getBranchUnlocked is the non-locking version of GetBranch for internal use.
func (sm *SessionManager) getBranchUnlocked(fromID string) []*SessionEntry {
	startID := fromID
	if startID == "" {
		startID = sm.leafID
	}
	var path []*SessionEntry
	current := sm.byID[startID]
	for current != nil {
		path = append([]*SessionEntry{current}, path...)
		if current.ParentID == "" {
			break
		}
		current = sm.byID[current.ParentID]
	}
	return path
}

// generateIDExcluding generates a unique ID not in the exclude set.
func (sm *SessionManager) generateIDExcluding(exclude map[string]bool) string {
	for i := 0; i < 100; i++ {
		id := uuid.New().String()[:12]
		if _, ok := sm.byID[id]; !ok && !exclude[id] {
			return id
		}
	}
	return uuid.New().String()[:12]
}

// --- Context building ---

func (sm *SessionManager) GetBranch(fromID string) []*SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.getBranchUnlocked(fromID)
}

func (sm *SessionManager) BuildSessionContext() SessionContext {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return BuildSessionContextFromEntries(sm.entries, sm.leafID, sm.byID)
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
	safePath := "--" + strings.TrimLeft(cwd, "/\\")
	safePath = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(safePath) + "--"
	dir := filepath.Join(agentDir, "sessions", safePath)
	os.MkdirAll(dir, 0755)
	return dir
}

// ForkFrom creates a new session from a source session file, copying all entries.
// The new session is created in the targetCwd's session directory.
func ForkFrom(sourcePath, targetCwd, sessionDir string) (*SessionManager, error) {
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

	return OpenSessionManager(newSessionFile, sessionDir), nil
}

// SessionsDir returns the parent directory that contains all per-project session directories.
func SessionsDir(agentDir string) string {
	return filepath.Join(agentDir, "sessions")
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
