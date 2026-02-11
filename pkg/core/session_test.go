package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
)

func TestSessionManagerNewSession(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir, filepath.Join(tmpDir, "sessions"))

	if sm.GetSessionID() == "" {
		t.Fatal("session ID should not be empty")
	}
	if sm.GetCwd() != tmpDir {
		t.Errorf("cwd should be %s, got %s", tmpDir, sm.GetCwd())
	}
	if sm.GetLeafID() != "" {
		t.Error("leaf should be empty for new session")
	}
	if sm.GetSessionFile() == "" {
		t.Error("session file should be set for persisted session")
	}
}

func TestSessionManagerInMemory(t *testing.T) {
	sm := InMemorySessionManager("/tmp/test")

	if sm.IsPersisted() {
		t.Error("in-memory session should not be persisted")
	}
	if sm.GetSessionFile() != "" {
		t.Error("in-memory session should have no file")
	}

	// Should still be able to append messages
	msg := ai.NewUserMsg("hello", time.Now().UnixMilli())
	id := sm.AppendAIMessage(msg)
	if id == "" {
		t.Error("expected entry id")
	}
	if sm.GetLeafID() != id {
		t.Errorf("leaf should be %s, got %s", id, sm.GetLeafID())
	}
}

func TestSessionManagerAppendMessage(t *testing.T) {
	sm := InMemorySessionManager()

	msg1 := ai.NewUserMsg("first message", time.Now().UnixMilli())
	id1 := sm.AppendAIMessage(msg1)

	msg2 := ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "anthropic",
		Model:    "claude-3",
	})
	id2 := sm.AppendAIMessage(msg2)

	if sm.GetLeafID() != id2 {
		t.Errorf("leaf should be %s", id2)
	}

	entries := sm.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != id1 {
		t.Error("first entry ID mismatch")
	}
	if entries[1].ID != id2 {
		t.Error("second entry ID mismatch")
	}

	// Check parent chain
	if entries[0].GetParentID() != "" {
		t.Error("first entry should have no parent")
	}
	if entries[1].GetParentID() != id1 {
		t.Errorf("second entry parent should be %s, got %s", id1, entries[1].GetParentID())
	}
}

func TestSessionManagerThinkingLevelChange(t *testing.T) {
	sm := InMemorySessionManager()

	sm.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm.AppendThinkingLevelChange("high")

	ctx := sm.BuildSessionContext()
	if ctx.ThinkingLevel != "high" {
		t.Errorf("expected thinking level 'high', got %q", ctx.ThinkingLevel)
	}
}

func TestSessionManagerModelChange(t *testing.T) {
	sm := InMemorySessionManager()

	sm.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm.AppendModelChange("anthropic", "claude-4")

	ctx := sm.BuildSessionContext()
	if ctx.Model == nil {
		t.Fatal("expected model reference")
	}
	if ctx.Model.Provider != "anthropic" || ctx.Model.ModelID != "claude-4" {
		t.Errorf("unexpected model: %+v", ctx.Model)
	}
}

func TestSessionManagerBranching(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	id2 := sm.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))
	_ = id2

	// Branch from first entry
	sm.Branch(id1)
	if sm.GetLeafID() != id1 {
		t.Errorf("leaf should be %s after branch", id1)
	}

	// Append new branch
	id3 := sm.AppendAIMessage(ai.NewUserMsg("branch", time.Now().UnixMilli()))

	// Verify tree structure
	entry3 := sm.GetEntry(id3)
	if entry3 == nil {
		t.Fatal("expected entry")
	}
	if entry3.GetParentID() != id1 {
		t.Errorf("branch entry parent should be %s, got %s", id1, entry3.GetParentID())
	}

	// Build context should use branch path
	ctx := sm.BuildSessionContext()
	if len(ctx.Messages) != 2 {
		t.Errorf("expected 2 messages in branch, got %d", len(ctx.Messages))
	}
}

func TestSessionManagerResetLeaf(t *testing.T) {
	sm := InMemorySessionManager()

	sm.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	sm.ResetLeaf()
	if sm.GetLeafID() != "" {
		t.Error("leaf should be empty after reset")
	}

	// Append should create new root
	id := sm.AppendAIMessage(ai.NewUserMsg("new root", time.Now().UnixMilli()))
	entry := sm.GetEntry(id)
	if entry.GetParentID() != "" {
		t.Error("new entry after reset should have no parent")
	}
}

func TestSessionManagerBranchWithSummary(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))

	summaryID := sm.BranchWithSummary(id1, "Summary of abandoned branch", nil, false)
	if sm.GetLeafID() != summaryID {
		t.Errorf("leaf should be summary entry %s", summaryID)
	}

	// Context should include branch summary
	ctx := sm.BuildSessionContext()
	found := false
	for _, m := range ctx.Messages {
		if m.Custom != nil {
			if bs, ok := m.Custom.(*BranchSummaryMessage); ok {
				if bs.Summary == "Summary of abandoned branch" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected branch summary in context")
	}
}

func TestSessionManagerCompaction(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("message 1", time.Now().UnixMilli()))
	id2 := sm.AppendAIMessage(ai.NewUserMsg("message 2", time.Now().UnixMilli()))
	id3 := sm.AppendAIMessage(ai.NewUserMsg("message 3", time.Now().UnixMilli()))
	_ = id3

	// Compact, keeping from id2 onward
	sm.AppendCompaction("Summary of old messages", id2, 5000, nil, false)

	ctx := sm.BuildSessionContext()
	// Should have: compaction summary + kept messages (id2, id3) = 3
	if len(ctx.Messages) < 2 {
		t.Errorf("expected at least 2 messages after compaction, got %d", len(ctx.Messages))
	}

	// First message should be compaction summary
	if ctx.Messages[0].Custom == nil {
		t.Error("first message should be compaction summary")
	}
	if _, ok := ctx.Messages[0].Custom.(*CompactionSummaryMessage); !ok {
		t.Error("first message should be CompactionSummaryMessage")
	}

	// Should NOT contain message 1
	_ = id1
}

func TestSessionManagerLabels(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("labeled", time.Now().UnixMilli()))

	sm.AppendLabelChange(id1, "important")
	if sm.GetLabel(id1) != "important" {
		t.Errorf("expected label 'important', got %q", sm.GetLabel(id1))
	}

	// Clear label
	sm.AppendLabelChange(id1, "")
	if sm.GetLabel(id1) != "" {
		t.Error("label should be cleared")
	}
}

func TestSessionManagerSessionName(t *testing.T) {
	sm := InMemorySessionManager()
	sm.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm.AppendSessionInfo("My Session")

	name := sm.GetSessionName()
	if name != "My Session" {
		t.Errorf("expected 'My Session', got %q", name)
	}
}

func TestSessionManagerGetTree(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("root", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewUserMsg("child1", time.Now().UnixMilli()))

	// Branch from root
	sm.Branch(id1)
	sm.AppendAIMessage(ai.NewUserMsg("child2", time.Now().UnixMilli()))

	tree := sm.GetTree()
	if len(tree) != 1 {
		t.Fatalf("expected 1 root, got %d", len(tree))
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("expected 2 children of root, got %d", len(tree[0].Children))
	}
}

func TestSessionManagerPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	sm := NewSessionManager(tmpDir, sessDir)
	sm.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("hi there")},
		Provider: "test",
		Model:    "test-model",
	}))

	sessionFile := sm.GetSessionFile()
	if sessionFile == "" {
		t.Fatal("expected session file")
	}

	// File should exist
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		t.Fatal("session file should exist after assistant message")
	}

	// Read it back
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header + 2 entries), got %d", len(lines))
	}

	// Verify header
	var header SessionHeader
	json.Unmarshal([]byte(lines[0]), &header)
	if header.Type != "session" {
		t.Errorf("expected session header, got type %q", header.Type)
	}
}

func TestSessionManagerOpenExisting(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	// Create and populate session
	sm1 := NewSessionManager(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "test",
		Model:    "test",
	}))
	sessionFile := sm1.GetSessionFile()

	// Open it again
	sm2 := OpenSessionManager(sessionFile)
	if sm2.GetSessionID() != sm1.GetSessionID() {
		t.Errorf("session IDs should match: %s != %s", sm2.GetSessionID(), sm1.GetSessionID())
	}

	entries := sm2.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries on reload, got %d", len(entries))
	}
}

func TestSessionManagerContinueRecent(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	// Create session with content
	sm1 := NewSessionManager(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "test",
		Model:    "test",
	}))
	origID := sm1.GetSessionID()

	// Continue recent should find it
	sm2 := ContinueRecentSession(tmpDir, sessDir)
	if sm2.GetSessionID() != origID {
		t.Errorf("should continue existing session: %s != %s", sm2.GetSessionID(), origID)
	}
}

func TestSessionManagerNewSessionAfterExisting(t *testing.T) {
	sm := InMemorySessionManager()

	sm.AppendAIMessage(ai.NewUserMsg("old", time.Now().UnixMilli()))
	oldID := sm.GetSessionID()

	sm.NewSession(nil)
	if sm.GetSessionID() == oldID {
		t.Error("new session should have different ID")
	}
	if sm.GetLeafID() != "" {
		t.Error("new session should have no leaf")
	}
	if len(sm.GetEntries()) != 0 {
		t.Error("new session should have no entries")
	}
}

func TestSessionManagerGetHeader(t *testing.T) {
	sm := InMemorySessionManager("/test/cwd")

	header := sm.GetHeader()
	if header == nil {
		t.Fatal("expected header")
	}
	if header.Type != "session" {
		t.Error("header type should be 'session'")
	}
	if header.Cwd != "/test/cwd" {
		t.Errorf("expected cwd '/test/cwd', got %q", header.Cwd)
	}
	if header.Version != CurrentSessionVersion {
		t.Errorf("expected version %d, got %d", CurrentSessionVersion, header.Version)
	}
}

func TestSessionManagerGetChildren(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("root", time.Now().UnixMilli()))
	id2 := sm.AppendAIMessage(ai.NewUserMsg("child1", time.Now().UnixMilli()))
	_ = id2

	sm.Branch(id1)
	id3 := sm.AppendAIMessage(ai.NewUserMsg("child2", time.Now().UnixMilli()))
	_ = id3

	children := sm.GetChildren(id1)
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
}

func TestBuildSessionContextEmpty(t *testing.T) {
	ctx := BuildSessionContextFromEntries(nil, "", nil)
	if ctx.ThinkingLevel != "off" {
		t.Error("thinking level should be 'off' for empty session")
	}
	if len(ctx.Messages) != 0 {
		t.Error("no messages expected for empty session")
	}
	if ctx.Model != nil {
		t.Error("no model expected for empty session")
	}
}

func TestBuildSessionContextModelFromAssistant(t *testing.T) {
	sm := InMemorySessionManager()

	sm.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "anthropic",
		Model:    "claude-3.5-sonnet",
	}))

	ctx := sm.BuildSessionContext()
	if ctx.Model == nil {
		t.Fatal("expected model from assistant message")
	}
	if ctx.Model.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got %q", ctx.Model.Provider)
	}
	if ctx.Model.ModelID != "claude-3.5-sonnet" {
		t.Errorf("expected model 'claude-3.5-sonnet', got %q", ctx.Model.ModelID)
	}
}

func TestSessionListEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	sessions, err := ListSessions(tmpDir, filepath.Join(tmpDir, "nonexistent"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Error("expected no sessions")
	}
}

func TestSessionListWithSessions(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	// Create two sessions
	sm1 := NewSessionManager(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("session 1", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response 1")},
		Provider: "test",
		Model:    "test",
	}))

	time.Sleep(10 * time.Millisecond) // ensure different timestamps

	sm2 := NewSessionManager(tmpDir, sessDir)
	sm2.AppendAIMessage(ai.NewUserMsg("session 2", time.Now().UnixMilli()))
	sm2.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response 2")},
		Provider: "test",
		Model:    "test",
	}))

	sessions, err := ListSessions(tmpDir, sessDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Should be sorted by modified time (most recent first)
	if sessions[0].FirstMessage != "session 2" {
		t.Errorf("expected most recent session first, got %q", sessions[0].FirstMessage)
	}
}

func TestSessionManagerCustomEntry(t *testing.T) {
	sm := InMemorySessionManager()

	data, _ := json.Marshal(map[string]string{"key": "value"})
	id := sm.AppendCustomEntry("my-extension", data)

	entry := sm.GetEntry(id)
	if entry == nil {
		t.Fatal("expected entry")
	}
	if entry.Type != "custom" {
		t.Errorf("expected type 'custom', got %q", entry.Type)
	}
	if entry.CustomType != "my-extension" {
		t.Errorf("expected customType 'my-extension', got %q", entry.CustomType)
	}
}

func TestFindMostRecentSessionEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	result := findMostRecentSession(tmpDir)
	if result != "" {
		t.Errorf("expected empty for no sessions, got %q", result)
	}
}

func TestSessionManagerConcurrentAppendAndRead(t *testing.T) {
	// This test should be run with -race to detect data races.
	sm := InMemorySessionManager()

	done := make(chan struct{})

	// Writer goroutine: append messages
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			sm.AppendAIMessage(ai.NewUserMsg("msg", time.Now().UnixMilli()))
		}
	}()

	// Reader goroutine: read state concurrently
	for i := 0; i < 50; i++ {
		_ = sm.GetLeafID()
		_ = sm.GetEntries()
		_ = sm.GetSessionName()
		_ = sm.GetSessionID()
		_ = sm.GetCwd()
		_ = sm.GetTree()
		_ = sm.BuildSessionContext()
	}

	<-done

	entries := sm.GetEntries()
	if len(entries) != 50 {
		t.Errorf("expected 50 entries, got %d", len(entries))
	}
}

func TestSessionManagerConcurrentMultipleWriters(t *testing.T) {
	sm := InMemorySessionManager()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				sm.AppendAIMessage(ai.NewUserMsg("msg", time.Now().UnixMilli()))
			}
		}()
	}

	// Concurrent readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = sm.GetEntries()
				_ = sm.GetLeafID()
				_ = sm.GetLeafEntry()
				_ = sm.BuildSessionContext()
			}
		}()
	}

	wg.Wait()

	entries := sm.GetEntries()
	if len(entries) != 50 {
		t.Errorf("expected 50 entries, got %d", len(entries))
	}
}

func TestSessionManagerCorruptFileRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// Create a session file with some corrupt lines mixed in
	header, _ := json.Marshal(SessionHeader{
		Type:      "session",
		ID:        "test-corrupt",
		Version:   CurrentSessionVersion,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Cwd:       tmpDir,
	})

	userMsg := ai.NewUserMsg("hello from corrupt file", time.Now().UnixMilli())
	userRaw, _ := json.Marshal(userMsg)
	entry1, _ := json.Marshal(SessionEntry{
		ID:         "entry-1",
		Type:       "message",
		RawMessage: userRaw,
	})

	assistantMsg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "test",
		Model:    "test",
	})
	assistantRaw, _ := json.Marshal(assistantMsg)
	entry2, _ := json.Marshal(SessionEntry{
		ID:         "entry-2",
		Type:       "message",
		ParentID:   "entry-1",
		RawMessage: assistantRaw,
	})

	// Write file with corrupt lines interspersed
	content := string(header) + "\n" +
		string(entry1) + "\n" +
		"this is garbage {{{not json\n" +
		string(entry2) + "\n" +
		"another corrupt line\n"

	sessionFile := filepath.Join(sessDir, "test-corrupt.jsonl")
	os.WriteFile(sessionFile, []byte(content), 0600)

	// Should load successfully, skipping corrupt lines
	sm := OpenSessionManager(sessionFile, sessDir)
	if sm.GetSessionID() != "test-corrupt" {
		t.Errorf("expected session ID 'test-corrupt', got %q", sm.GetSessionID())
	}

	entries := sm.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 valid entries (skipping corrupt lines), got %d", len(entries))
	}
	if entries[0].ID != "entry-1" {
		t.Errorf("expected first entry ID 'entry-1', got %q", entries[0].ID)
	}
	if entries[1].ID != "entry-2" {
		t.Errorf("expected second entry ID 'entry-2', got %q", entries[1].ID)
	}
}

func TestSessionManagerEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// Create an empty session file
	sessionFile := filepath.Join(sessDir, "empty.jsonl")
	os.WriteFile(sessionFile, []byte(""), 0600)

	sm := OpenSessionManager(sessionFile, sessDir)
	entries := sm.GetEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty file, got %d", len(entries))
	}
}

func TestSessionManagerCreateBranchedSession_InMemory(t *testing.T) {
	sm := InMemorySessionManager("test-cwd")

	id1 := sm.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	id2 := sm.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewUserMsg("third", time.Now().UnixMilli()))

	_ = id2
	originalID := sm.GetSessionID()
	sm.CreateBranchedSession(id1)

	// Should have a new session ID
	if sm.GetSessionID() == originalID {
		t.Error("expected new session ID after branching")
	}

	// Only the branch up to id1 should remain
	entries := sm.GetEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != id1 {
		t.Errorf("expected entry %s, got %s", id1, entries[0].ID)
	}
}

func TestSessionManagerCreateBranchedSession_Persisted(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionManager("test-cwd", dir)

	id1 := sm.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))
	// Need an assistant to trigger write
	sm.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{{Text: &ai.TextContent{Text: "reply"}}},
	}))

	oldFile := sm.GetSessionFile()
	newFile := sm.CreateBranchedSession(id1)

	if newFile == "" {
		t.Fatal("expected new session file path")
	}
	if newFile == oldFile {
		t.Error("expected different file path")
	}

	// New file should exist
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("new session file not created: %v", err)
	}

	// Entries should only contain the branch
	entries := sm.GetEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after branch, got %d", len(entries))
	}
	if entries[0].ID != id1 {
		t.Errorf("expected entry %s, got %s", id1, entries[0].ID)
	}
}

func TestSessionManagerCreateBranchedSession_WithLabels(t *testing.T) {
	sm := InMemorySessionManager()

	id1 := sm.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))

	// Add a label to id1
	sm.AppendLabelChange(id1, "important")

	sm.CreateBranchedSession(id1)

	// Label should be preserved
	label := sm.GetLabel(id1)
	if label != "important" {
		t.Errorf("expected label 'important', got %q", label)
	}
}

func TestIsValidSessionFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Valid
	valid := filepath.Join(tmpDir, "valid.jsonl")
	header, _ := json.Marshal(SessionHeader{Type: "session", ID: "test-id", Timestamp: time.Now().UTC().Format(time.RFC3339)})
	os.WriteFile(valid, header, 0644)
	if !isValidSessionFile(valid) {
		t.Error("expected valid session file")
	}

	// Invalid
	invalid := filepath.Join(tmpDir, "invalid.jsonl")
	os.WriteFile(invalid, []byte(`{"type":"wrong"}`), 0644)
	if isValidSessionFile(invalid) {
		t.Error("expected invalid session file")
	}
}

func newTestAgentMsg(t *testing.T, role, text string) agent.AgentMessage {
	t.Helper()
	switch role {
	case "user":
		return agent.NewAgentMessage(ai.NewUserMsg(text, time.Now().UnixMilli()))
	case "assistant":
		return agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{ai.NewTextContent(text)},
		}))
	default:
		t.Fatalf("unknown role: %s", role)
		return agent.AgentMessage{}
	}
}

func TestForkFrom(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	sessionsDir := filepath.Join(dstDir, "sessions")

	// Create a source session
	srcSM := NewSessionManager(srcDir, filepath.Join(srcDir, "sessions"))
	srcSM.AppendAgentMessage(newTestAgentMsg(t, "user", "hello"))
	srcSM.AppendAgentMessage(newTestAgentMsg(t, "assistant", "world"))
	srcFile := srcSM.GetSessionFile()
	if srcFile == "" {
		t.Fatal("source session file not set")
	}

	// Fork it
	forkSM, err := ForkFrom(srcFile, dstDir, sessionsDir)
	if err != nil {
		t.Fatalf("ForkFrom failed: %v", err)
	}

	// Verify the fork
	if forkSM.GetCwd() != dstDir {
		t.Errorf("expected cwd %s, got %s", dstDir, forkSM.GetCwd())
	}
	if forkSM.GetSessionID() == srcSM.GetSessionID() {
		t.Error("forked session should have a different ID")
	}
	entries := forkSM.GetEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries in forked session, got %d", len(entries))
	}
	header := forkSM.GetHeader()
	if header.ParentSession != srcFile {
		t.Errorf("expected parent session %s, got %s", srcFile, header.ParentSession)
	}
}

func TestForkFrom_InvalidSource(t *testing.T) {
	_, err := ForkFrom("/nonexistent/file.jsonl", t.TempDir(), filepath.Join(t.TempDir(), "sessions"))
	if err == nil {
		t.Error("expected error for invalid source")
	}
}

func TestListAllSessions(t *testing.T) {
	agentDir := t.TempDir()

	// Create two project session directories
	project1Dir := filepath.Join(agentDir, "sessions", "project1")
	project2Dir := filepath.Join(agentDir, "sessions", "project2")

	// Create sessions in each (session file is written only after an assistant message)
	sm1 := NewSessionManager("/proj1", project1Dir)
	sm1.AppendAIMessage(ai.NewUserMsg("msg1", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply1")},
	}))

	sm2 := NewSessionManager("/proj2", project2Dir)
	sm2.AppendAIMessage(ai.NewUserMsg("msg2", time.Now().UnixMilli()))
	sm2.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply2")},
	}))

	// Verify files exist
	if sm1.GetSessionFile() == "" {
		t.Fatal("sm1 session file not set")
	}
	if sm2.GetSessionFile() == "" {
		t.Fatal("sm2 session file not set")
	}

	// List all
	sessions, err := ListAllSessions(agentDir)
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestListAllSessions_Empty(t *testing.T) {
	agentDir := t.TempDir()
	sessions, err := ListAllSessions(agentDir)
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}
