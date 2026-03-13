// Ported from: packages/coding-agent/src/core/tools/edit.ts + edit-diff.ts
// Upstream hash: 1caadb2e
package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// EditToolParams are the parameters for the edit tool.
type EditToolParams struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// editResult holds the output of applyEditLogic.
type editResult struct {
	finalContent string
	diff         string
	firstLine    *int
}

// applyEditLogic applies BOM handling, line-ending normalization, fuzzy match, and
// replacement to the given content. It is the shared core of both NewEditTool and
// NewEditToolWithReadWriter.
func applyEditLogic(content, oldText, newText, path string) (editResult, error) {
	firlog.Debug("edit file", "path", path, "oldLen", len(oldText), "newLen", len(newText))
	bom := ""
	if strings.HasPrefix(content, "\uFEFF") {
		bom = "\uFEFF"
		content = content[len("\uFEFF"):]
	}
	originalEnding := detectLineEnding(content)
	normalizedContent := normalizeToLF(content)
	normalizedOldText := normalizeToLF(oldText)
	normalizedNewText := normalizeToLF(newText)

	matchResult := fuzzyFindText(normalizedContent, normalizedOldText)
	if !matchResult.found {
		return editResult{}, fmt.Errorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
	}
	if matchResult.occurrences > 1 {
		return editResult{}, fmt.Errorf("Found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.", matchResult.occurrences, path)
	}

	baseContent := matchResult.contentForReplacement
	newContent := baseContent[:matchResult.index] + normalizedNewText + baseContent[matchResult.index+matchResult.matchLength:]
	if baseContent == newContent {
		return editResult{}, fmt.Errorf("No changes made to %s. The replacement produced identical content.", path)
	}

	finalContent := bom + restoreLineEndings(newContent, originalEnding)
	diffResult := GenerateDiffString(baseContent, newContent, 4)
	return editResult{
		finalContent: finalContent,
		diff:         diffResult.Diff,
		firstLine:    diffResult.FirstChangedLine,
	}, nil
}

// NewEditTool creates the edit (find-and-replace) tool.
func NewEditTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        "edit",
			Description: "Edit a file by replacing exact text. The oldText must match exactly (including whitespace). Use this for precise, surgical edits.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to edit (relative or absolute)",
					},
					"oldText": map[string]any{
						"type":        "string",
						"description": "Exact text to find and replace (must match exactly)",
					},
					"newText": map[string]any{
						"type":        "string",
						"description": "New text to replace the old text with",
					},
				},
				"required": []string{"path", "oldText", "newText"},
			},
		},
		Label: "edit",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			path, _ := params["path"].(string)
			oldText, _ := params["oldText"].(string)
			newText, _ := params["newText"].(string)

			if path == "" {
				return agent.AgentToolResult{}, fmt.Errorf("path is required")
			}

			absolutePath := ResolveToCwd(path, cwd)

			// Check context
			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Check if file exists and is readable/writable
			if _, err := os.Stat(absolutePath); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("File not found: %s", path)
			}

			// Read the file
			rawContent, err := os.ReadFile(absolutePath)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("failed to read %s: %w", path, err)
			}

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Strip BOM, normalize, fuzzy-match, replace — shared with NewEditToolWithReadWriter.
			result, err := applyEditLogic(string(rawContent), oldText, newText, path)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			// Write the result
			if err := os.WriteFile(absolutePath, []byte(result.finalContent), 0644); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("failed to write %s: %w", path, err)
			}

			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{
					{Type: "text", Text: fmt.Sprintf("Successfully replaced text in %s.", path)},
				},
				Details: &EditToolDetails{
					Diff:             result.diff,
					FirstChangedLine: result.firstLine,
				},
			}, nil
		},
	}
}

// NewEditToolWithReadWriter creates an edit tool that uses readFn/writeFn for file I/O.
// This enables ACP client file delegation (Zed's "Reject All"/"Keep All" review UI).
func NewEditToolWithReadWriter(cwd string, readFn ReadFileFn, writeFn WriteFileFn) agent.AgentTool {
	t := NewEditTool(cwd)
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		path, _ := params["path"].(string)
		oldText, _ := params["oldText"].(string)
		newText, _ := params["newText"].(string)
		if path == "" {
			return agent.AgentToolResult{}, fmt.Errorf("path is required")
		}
		absolutePath := ResolveToCwd(path, cwd)
		if ctx.Err() != nil {
			return agent.AgentToolResult{}, ctx.Err()
		}
		// Read via delegated function
		rawContent, err := readFn(ctx, absolutePath)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("File not found: %s", path)
		}
		// Apply shared edit logic (BOM, normalization, fuzzy match, replacement).
		result, err := applyEditLogic(rawContent, oldText, newText, path)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := writeFn(ctx, absolutePath, result.finalContent); err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("failed to write %s: %w", path, err)
		}
		return agent.AgentToolResult{
			Content: []ai.ToolResultContent{
				{Type: "text", Text: fmt.Sprintf("Successfully replaced text in %s.", path)},
			},
			Details: &EditToolDetails{
				Diff:             result.diff,
				FirstChangedLine: result.firstLine,
			},
		}, nil
	}
	return t
}

// --- edit-diff helpers ---

// detectLineEnding returns the dominant line ending in the content.
func detectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")
	if lfIdx == -1 {
		return "\n"
	}
	if crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

// normalizeToLF converts all line endings to LF.
func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

// restoreLineEndings converts LF back to the original line ending.
func restoreLineEndings(text string, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// normalizeForFuzzyMatch applies progressive normalization:
// - Strip trailing whitespace per line
// - Normalize smart quotes to ASCII
// - Normalize Unicode dashes to ASCII hyphen
// - Normalize special Unicode spaces to regular space
func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = strings.Join(lines, "\n")

	// Smart single quotes → '
	for _, r := range []rune{'\u2018', '\u2019', '\u201A', '\u201B'} {
		text = strings.ReplaceAll(text, string(r), "'")
	}
	// Smart double quotes → "
	for _, r := range []rune{'\u201C', '\u201D', '\u201E', '\u201F'} {
		text = strings.ReplaceAll(text, string(r), "\"")
	}
	// Various dashes → -
	for _, r := range []rune{'\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212'} {
		text = strings.ReplaceAll(text, string(r), "-")
	}
	// Special spaces → regular space
	for _, r := range []rune{'\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000'} {
		text = strings.ReplaceAll(text, string(r), " ")
	}

	return text
}

// fuzzyMatchResult holds the result of fuzzy text matching.
type fuzzyMatchResult struct {
	found                 bool
	index                 int
	matchLength           int
	usedFuzzyMatch        bool
	contentForReplacement string
	occurrences           int // total number of matches (exact or fuzzy)
}

// fuzzyFindText tries exact match first, then fuzzy match.
// When fuzzy matching, it maps the match back to the original content
// so that only the matched region is affected, not the entire file.
func fuzzyFindText(content, oldText string) fuzzyMatchResult {
	// Try exact match first
	idx := strings.Index(content, oldText)
	if idx != -1 {
		return fuzzyMatchResult{
			found:                 true,
			index:                 idx,
			matchLength:           len(oldText),
			usedFuzzyMatch:        false,
			contentForReplacement: content,
			occurrences:           strings.Count(content, oldText),
		}
	}

	// Try fuzzy match
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)
	fuzzyIdx := strings.Index(fuzzyContent, fuzzyOldText)
	if fuzzyIdx == -1 {
		return fuzzyMatchResult{found: false, index: -1, contentForReplacement: content}
	}

	// Map the fuzzy match position back to the original content.
	// Build a mapping from normalized byte offsets to original byte offsets.
	origStart, origEnd := mapFuzzyToOriginal(content, fuzzyContent, fuzzyIdx, fuzzyIdx+len(fuzzyOldText))

	return fuzzyMatchResult{
		found:                 true,
		index:                 origStart,
		matchLength:           origEnd - origStart,
		usedFuzzyMatch:        true,
		contentForReplacement: content,
		occurrences:           strings.Count(fuzzyContent, fuzzyOldText),
	}
}

// mapFuzzyToOriginal maps byte offsets in normalized text back to byte offsets
// in the original text. It re-normalizes character by character to build the mapping.
func mapFuzzyToOriginal(original, normalized string, normStart, normEnd int) (origStart, origEnd int) {
	// We rebuild the normalization process, tracking position in both strings.
	// Since normalizeForFuzzyMatch works line-by-line (trimming trailing whitespace)
	// then does character replacements, we do it in the same order.

	// Step 1: Strip trailing whitespace per line to get an intermediate form,
	// building a mapping from intermediate positions to original positions.
	origLines := strings.Split(original, "\n")
	var intermediateBuilder strings.Builder
	// origPositions[i] = original byte offset corresponding to intermediate byte offset i
	var origPositions []int

	origOffset := 0
	for lineIdx, line := range origLines {
		trimmed := strings.TrimRight(line, " \t")
		for i := 0; i < len(trimmed); i++ {
			origPositions = append(origPositions, origOffset+i)
			intermediateBuilder.WriteByte(trimmed[i])
		}
		origOffset += len(line)
		if lineIdx < len(origLines)-1 {
			origPositions = append(origPositions, origOffset) // the \n
			intermediateBuilder.WriteByte('\n')
			origOffset++ // skip past \n in original
		}
	}
	// Sentinel: position past the end
	origPositions = append(origPositions, origOffset)

	intermediate := intermediateBuilder.String()

	// Step 2: Apply character replacements. Build mapping from final normalized
	// positions to intermediate positions.
	type replacement struct {
		from rune
		to   string
	}
	replacements := []replacement{
		// Smart single quotes → '
		{'\u2018', "'"}, {'\u2019', "'"}, {'\u201A', "'"}, {'\u201B', "'"},
		// Smart double quotes → "
		{'\u201C', "\""}, {'\u201D', "\""}, {'\u201E', "\""}, {'\u201F', "\""},
		// Various dashes → -
		{'\u2010', "-"}, {'\u2011', "-"}, {'\u2012', "-"}, {'\u2013', "-"},
		{'\u2014', "-"}, {'\u2015', "-"}, {'\u2212', "-"},
		// Special spaces → regular space
		{'\u00A0', " "}, {'\u2002', " "}, {'\u2003', " "}, {'\u2004', " "},
		{'\u2005', " "}, {'\u2006', " "}, {'\u2007', " "}, {'\u2008', " "},
		{'\u2009', " "}, {'\u200A', " "}, {'\u202F', " "}, {'\u205F', " "},
		{'\u3000', " "},
	}

	replMap := make(map[rune]string)
	for _, r := range replacements {
		replMap[r.from] = r.to
	}

	// interPositions[i] = intermediate byte offset corresponding to normalized byte offset i
	var interPositions []int
	normIdx := 0
	interIdx := 0
	for interIdx < len(intermediate) {
		r, runeSize := utf8.DecodeRuneInString(intermediate[interIdx:])
		if repl, ok := replMap[r]; ok {
			for i := 0; i < len(repl); i++ {
				interPositions = append(interPositions, interIdx)
				normIdx++
			}
			interIdx += runeSize
		} else {
			for i := 0; i < runeSize; i++ {
				interPositions = append(interPositions, interIdx+i)
				normIdx++
			}
			interIdx += runeSize
		}
	}
	// Sentinel
	interPositions = append(interPositions, interIdx)

	// Map normalized offsets → intermediate offsets → original offsets
	interStart := interPositions[normStart]
	interEnd := interPositions[normEnd]
	origStart = origPositions[interStart]
	origEnd = origPositions[interEnd]
	return origStart, origEnd
}
