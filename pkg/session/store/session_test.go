package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

func TestSessionStoreNewSession(t *testing.T) {
	tmpDir := t.TempDir()
	ss := NewSessionStore(tmpDir, filepath.Join(tmpDir, "sessions"))

	if ss.GetSessionID() == "" {
		t.Fatal("session ID should not be empty")
	}
	if ss.GetCwd() != tmpDir {
		t.Errorf("cwd should be %s, got %s", tmpDir, ss.GetCwd())
	}
	if ss.GetLeafID() != "" {
		t.Error("leaf should be empty for new session")
	}
	if ss.GetSessionFile() == "" {
		t.Error("session file should be set for persisted session")
	}
}

func TestSessionStoreInMemory(t *testing.T) {
	ss := InMemorySessionStore("/tmp/test")

	if ss.IsPersisted() {
		t.Error("in-memory session should not be persisted")
	}
	if ss.GetSessionFile() != "" {
		t.Error("in-memory session should have no file")
	}

	// Should still be able to append messages
	msg := ai.NewUserMsg("hello", time.Now().UnixMilli())
	id := ss.AppendAIMessage(msg)
	if id == "" {
		t.Error("expected entry id")
	}
	if ss.GetLeafID() != id {
		t.Errorf("leaf should be %s, got %s", id, ss.GetLeafID())
	}
}

func TestSessionStoreAppendMessage(t *testing.T) {
	ss := InMemorySessionStore()

	msg1 := ai.NewUserMsg("first message", time.Now().UnixMilli())
	id1 := ss.AppendAIMessage(msg1)

	msg2 := ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "anthropic",
		Model:    "claude-3",
	})
	id2 := ss.AppendAIMessage(msg2)

	if ss.GetLeafID() != id2 {
		t.Errorf("leaf should be %s", id2)
	}

	entries := ss.GetEntries()
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

func TestSessionStoreThinkingLevelChange(t *testing.T) {
	ss := InMemorySessionStore()

	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendThinkingLevelChange("high")

	ctx := ss.BuildSessionContext()
	if ctx.ThinkingLevel != "high" {
		t.Errorf("expected thinking level 'high', got %q", ctx.ThinkingLevel)
	}
}

func TestSessionStoreModelChange(t *testing.T) {
	ss := InMemorySessionStore()

	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendModelChange("anthropic", "claude-4")

	ctx := ss.BuildSessionContext()
	if ctx.Model == nil {
		t.Fatal("expected model reference")
	}
	if ctx.Model.Provider != "anthropic" || ctx.Model.ModelID != "claude-4" {
		t.Errorf("unexpected model: %+v", ctx.Model)
	}
}

func TestSessionStoreBranching(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	id2 := ss.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))
	_ = id2

	// Branch from first entry
	ss.Branch(id1)
	if ss.GetLeafID() != id1 {
		t.Errorf("leaf should be %s after branch", id1)
	}

	// Append new branch
	id3 := ss.AppendAIMessage(ai.NewUserMsg("branch", time.Now().UnixMilli()))

	// Verify tree structure
	entry3 := ss.GetEntry(id3)
	if entry3 == nil {
		t.Fatal("expected entry")
	}
	if entry3.GetParentID() != id1 {
		t.Errorf("branch entry parent should be %s, got %s", id1, entry3.GetParentID())
	}

	// Build context should use branch path
	ctx := ss.BuildSessionContext()
	if len(ctx.Messages) != 2 {
		t.Errorf("expected 2 messages in branch, got %d", len(ctx.Messages))
	}
}

func TestSessionStoreResetLeaf(t *testing.T) {
	ss := InMemorySessionStore()

	ss.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	ss.ResetLeaf()
	if ss.GetLeafID() != "" {
		t.Error("leaf should be empty after reset")
	}

	// Append should create new root
	id := ss.AppendAIMessage(ai.NewUserMsg("new root", time.Now().UnixMilli()))
	entry := ss.GetEntry(id)
	if entry.GetParentID() != "" {
		t.Error("new entry after reset should have no parent")
	}
}

func TestSessionStoreBranchWithSummary(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))

	summaryID := ss.BranchWithSummary(id1, "Summary of abandoned branch", nil, false)
	if ss.GetLeafID() != summaryID {
		t.Errorf("leaf should be summary entry %s", summaryID)
	}

	// Context should include branch summary
	ctx := ss.BuildSessionContext()
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

func TestSessionStoreCompaction(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("message 1", time.Now().UnixMilli()))
	id2 := ss.AppendAIMessage(ai.NewUserMsg("message 2", time.Now().UnixMilli()))
	id3 := ss.AppendAIMessage(ai.NewUserMsg("message 3", time.Now().UnixMilli()))
	_ = id3

	// Compact, keeping from id2 onward
	ss.AppendCompaction("Summary of old messages", id2, 5000, nil, false)

	ctx := ss.BuildSessionContext()
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

func TestSessionStoreLabels(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("labeled", time.Now().UnixMilli()))

	ss.AppendLabelChange(id1, "important")
	if ss.GetLabel(id1) != "important" {
		t.Errorf("expected label 'important', got %q", ss.GetLabel(id1))
	}

	// Clear label
	ss.AppendLabelChange(id1, "")
	if ss.GetLabel(id1) != "" {
		t.Error("label should be cleared")
	}
}

func TestSessionStoreSessionName(t *testing.T) {
	ss := InMemorySessionStore()
	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendSessionInfo("My Session")

	name := ss.GetSessionName()
	if name != "My Session" {
		t.Errorf("expected 'My Session', got %q", name)
	}
}

func TestSessionStoreGetTree(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("root", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewUserMsg("child1", time.Now().UnixMilli()))

	// Branch from root
	ss.Branch(id1)
	ss.AppendAIMessage(ai.NewUserMsg("child2", time.Now().UnixMilli()))

	tree := ss.GetTree()
	if len(tree) != 1 {
		t.Fatalf("expected 1 root, got %d", len(tree))
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("expected 2 children of root, got %d", len(tree[0].Children))
	}
}

func TestSessionStorePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	ss := NewSessionStore(tmpDir, sessDir)
	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("hi there")},
		Provider: "test",
		Model:    "test-model",
	}))

	sessionFile := ss.GetSessionFile()
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

func TestSessionStoreOpenExisting(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	// Create and populate session
	sm1 := NewSessionStore(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "test",
		Model:    "test",
	}))
	sessionFile := sm1.GetSessionFile()
	origID := sm1.GetSessionID()

	// Close sm1 to release the flock before reopening.
	sm1.Close()

	// Open it again
	sm2, _ := OpenSessionStore(sessionFile)
	if sm2.GetSessionID() != origID {
		t.Errorf("session IDs should match: %s != %s", sm2.GetSessionID(), sm1.GetSessionID())
	}

	entries := sm2.GetEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries on reload, got %d", len(entries))
	}
}

func TestSessionStoreContinueRecent(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	// Create session with content
	sm1 := NewSessionStore(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "test",
		Model:    "test",
	}))
	origID := sm1.GetSessionID()

	// Close sm1 to release the flock before continuing.
	sm1.Close()

	// Continue recent should find it
	sm2, _ := ContinueRecentSession(tmpDir, sessDir)
	if sm2.GetSessionID() != origID {
		t.Errorf("should continue existing session: %s != %s", sm2.GetSessionID(), origID)
	}
}

func TestSessionStoreNewSessionAfterExisting(t *testing.T) {
	ss := InMemorySessionStore()

	ss.AppendAIMessage(ai.NewUserMsg("old", time.Now().UnixMilli()))
	oldID := ss.GetSessionID()

	ss.NewSession(nil)
	if ss.GetSessionID() == oldID {
		t.Error("new session should have different ID")
	}
	if ss.GetLeafID() != "" {
		t.Error("new session should have no leaf")
	}
	if len(ss.GetEntries()) != 0 {
		t.Error("new session should have no entries")
	}
}

func TestSessionStoreGetHeader(t *testing.T) {
	ss := InMemorySessionStore("/test/cwd")

	header := ss.GetHeader()
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

func TestSessionStoreGetChildren(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("root", time.Now().UnixMilli()))
	id2 := ss.AppendAIMessage(ai.NewUserMsg("child1", time.Now().UnixMilli()))
	_ = id2

	ss.Branch(id1)
	id3 := ss.AppendAIMessage(ai.NewUserMsg("child2", time.Now().UnixMilli()))
	_ = id3

	children := ss.GetChildren(id1)
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
	ss := InMemorySessionStore()

	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response")},
		Provider: "anthropic",
		Model:    "claude-3.5-sonnet",
	}))

	ctx := ss.BuildSessionContext()
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
	sm1 := NewSessionStore(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("session 1", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("response 1")},
		Provider: "test",
		Model:    "test",
	}))

	time.Sleep(10 * time.Millisecond) // ensure different timestamps

	sm2 := NewSessionStore(tmpDir, sessDir)
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

func TestSessionStoreCustomEntry(t *testing.T) {
	ss := InMemorySessionStore()

	data, _ := json.Marshal(map[string]string{"key": "value"})
	id := ss.AppendCustomEntry("my-extension", data)

	entry := ss.GetEntry(id)
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

func TestSessionStoreConcurrentAppendAndRead(t *testing.T) {
	// This test should be run with -race to detect data races.
	ss := InMemorySessionStore()

	done := make(chan struct{})

	// Writer goroutine: append messages
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			ss.AppendAIMessage(ai.NewUserMsg("msg", time.Now().UnixMilli()))
		}
	}()

	// Reader goroutine: read state concurrently
	for i := 0; i < 50; i++ {
		_ = ss.GetLeafID()
		_ = ss.GetEntries()
		_ = ss.GetSessionName()
		_ = ss.GetSessionID()
		_ = ss.GetCwd()
		_ = ss.GetTree()
		_ = ss.BuildSessionContext()
	}

	<-done

	entries := ss.GetEntries()
	if len(entries) != 50 {
		t.Errorf("expected 50 entries, got %d", len(entries))
	}
}

func TestSessionStoreConcurrentMultipleWriters(t *testing.T) {
	ss := InMemorySessionStore()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ss.AppendAIMessage(ai.NewUserMsg("msg", time.Now().UnixMilli()))
			}
		}()
	}

	// Concurrent readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = ss.GetEntries()
				_ = ss.GetLeafID()
				_ = ss.GetLeafEntry()
				_ = ss.BuildSessionContext()
			}
		}()
	}

	wg.Wait()

	entries := ss.GetEntries()
	if len(entries) != 50 {
		t.Errorf("expected 50 entries, got %d", len(entries))
	}
}

func TestSessionStoreCorruptFileRecovery(t *testing.T) {
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
	ss, _ := OpenSessionStore(sessionFile, sessDir)
	if ss.GetSessionID() != "test-corrupt" {
		t.Errorf("expected session ID 'test-corrupt', got %q", ss.GetSessionID())
	}

	entries := ss.GetEntries()
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

func TestSessionStoreEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// Create an empty session file
	sessionFile := filepath.Join(sessDir, "empty.jsonl")
	os.WriteFile(sessionFile, []byte(""), 0600)

	ss, _ := OpenSessionStore(sessionFile, sessDir)
	entries := ss.GetEntries()
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty file, got %d", len(entries))
	}
}

func TestSessionStoreCreateBranchedSession_InMemory(t *testing.T) {
	ss := InMemorySessionStore("test-cwd")

	id1 := ss.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	id2 := ss.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewUserMsg("third", time.Now().UnixMilli()))

	_ = id2
	originalID := ss.GetSessionID()
	if _, err := ss.CreateBranchedSession(id1); err != nil {
		t.Fatalf("CreateBranchedSession: %v", err)
	}

	// Should have a new session ID
	if ss.GetSessionID() == originalID {
		t.Error("expected new session ID after branching")
	}

	// Only the branch up to id1 should remain
	entries := ss.GetEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != id1 {
		t.Errorf("expected entry %s, got %s", id1, entries[0].ID)
	}
}

func TestSessionStoreCreateBranchedSession_Persisted(t *testing.T) {
	dir := t.TempDir()
	ss := NewSessionStore("test-cwd", dir)

	id1 := ss.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))
	// Need an assistant to trigger write
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{{Text: &ai.TextContent{Text: "reply"}}},
	}))

	oldFile := ss.GetSessionFile()
	newFile, err := ss.CreateBranchedSession(id1)
	if err != nil {
		t.Fatalf("CreateBranchedSession: %v", err)
	}

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
	entries := ss.GetEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after branch, got %d", len(entries))
	}
	if entries[0].ID != id1 {
		t.Errorf("expected entry %s, got %s", id1, entries[0].ID)
	}
}

func TestSessionStoreCreateBranchedSession_WithLabels(t *testing.T) {
	ss := InMemorySessionStore()

	id1 := ss.AppendAIMessage(ai.NewUserMsg("first", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewUserMsg("second", time.Now().UnixMilli()))

	// Add a label to id1
	ss.AppendLabelChange(id1, "important")

	if _, err := ss.CreateBranchedSession(id1); err != nil {
		t.Fatalf("CreateBranchedSession: %v", err)
	}

	// Label should be preserved
	label := ss.GetLabel(id1)
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
	srcSM := NewSessionStore(srcDir, filepath.Join(srcDir, "sessions"))
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
	sm1 := NewSessionStore("/proj1", project1Dir)
	sm1.AppendAIMessage(ai.NewUserMsg("msg1", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply1")},
	}))

	sm2 := NewSessionStore("/proj2", project2Dir)
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

func TestListAllSessions_MultipleDirs(t *testing.T) {
	agentDir1 := t.TempDir()
	agentDir2 := t.TempDir()

	// Create a session in each agent dir
	project1Dir := filepath.Join(agentDir1, "sessions", "project1")
	sm1 := NewSessionStore("/proj1", project1Dir)
	sm1.AppendAIMessage(ai.NewUserMsg("msg1", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply1")},
	}))

	project2Dir := filepath.Join(agentDir2, "sessions", "project2")
	sm2 := NewSessionStore("/proj2", project2Dir)
	sm2.AppendAIMessage(ai.NewUserMsg("msg2", time.Now().UnixMilli()))
	sm2.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply2")},
	}))

	// List from both dirs
	sessions, err := ListAllSessions(agentDir1, agentDir2)
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions from both dirs, got %d", len(sessions))
	}
}

func TestListAllSessions_ExtraDirMissing(t *testing.T) {
	agentDir := t.TempDir()

	// Create a session in the main dir
	projectDir := filepath.Join(agentDir, "sessions", "project1")
	ss := NewSessionStore("/proj1", projectDir)
	ss.AppendAIMessage(ai.NewUserMsg("msg1", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply1")},
	}))

	// Pass a nonexistent extra dir — should not error, just skip it
	sessions, err := ListAllSessions(agentDir, "/nonexistent/agent/dir")
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}
}

func TestListAllSessions_DeduplicatesOverlappingPaths(t *testing.T) {
	// Both agent dirs share the same sessions subdirectory via symlink,
	// so the same session files appear from both sources.
	agentDir1 := t.TempDir()
	agentDir2 := t.TempDir()

	// Create a session directory under agentDir1
	projectDir := filepath.Join(agentDir1, "sessions", "project1")
	ss := NewSessionStore("/proj1", projectDir)
	ss.AppendAIMessage(ai.NewUserMsg("msg", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply")},
	}))

	sessionFile := ss.GetSessionFile()
	if sessionFile == "" {
		t.Fatal("session file not written")
	}

	// Create a symlink so agentDir2/sessions points to the same dir
	os.MkdirAll(agentDir2, 0o755)
	err := os.Symlink(
		filepath.Join(agentDir1, "sessions"),
		filepath.Join(agentDir2, "sessions"),
	)
	if err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	// Both dirs see the same file — ListAllSessions should deduplicate
	sessions, err := ListAllSessions(agentDir1, agentDir2)
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session (deduped), got %d", len(sessions))
	}
}

func TestListAllSessions_DeduplicatesCopiedFiles(t *testing.T) {
	// Simulate two agent dirs that have an identical session file copied
	// to the same subpath (e.g. backup scenario). Since paths differ,
	// both should appear (dedup is by Path, not content).
	agentDir1 := t.TempDir()
	agentDir2 := t.TempDir()

	// Session in agentDir1
	project1Dir := filepath.Join(agentDir1, "sessions", "project1")
	sm1 := NewSessionStore("/proj1", project1Dir)
	sm1.AppendAIMessage(ai.NewUserMsg("msg1", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply1")},
	}))

	// Different session in agentDir2 under the same project name
	project2Dir := filepath.Join(agentDir2, "sessions", "project1")
	sm2 := NewSessionStore("/proj1", project2Dir)
	sm2.AppendAIMessage(ai.NewUserMsg("msg2", time.Now().UnixMilli()))
	sm2.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply2")},
	}))

	sessions, err := ListAllSessions(agentDir1, agentDir2)
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}

	// Different paths → no dedup → 2 sessions
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions (different paths), got %d", len(sessions))
	}
}

func TestListAllSessions_SameAgentDirPassedTwice(t *testing.T) {
	agentDir := t.TempDir()

	projectDir := filepath.Join(agentDir, "sessions", "project1")
	ss := NewSessionStore("/proj1", projectDir)
	ss.AppendAIMessage(ai.NewUserMsg("msg", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("reply")},
	}))

	// Pass the same agent dir twice — should deduplicate
	sessions, err := ListAllSessions(agentDir, agentDir)
	if err != nil {
		t.Fatalf("ListAllSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session (same dir passed twice, deduped by path), got %d", len(sessions))
	}
}

// ============================================================================
// Command entry tests
// ============================================================================

// TestSessionStoreCommandEntry verifies that AppendCommandEntry records a
// "command" entry and that it is NOT included in the LLM context.
func TestSessionStoreCommandEntry(t *testing.T) {
	ss := InMemorySessionStore()

	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	id := ss.AppendCommandEntry("model", "claude-opus-4")

	if id == "" {
		t.Fatal("expected non-empty entry ID")
	}

	entries := ss.GetEntries()
	var found *SessionEntry
	for _, e := range entries {
		if e.ID == id {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatal("command entry not found in session entries")
	}
	if found.Type != "command" {
		t.Errorf("expected type 'command', got %q", found.Type)
	}
	if found.Command != "model" {
		t.Errorf("expected command 'model', got %q", found.Command)
	}
	if found.Args != "claude-opus-4" {
		t.Errorf("expected args 'claude-opus-4', got %q", found.Args)
	}

	// Command entries must not appear in the LLM context.
	// The sole meaningful check: only the user message should be in context.
	ctx := ss.BuildSessionContext()
	if len(ctx.Messages) != 1 {
		t.Errorf("expected 1 message in context (command entry must be excluded), got %d", len(ctx.Messages))
	}
}

// TestSessionStoreCommandEntryNoArgs verifies AppendCommandEntry works with empty args.
func TestSessionStoreCommandEntryNoArgs(t *testing.T) {
	ss := InMemorySessionStore()
	id := ss.AppendCommandEntry("reload", "")
	if id == "" {
		t.Fatal("expected non-empty entry ID")
	}
	entries := ss.GetEntries()
	var found *SessionEntry
	for _, e := range entries {
		if e.ID == id {
			found = e
		}
	}
	if found == nil {
		t.Fatal("command entry not found")
	}
	if found.Args != "" {
		t.Errorf("expected empty args, got %q", found.Args)
	}
}

// TestSessionStoreCommandEntryRoundTrip verifies that command entries survive
// a write-to-disk / read-from-disk cycle and remain correctly typed.
func TestSessionStoreCommandEntryRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, "sessions")

	// Write session with a command entry (need an assistant message to flush).
	sm1 := NewSessionStore(tmpDir, sessDir)
	sm1.AppendAIMessage(ai.NewUserMsg("hi", time.Now().UnixMilli()))
	sm1.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("hello")},
		Provider: "test",
		Model:    "test-model",
	}))
	sm1.AppendCommandEntry("compact", "summarize recent work")
	sessionFile := sm1.GetSessionFile()
	if sessionFile == "" {
		t.Fatal("expected session file to be set")
	}

	// Read it back.
	sm2, _ := OpenSessionStore(sessionFile)
	entries := sm2.GetEntries()

	var cmdEntry *SessionEntry
	for _, e := range entries {
		if e.Type == "command" {
			cmdEntry = e
			break
		}
	}
	if cmdEntry == nil {
		t.Fatal("command entry not found after round-trip")
	}
	if cmdEntry.Command != "compact" {
		t.Errorf("expected command 'compact', got %q", cmdEntry.Command)
	}
	if cmdEntry.Args != "summarize recent work" {
		t.Errorf("expected args 'summarize recent work', got %q", cmdEntry.Args)
	}

	// Context should not include the command entry.
	ctx := sm2.BuildSessionContext()
	for _, msg := range ctx.Messages {
		if msg.Message.Role() == "" && msg.Custom == nil {
			t.Error("unexpected nil message in context")
		}
	}
	// Only user + assistant messages (2), no command.
	if len(ctx.Messages) != 2 {
		t.Errorf("expected 2 messages in restored context, got %d", len(ctx.Messages))
	}
}

// TestSessionStoreCommandEntryParentChain verifies that command entries are
// linked into the entry parent chain correctly (each entry's parent is the
// previous leaf, so commands don't break the chain for subsequent messages).
func TestSessionStoreCommandEntryParentChain(t *testing.T) {
	ss := InMemorySessionStore()

	userID := ss.AppendAIMessage(ai.NewUserMsg("question", time.Now().UnixMilli()))
	cmdID := ss.AppendCommandEntry("thinking", "high")

	// Command's parent should be the user message.
	cmdEntry := ss.GetEntry(cmdID)
	if cmdEntry == nil {
		t.Fatal("command entry not found")
	}
	if cmdEntry.ParentID != userID {
		t.Errorf("command entry parent should be %s, got %s", userID, cmdEntry.ParentID)
	}

	// Subsequent message should have the command as parent.
	assistantID := ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("response")},
	}))
	assistantEntry := ss.GetEntry(assistantID)
	if assistantEntry == nil {
		t.Fatal("assistant entry not found")
	}
	if assistantEntry.ParentID != cmdID {
		t.Errorf("assistant entry parent should be %s (command), got %s", cmdID, assistantEntry.ParentID)
	}
}

func TestSessionStore_AppendPlanUpdate_PersistedAndRestored(t *testing.T) {
	tmpDir := t.TempDir()
	ss := NewSessionStore(tmpDir, filepath.Join(tmpDir, "sessions"))

	// Need an assistant message first so the session flushes to disk
	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("hi")},
		Provider: "test",
		Model:    "test-model",
	}))

	entries := []agent.PlanEntry{
		{Content: "Step 1", Status: agent.PlanEntryStatusInProgress, Priority: agent.PlanEntryPriorityHigh},
		{Content: "Step 2", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityMedium},
	}
	ss.AppendPlanUpdate("Test Plan", entries, nil)

	sessionFile := ss.GetSessionFile()
	if sessionFile == "" {
		t.Fatal("expected a session file to be written")
	}

	// Reload the session from disk and verify plan is in context
	sm2, _ := OpenSessionStore(sessionFile)
	ctx := sm2.BuildSessionContext()

	if len(ctx.PlanEntries) != 2 {
		t.Fatalf("expected 2 plan entries after reload, got %d", len(ctx.PlanEntries))
	}
	if ctx.PlanEntries[0].Content != "Step 1" {
		t.Errorf("expected Step 1, got %s", ctx.PlanEntries[0].Content)
	}
	if ctx.PlanEntries[1].Status != agent.PlanEntryStatusPending {
		t.Errorf("expected pending, got %s", ctx.PlanEntries[1].Status)
	}
	if ctx.PlanTitle != "Test Plan" {
		t.Errorf("expected plan title 'Test Plan', got %q", ctx.PlanTitle)
	}
}

func TestSessionStore_AppendPlanUpdate_ClearRestored(t *testing.T) {
	tmpDir := t.TempDir()
	ss := NewSessionStore(tmpDir, filepath.Join(tmpDir, "sessions"))

	ss.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	ss.AppendAIMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content:  []ai.AssistantContent{ai.NewTextContent("hi")},
		Provider: "test",
		Model:    "test-model",
	}))

	// Set a plan then clear it
	ss.AppendPlanUpdate("Temp Plan", []agent.PlanEntry{
		{Content: "Step 1", Status: agent.PlanEntryStatusInProgress, Priority: agent.PlanEntryPriorityHigh},
	}, nil)
	ss.AppendPlanUpdate("", nil, nil) // clear

	sessionFile := ss.GetSessionFile()
	sm2, _ := OpenSessionStore(sessionFile)
	ctx := sm2.BuildSessionContext()

	if len(ctx.PlanEntries) != 0 {
		t.Errorf("expected plan to be empty after clear, got %d entries", len(ctx.PlanEntries))
	}
	if ctx.PlanTitle != "" {
		t.Errorf("expected empty plan title after clear, got %q", ctx.PlanTitle)
	}
}

func TestBuildSessionContextFromEntries_PlanNotInMessages(t *testing.T) {
	entries := []*SessionEntry{
		{
			Type:      "plan_update",
			ID:        "e1",
			ParentID:  "",
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	// Marshal plan entries into it
	planData, _ := json.Marshal([]agent.PlanEntry{
		{Content: "Do thing", Status: agent.PlanEntryStatusPending, Priority: agent.PlanEntryPriorityHigh},
	})
	entries[0].PlanEntries = planData
	entries[0].PlanTitle = "My Plan"

	byID := map[string]*SessionEntry{"e1": entries[0]}
	ctx := BuildSessionContextFromEntries(entries, "e1", byID)

	if len(ctx.Messages) != 0 {
		t.Errorf("plan_update should not produce LLM messages, got %d", len(ctx.Messages))
	}
	if len(ctx.PlanEntries) != 1 {
		t.Fatalf("expected 1 plan entry in context, got %d", len(ctx.PlanEntries))
	}
	if ctx.PlanEntries[0].Content != "Do thing" {
		t.Errorf("wrong plan entry content: %s", ctx.PlanEntries[0].Content)
	}
	if ctx.PlanTitle != "My Plan" {
		t.Errorf("expected plan title 'My Plan', got %q", ctx.PlanTitle)
	}
}

// Test helper methods — moved from session.go because they are only used in tests.

func (ss *SessionStore) GetLeafEntry() *SessionEntry {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if ss.leafID == "" {
		return nil
	}
	return ss.byID[ss.leafID]
}

func (ss *SessionStore) GetLabel(id string) string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.labelsById[id]
}

func (ss *SessionStore) GetHeader() *SessionHeader {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.header
}

func (ss *SessionStore) GetChildren(parentID string) []*SessionEntry {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	var children []*SessionEntry
	for _, e := range ss.entries {
		if e.ParentID == parentID {
			children = append(children, e)
		}
	}
	return children
}

func (ss *SessionStore) ResetLeaf() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.leafID = ""
}

func TestMergeSessions(t *testing.T) {
	now := time.Now()
	a := []SessionListInfo{
		{Path: "/a/1.jsonl", ID: "a1", Modified: now.Add(-1 * time.Minute)},
		{Path: "/a/2.jsonl", ID: "a2", Modified: now.Add(-3 * time.Minute)},
	}
	b := []SessionListInfo{
		{Path: "/b/1.jsonl", ID: "b1", Modified: now.Add(-2 * time.Minute)},
		{Path: "/a/1.jsonl", ID: "a1", Modified: now.Add(-1 * time.Minute)}, // duplicate
	}

	merged := MergeSessions(a, b)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged sessions, got %d", len(merged))
	}
	// Should be sorted most-recent first.
	if merged[0].ID != "a1" || merged[1].ID != "b1" || merged[2].ID != "a2" {
		t.Errorf("unexpected order: %s, %s, %s", merged[0].ID, merged[1].ID, merged[2].ID)
	}
}

func TestSessionDirForCwd(t *testing.T) {
	dir := SessionDirForCwd("/home/user/.config/fir", "/Users/dev/myproject")
	expected := DefaultSessionDir("/home/user/.config/fir", "/Users/dev/myproject")
	if dir != expected {
		t.Errorf("SessionDirForCwd = %q, want %q", dir, expected)
	}
}
