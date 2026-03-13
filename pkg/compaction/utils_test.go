package compaction

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

func TestFileOperations_ExtractFromMessage(t *testing.T) {
	fileOps := NewFileOperations()

	// Create an assistant message with tool calls
	assistantMsg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "read", map[string]any{"path": "/src/main.go"}),
			ai.NewToolCallContent("2", "edit", map[string]any{"path": "/src/main.go", "oldText": "a", "newText": "b"}),
			ai.NewToolCallContent("3", "write", map[string]any{"path": "/src/new.go"}),
			ai.NewToolCallContent("4", "read", map[string]any{"path": "/src/util.go"}),
			ai.NewTextContent("Some explanation"),
		},
	})

	ExtractFileOpsFromMessage(agent.NewAgentMessage(assistantMsg), fileOps)

	if _, ok := fileOps.Read["/src/main.go"]; !ok {
		t.Error("expected /src/main.go in Read")
	}
	if _, ok := fileOps.Read["/src/util.go"]; !ok {
		t.Error("expected /src/util.go in Read")
	}
	if _, ok := fileOps.Edited["/src/main.go"]; !ok {
		t.Error("expected /src/main.go in Edited")
	}
	if _, ok := fileOps.Written["/src/new.go"]; !ok {
		t.Error("expected /src/new.go in Written")
	}
}

func TestFileOperations_IgnoreNonAssistant(t *testing.T) {
	fileOps := NewFileOperations()

	userMsg := ai.NewUserMsg("hello", 0)
	ExtractFileOpsFromMessage(agent.NewAgentMessage(userMsg), fileOps)

	if len(fileOps.Read) != 0 || len(fileOps.Written) != 0 || len(fileOps.Edited) != 0 {
		t.Error("expected empty file ops for non-assistant message")
	}
}

func TestComputeFileLists(t *testing.T) {
	fileOps := NewFileOperations()
	fileOps.Read["/src/main.go"] = struct{}{}
	fileOps.Read["/src/util.go"] = struct{}{}
	fileOps.Edited["/src/main.go"] = struct{}{} // also modified, should not appear in read-only
	fileOps.Written["/src/new.go"] = struct{}{}

	readFiles, modifiedFiles := ComputeFileLists(fileOps)

	// /src/util.go was only read, not modified
	if len(readFiles) != 1 || readFiles[0] != "/src/util.go" {
		t.Errorf("expected readFiles=[/src/util.go], got %v", readFiles)
	}

	// /src/main.go was edited, /src/new.go was written
	if len(modifiedFiles) != 2 {
		t.Errorf("expected 2 modified files, got %v", modifiedFiles)
	}
}

func TestComputeFileLists_Empty(t *testing.T) {
	fileOps := NewFileOperations()
	readFiles, modifiedFiles := ComputeFileLists(fileOps)
	if len(readFiles) != 0 || len(modifiedFiles) != 0 {
		t.Error("expected empty lists")
	}
}

func TestFormatFileOperations(t *testing.T) {
	result := FormatFileOperations([]string{"/src/util.go"}, []string{"/src/main.go", "/src/new.go"})

	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "<read-files>") {
		t.Error("expected <read-files> tag")
	}
	if !strings.Contains(result, "/src/util.go") {
		t.Error("expected /src/util.go in output")
	}
	if !strings.Contains(result, "<modified-files>") {
		t.Error("expected <modified-files> tag")
	}
	if !strings.Contains(result, "/src/main.go") {
		t.Error("expected /src/main.go in output")
	}
}

func TestFormatFileOperations_Empty(t *testing.T) {
	result := FormatFileOperations(nil, nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFormatFileOperations_OnlyRead(t *testing.T) {
	result := FormatFileOperations([]string{"/a.go"}, nil)
	if !strings.Contains(result, "<read-files>") {
		t.Error("expected <read-files> tag")
	}
	if strings.Contains(result, "<modified-files>") {
		t.Error("did not expect <modified-files> tag")
	}
}

func TestFormatFileOperations_OnlyModified(t *testing.T) {
	result := FormatFileOperations(nil, []string{"/a.go"})
	if strings.Contains(result, "<read-files>") {
		t.Error("did not expect <read-files> tag")
	}
	if !strings.Contains(result, "<modified-files>") {
		t.Error("expected <modified-files> tag")
	}
}

func TestSerializeConversation(t *testing.T) {
	messages := []ai.Message{
		ai.NewUserMsg("Hello, can you help me?", 100),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{
				ai.NewTextContent("Sure, let me take a look."),
			},
		}),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{
				ai.NewThinkingContent("Let me think about this..."),
				ai.NewToolCallContent("1", "read", map[string]any{"path": "/main.go"}),
			},
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			ToolCallID: "1",
			ToolName:   "read",
			Content: []ai.ToolResultContent{
				{Type: "text", Text: "package main"},
			},
		}),
	}

	result := SerializeConversation(messages)

	if !strings.Contains(result, "[User]: Hello, can you help me?") {
		t.Error("expected user message in output")
	}
	if !strings.Contains(result, "[Assistant]: Sure, let me take a look.") {
		t.Error("expected assistant text in output")
	}
	if !strings.Contains(result, "[Assistant thinking]: Let me think about this...") {
		t.Error("expected thinking in output")
	}
	if !strings.Contains(result, "[Assistant tool calls]: read(") {
		t.Error("expected tool calls in output")
	}
	if !strings.Contains(result, "[Tool result]: package main") {
		t.Error("expected tool result in output")
	}
}

func TestSerializeConversation_Empty(t *testing.T) {
	result := SerializeConversation(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestSummarizationSystemPrompt(t *testing.T) {
	if SummarizationSystemPrompt == "" {
		t.Error("expected non-empty system prompt")
	}
	if !strings.Contains(SummarizationSystemPrompt, "summarization") {
		t.Error("expected 'summarization' in system prompt")
	}
}
