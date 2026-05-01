// File operation tracking for compaction.
// Split from utils.go.
package compaction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/agent"
)

// FileOperations tracks files read, written, and edited during a conversation.
type FileOperations struct {
	Read    map[string]struct{}
	Written map[string]struct{}
	Edited  map[string]struct{}
}

// NewFileOperations creates an empty FileOperations.
func NewFileOperations() *FileOperations {
	return &FileOperations{
		Read:    make(map[string]struct{}),
		Written: make(map[string]struct{}),
		Edited:  make(map[string]struct{}),
	}
}

// ExtractFileOpsFromMessage extracts file operations from tool calls in an assistant message.
func ExtractFileOpsFromMessage(message agent.AgentMessage, fileOps *FileOperations) {
	if message.Role() != "assistant" {
		return
	}
	assistant := message.Message.AsAssistant()
	if assistant == nil {
		return
	}

	for _, block := range assistant.Content {
		if block.ToolCall == nil {
			continue
		}
		tc := block.ToolCall
		path, ok := tc.Arguments["path"].(string)
		if !ok || path == "" {
			continue
		}

		switch tc.Name {
		case "read":
			fileOps.Read[path] = struct{}{}
		case "write":
			fileOps.Written[path] = struct{}{}
		case "edit":
			fileOps.Edited[path] = struct{}{}
		}
	}
}

// ComputeFileLists returns readFiles (only read, not modified) and modifiedFiles.
func ComputeFileLists(fileOps *FileOperations) (readFiles, modifiedFiles []string) {
	modified := make(map[string]struct{})
	for f := range fileOps.Edited {
		modified[f] = struct{}{}
	}
	for f := range fileOps.Written {
		modified[f] = struct{}{}
	}

	for f := range fileOps.Read {
		if _, isModified := modified[f]; !isModified {
			readFiles = append(readFiles, f)
		}
	}

	for f := range modified {
		modifiedFiles = append(modifiedFiles, f)
	}

	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return
}

// FormatFileOperations formats file operations as XML tags for summary.
func FormatFileOperations(readFiles, modifiedFiles []string) string {
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, fmt.Sprintf("<read-files>\n%s\n</read-files>", strings.Join(readFiles, "\n")))
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, fmt.Sprintf("<modified-files>\n%s\n</modified-files>", strings.Join(modifiedFiles, "\n")))
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}
