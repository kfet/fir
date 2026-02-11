// Ported from: packages/coding-agent/src/core/session-manager.ts
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
)

// CurrentSessionVersion is the latest session file format version.
const CurrentSessionVersion = 3

// --- Session header ---

// SessionHeader is the first entry in a session JSONL file.
type SessionHeader struct {
	Type          string `json:"type"`              // always "session"
	Version       int    `json:"version"`           // CurrentSessionVersion
	ID            string `json:"id"`                //
	Timestamp     string `json:"timestamp"`         //
	Cwd           string `json:"cwd"`               //
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
	AllMessagesText   string
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
	}
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

func (sm *SessionManager) GetLeafEntry() *SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.leafID == "" {
		return nil
	}
	return sm.byID[sm.leafID]
}

func (sm *SessionManager) GetEntry(id string) *SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.byID[id]
}

func (sm *SessionManager) GetLabel(id string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.labelsById[id]
}

func (sm *SessionManager) GetHeader() *SessionHeader {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.header
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

func (sm *SessionManager) GetChildren(parentID string) []*SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var children []*SessionEntry
	for _, e := range sm.entries {
		if e.ParentID == parentID {
			children = append(children, e)
		}
	}
	return children
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

// --- Branching ---

func (sm *SessionManager) Branch(branchFromID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.leafID = branchFromID
}

func (sm *SessionManager) ResetLeaf() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.leafID = ""
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

// --- Context building ---

func (sm *SessionManager) GetBranch(fromID string) []*SessionEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
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
		}
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
func ListSessions(cwd, sessionDir string) ([]SessionListInfo, error) {
	dirEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []SessionListInfo
	for _, de := range dirEntries {
		if !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(sessionDir, de.Name())
		info := buildSessionListInfo(path)
		if info != nil {
			sessions = append(sessions, *info)
		}
	}

	// Sort by modified time (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Modified.After(sessions[j].Modified)
	})

	return sessions, nil
}

func buildSessionListInfo(filePath string) *SessionListInfo {
	header, entries := loadEntriesFromFile(filePath)
	if header == nil {
		return nil
	}

	stat, err := os.Stat(filePath)
	if err != nil {
		return nil
	}

	var messageCount int
	var firstMessage string
	var allMessages []string
	var name string

	for _, entry := range entries {
		if entry.Type == "session_info" && entry.Name != "" {
			name = entry.Name
		}
		if entry.Type != "message" {
			continue
		}
		messageCount++

		if len(entry.RawMessage) > 0 {
			var probe struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			}
			if json.Unmarshal(entry.RawMessage, &probe) == nil {
				text := extractTextFromAny(probe.Content)
				if text != "" {
					allMessages = append(allMessages, text)
					if firstMessage == "" && probe.Role == "user" {
						firstMessage = text
					}
				}
			}
		}
	}

	if firstMessage == "" {
		firstMessage = "(no messages)"
	}

	created, _ := time.Parse(time.RFC3339Nano, header.Timestamp)

	return &SessionListInfo{
		Path:              filePath,
		ID:                header.ID,
		Cwd:               header.Cwd,
		Name:              name,
		ParentSessionPath: header.ParentSession,
		Created:           created,
		Modified:          stat.ModTime(),
		MessageCount:      messageCount,
		FirstMessage:      firstMessage,
		AllMessagesText:   strings.Join(allMessages, " "),
	}
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
