package compaction

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

func TestFileOperations_ExtractFromMessage(t *testing.T) {
	fileOps := NewFileOperations()

	assistantMsg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "read", map[string]any{"path": "/src/main.go"}),
			ai.NewToolCallContent("2", "edit", map[string]any{"path": "/src/main.go", "oldText": "a", "newText": "b"}),
			ai.NewToolCallContent("3", "write", map[string]any{"path": "/src/new.go"}),
			ai.NewToolCallContent("4", "read", map[string]any{"path": "/src/util.go"}),
			ai.NewTextContent("Some explanation"),
		},
	})

	ExtractFileOpsFromMessage(agent.NewAgentMessage(assistantMsg), "e1", fileOps)

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
	ExtractFileOpsFromMessage(agent.NewAgentMessage(userMsg), "", fileOps)

	if len(fileOps.Read) != 0 || len(fileOps.Written) != 0 || len(fileOps.Edited) != 0 {
		t.Error("expected empty file ops for non-assistant message")
	}
}

func TestFileOperations_BashRedirect(t *testing.T) {
	fileOps := NewFileOperations()
	asst := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "bash", map[string]any{
				"command": "echo hi > out.txt && cat data | tee /tmp/log.txt",
			}),
		},
	})
	ExtractFileOpsFromMessage(agent.NewAgentMessage(asst), "e1", fileOps)
	if _, ok := fileOps.Written["out.txt"]; !ok {
		t.Errorf("expected out.txt in Written; got %#v", fileOps.Written)
	}
	if _, ok := fileOps.Written["/tmp/log.txt"]; !ok {
		t.Errorf("expected /tmp/log.txt in Written; got %#v", fileOps.Written)
	}
}

func TestFileOperations_BashSedInplace_TODO(t *testing.T) {
	t.Skip("sed -i extraction needs a real tokeniser; tracked as TODO in fileops.go")
}

func TestFileOperations_BashSkipsDevNull(t *testing.T) {
	fileOps := NewFileOperations()
	asst := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "bash", map[string]any{
				"command": "noisy 2>/dev/null >&2",
			}),
		},
	})
	ExtractFileOpsFromMessage(agent.NewAgentMessage(asst), "e1", fileOps)
	if len(fileOps.Written) != 0 {
		t.Errorf("expected nothing tracked, got %#v", fileOps.Written)
	}
}

func TestFileOperations_MultiEdit(t *testing.T) {
	fileOps := NewFileOperations()
	asst := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("1", "multi_edit", map[string]any{"path": "/x.go"}),
		},
	})
	ExtractFileOpsFromMessage(agent.NewAgentMessage(asst), "e1", fileOps)
	if _, ok := fileOps.Edited["/x.go"]; !ok {
		t.Errorf("expected /x.go in Edited via multi_edit; got %#v", fileOps.Edited)
	}
}

func TestComputeFileLists(t *testing.T) {
	fileOps := NewFileOperations()
	fileOps.Read["/src/main.go"] = struct{}{}
	fileOps.Read["/src/util.go"] = struct{}{}
	fileOps.Edited["/src/main.go"] = struct{}{}
	fileOps.Written["/src/new.go"] = struct{}{}

	readFiles, modifiedFiles := ComputeFileLists(fileOps)

	if len(readFiles) != 1 || readFiles[0] != "/src/util.go" {
		t.Errorf("expected readFiles=[/src/util.go], got %v", readFiles)
	}

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
	result := FormatFileOperations([]string{"/src/util.go"}, []string{"/src/main.go", "/src/new.go"}, nil)

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
}

func TestFormatFileOperations_Empty(t *testing.T) {
	result := FormatFileOperations(nil, nil, nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFormatFileOperations_OnlyRead(t *testing.T) {
	result := FormatFileOperations([]string{"/a.go"}, nil, nil)
	if !strings.Contains(result, "<read-files>") {
		t.Error("expected <read-files> tag")
	}
	if strings.Contains(result, "<modified-files>") {
		t.Error("did not expect <modified-files> tag")
	}
}

func TestFormatFileOperations_OnlyModified(t *testing.T) {
	result := FormatFileOperations(nil, []string{"/a.go"}, nil)
	if strings.Contains(result, "<read-files>") {
		t.Error("did not expect <read-files> tag")
	}
	if !strings.Contains(result, "<modified-files>") {
		t.Error("expected <modified-files> tag")
	}
}
