// File operation tracking for compaction.
// Split from utils.go.
package compaction

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/agent"
)

// FileOperations tracks files read, written, and edited during a conversation.
//
// EntryIDs maps each tracked path to the most-recent session-store entry ID
// where the operation appeared. Empty string means the source entry ID is
// not known (e.g. carried forward from an older compaction's Details).
type FileOperations struct {
	Read     map[string]struct{}
	Written  map[string]struct{}
	Edited   map[string]struct{}
	EntryIDs map[string]string
}

// NewFileOperations creates an empty FileOperations.
func NewFileOperations() *FileOperations {
	return &FileOperations{
		Read:     make(map[string]struct{}),
		Written:  make(map[string]struct{}),
		Edited:   make(map[string]struct{}),
		EntryIDs: make(map[string]string),
	}
}

// ExtractFileOpsFromMessage extracts file operations from tool calls in an assistant message.
// entryID is the stable session-store ID associated with the message; pass
// "" if it is not known.
func ExtractFileOpsFromMessage(message agent.AgentMessage, entryID string, fileOps *FileOperations) {
	if message.Role() != "assistant" {
		return
	}
	assistant := message.Message.AsAssistant()
	if assistant == nil {
		return
	}

	record := func(op string, path string) {
		if path == "" {
			return
		}
		switch op {
		case "read":
			fileOps.Read[path] = struct{}{}
		case "write":
			fileOps.Written[path] = struct{}{}
		case "edit":
			fileOps.Edited[path] = struct{}{}
		default:
			return
		}
		if entryID != "" {
			fileOps.EntryIDs[path] = entryID
		}
	}

	for _, block := range assistant.Content {
		if block.ToolCall == nil {
			continue
		}
		tc := block.ToolCall

		// Bash: parse the command for write/edit-shaped patterns.
		// (Phase 2 #8 — redirects, tee. sed -i is TODO; see
		// extractBashWrittenPaths.)
		if tc.Name == "bash" {
			cmd, _ := tc.Arguments["command"].(string)
			for _, p := range extractBashWrittenPaths(cmd) {
				record("write", p)
			}
			continue
		}

		// Path-keyed tools.
		path, ok := tc.Arguments["path"].(string)
		if !ok || path == "" {
			continue
		}

		switch tc.Name {
		case "read":
			record("read", path)
		case "write":
			record("write", path)
		case "edit", "multi_edit", "MultiEdit":
			record("edit", path)
		}
	}
}

// extractBashWrittenPaths heuristically pulls likely-written file paths
// from a bash command string. Patterns recognised:
//
//   - redirect:  > FILE   >> FILE
//   - tee:       tee FILE | tee -a FILE
//
// Pseudo-targets (/dev/null, /dev/stdout, &1, &2, etc.) are skipped.
// This is intentionally conservative — false positives are preferable to
// silent silence in the summary, but we don't try to parse arbitrary shell.
//
// TODO(compaction): `sed -i` and `awk -i inplace` need a real tokeniser
// (Go regex lacks lookbehind, and the expression token sits between the
// -i flag and the file argument). Skip for now.
func extractBashWrittenPaths(command string) []string {
	if command == "" {
		return nil
	}
	out := make(map[string]struct{})

	// Redirects: capture the token after > or >>.
	for _, m := range bashRedirectRe.FindAllStringSubmatch(command, -1) {
		if p := normaliseBashTarget(m[1]); p != "" {
			out[p] = struct{}{}
		}
	}
	// tee.
	for _, m := range bashTeeRe.FindAllStringSubmatch(command, -1) {
		if p := normaliseBashTarget(m[1]); p != "" {
			out[p] = struct{}{}
		}
	}

	if len(out) == 0 {
		return nil
	}
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	return paths
}

func normaliseBashTarget(tok string) string {
	tok = strings.Trim(tok, "\"'")
	if tok == "" {
		return ""
	}
	// Skip fd duplications (>&2 etc) and /dev/null-like sinks.
	if strings.HasPrefix(tok, "&") {
		return ""
	}
	if tok == "/dev/null" || tok == "/dev/stdout" || tok == "/dev/stderr" || tok == "/dev/tty" {
		return ""
	}
	return tok
}

var (
	// >FILE / >>FILE — disallow `&` (fd dup) right after the operator.
	bashRedirectRe = regexp.MustCompile(`>>?\s*([^\s|&;<>()]+)`)
	bashTeeRe      = regexp.MustCompile(`\btee\b(?:\s+-[aA-Za-z]+)*\s+("[^"]+"|'[^']+'|[^\s|&;<>()]+)`)
)

// ComputeFileLists returns readFiles (only read, not modified) and modifiedFiles
// as plain path slices, sorted, deduplicated. Use FormatFileOperations to
// render them — it will look up entry IDs from the FileOperations.
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
// When entryIDs[path] is non-empty, the rendered line is
// "path (entry <id>)" — a back-reference to the session-store entry that
// performed the most recent op on that path.
func FormatFileOperations(readFiles, modifiedFiles []string, entryIDs map[string]string) string {
	render := func(paths []string) []string {
		out := make([]string, len(paths))
		for i, p := range paths {
			if id := entryIDs[p]; id != "" {
				out[i] = p + " (entry " + id + ")"
			} else {
				out[i] = p
			}
		}
		return out
	}
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, fmt.Sprintf("<read-files>\n%s\n</read-files>", strings.Join(render(readFiles), "\n")))
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, fmt.Sprintf("<modified-files>\n%s\n</modified-files>", strings.Join(render(modifiedFiles), "\n")))
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}
