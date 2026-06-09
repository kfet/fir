// Ported from: packages/coding-agent/src/core/tools/read.ts
// Upstream hash: 1caadb2e
package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai/core"
	"log/slog"
)

// ReadToolParams are the parameters for the read tool.
type ReadToolParams struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"` // 1-indexed line number
	Limit  *int   `json:"limit,omitempty"`
	IfHash string `json:"if_hash,omitempty"` // optional: confirm-unchanged opt-in
}

// ReadToolDetails contains details about a read result.
type ReadToolDetails struct {
	Truncation *TruncationResult `json:"truncation,omitempty"`
	// Hash is the content hash of the file (P2). Always set on a successful
	// text/image read so the model can later pass it back as if_hash.
	Hash string `json:"hash,omitempty"`
	// Unchanged is true when an if_hash matched and the full body was elided.
	Unchanged bool `json:"unchanged,omitempty"`
}

// readContentHash returns a short, stable content hash for if_hash comparison.
func readContentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// fileContentHash streams a file into the hash so we never hold the whole file
// in memory (matters for the partial-read path, which deliberately avoids
// loading large files).
func fileContentHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// unchangedStub is the tiny result returned when a supplied if_hash matches the
// current content hash: the model already holds the body and only needed to
// confirm the file is still current.
func unchangedStub(hash string) agent.AgentToolResult {
	return agent.AgentToolResult{
		Content: []core.ToolResultContent{{
			Type: "text",
			Text: fmt.Sprintf("[unchanged] content matches if_hash (hash: %s)", hash),
		}},
		Details: &ReadToolDetails{Hash: hash, Unchanged: true},
	}
}

// appendReadHash attaches the content hash to a read result: a model-visible
// trailing text block plus the structured detail.
func appendReadHash(res agent.AgentToolResult, hash string) agent.AgentToolResult {
	res.Content = append(res.Content, core.ToolResultContent{
		Type: "text",
		Text: fmt.Sprintf("[hash: %s]", hash),
	})
	switch d := res.Details.(type) {
	case *ReadToolDetails:
		d.Hash = hash
	case nil:
		res.Details = &ReadToolDetails{Hash: hash}
	}
	return res
}

// SupportedImageExtensions lists file extensions treated as images.
var SupportedImageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// NewReadTool creates the read tool for the given working directory.
func NewReadTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: core.Tool{
			Name: "read",
			Description: fmt.Sprintf(
				"Read the contents of a file. Supports text files and images (jpg, png, gif, webp). "+
					"Images are sent as attachments. For text files, output is truncated to %d lines or %dKB "+
					"(whichever is hit first). Use offset/limit for large files. When you need the full file, "+
					"continue with offset until complete. This is the preferred way to view file contents — "+
					"do not shell out to cat/sed/head/tail for this. "+
					"Each read returns a `hash`. If you already hold a file's contents and only need to confirm "+
					"it is still current, pass `if_hash` to get back `unchanged` cheaply instead of the full body. "+
					"Re-read freely whenever you actually need the content.",
				DefaultMaxLines, DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file to read (relative or absolute)",
					},
					"offset": map[string]any{
						"type":        "number",
						"description": "Line number to start reading from (1-indexed)",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum number of lines to read",
					},
					"if_hash": map[string]any{
						"type":        "string",
						"description": "Optional. For a whole-file read (no offset/limit): if you already hold this file's contents, pass the `hash` from your previous read. When it still matches, you get back a tiny `unchanged` stub instead of the full body; if it differs (your edit or any outside change), you get the full current content. Ignored for partial (offset/limit) reads.",
					},
				},
				"required": []string{"path"},
			},
		},
		Label: "read",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			path, _ := params["path"].(string)
			if path == "" {
				return agent.AgentToolResult{}, fmt.Errorf("path is required")
			}

			var offset *int
			if v, ok := params["offset"].(float64); ok {
				i := int(v)
				offset = &i
			}
			var limit *int
			if v, ok := params["limit"].(float64); ok {
				i := int(v)
				limit = &i
			}

			if ctx.Err() != nil {
				return agent.AgentToolResult{}, ctx.Err()
			}

			ifHash, _ := params["if_hash"].(string)
			return executeRead(path, cwd, offset, limit, ifHash)
		},
	}
}

// executeRead reads a file, attaches a content hash, and honours the optional
// if_hash confirm-unchanged opt-in (P2). The hash and if_hash apply to
// WHOLE-FILE reads only: a partial read (offset/limit) targets a slice, but the
// hash is a whole-file identity — honouring if_hash there could return
// "unchanged" for a slice the model has never seen, and hashing the whole file
// would defeat the partial-read path's large-file streaming. So partial reads
// carry no hash and ignore if_hash. The hash is recomputed fresh on every call,
// so outside changes invalidate it automatically — no staleness window, no
// "who changed it" question.
func executeRead(path, cwd string, offset, limit *int, ifHash string) (agent.AgentToolResult, error) {
	fullRead := offset == nil && limit == nil
	if fullRead {
		absolutePath := ResolveReadPath(path, cwd)
		if hash, herr := fileContentHash(absolutePath); herr == nil {
			if ifHash != "" && ifHash == hash {
				return unchangedStub(hash), nil
			}
			res, err := executeReadBase(path, cwd, offset, limit)
			if err != nil {
				return res, err
			}
			return appendReadHash(res, hash), nil
		}
		// File can't be hashed (missing/unreadable/dir) — fall through to the
		// base read so it returns the canonical not-found/error result.
	}
	return executeReadBase(path, cwd, offset, limit)
}

// executeReadBase reads a file and returns the result (no hash handling).
func executeReadBase(path, cwd string, offset, limit *int) (agent.AgentToolResult, error) {
	slog.Debug("read file", "path", path, "cwd", cwd)
	absolutePath := ResolveReadPath(path, cwd)

	// Check if file exists and is readable
	info, err := os.Stat(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("file not found: %s", path)
	}
	if info.IsDir() {
		return agent.AgentToolResult{}, fmt.Errorf("%s is a directory, not a file", path)
	}

	// Check if it's an image by extension
	ext := strings.ToLower(filepath.Ext(absolutePath))
	if mimeType, ok := SupportedImageExtensions[ext]; ok {
		return readImage(absolutePath, path, mimeType)
	}

	// Read as text. When offset or limit are set, avoid reading the entire
	// file into memory — stream only the lines we need plus enough extra for
	// truncation limits.
	if offset != nil || limit != nil {
		result, err := readTextPartial(absolutePath, path, offset, limit)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return result, nil
	}

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return applyReadFilters(path, string(data), offset, limit)
}

// readImage reads an image file, resizes if needed, and returns it as base64.
func readImage(absolutePath, displayPath, mimeType string) (agent.AgentToolResult, error) {
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to read image %s: %w", displayPath, err)
	}

	b64 := base64.StdEncoding.EncodeToString(data)

	// Resize image if needed (max 2000x2000, max 4.5MB)
	resized := ResizeImage(b64, mimeType, nil)
	textNote := fmt.Sprintf("Read image file [%s]", resized.MimeType)
	if dimNote := FormatDimensionNote(resized); dimNote != "" {
		textNote += "\n" + dimNote
	}

	return agent.AgentToolResult{
		Content: []core.ToolResultContent{
			{Type: "text", Text: textNote},
			{Type: "image", Data: resized.Data, MimeType: resized.MimeType},
		},
	}, nil
}

// ReadFileFn is a function that reads a file and returns its text content.
// Used for ACP client delegation.
type ReadFileFn func(ctx context.Context, path string) (string, error)

// readTextPartial reads only the needed lines from a file, avoiding loading
// the entire file into memory. It counts total lines by scanning the full file
// but only keeps the lines in the requested range in memory.
func readTextPartial(absolutePath, displayPath string, offset, limit *int) (agent.AgentToolResult, error) {
	f, err := os.Open(absolutePath)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to read file %s: %w", displayPath, err)
	}
	defer f.Close()

	startLine := 0 // 0-indexed
	if offset != nil {
		startLine = *offset - 1
		if startLine < 0 {
			startLine = 0
		}
	}

	// Determine how many lines we actually need to collect. We need enough
	// for the user limit (if any) and the truncation limits (max lines/bytes).
	maxNeeded := DefaultMaxLines
	if limit != nil && *limit < maxNeeded {
		maxNeeded = *limit
	}
	// Read one extra line so we can detect "more lines remaining" accurately.
	maxNeeded++

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // allow up to 1MB per line

	var collected []string
	lineNum := 0 // 0-indexed
	totalLines := 0
	bytesCollected := 0
	// We stop collecting once we have enough, but keep counting lines.
	collecting := true

	for scanner.Scan() {
		totalLines++
		if lineNum < startLine {
			lineNum++
			continue
		}
		if collecting {
			line := scanner.Text()
			collected = append(collected, line)
			bytesCollected += len(line) + 1 // +1 for newline
			lineNum++
			// Stop collecting once we have enough lines or bytes.
			if len(collected) >= maxNeeded || bytesCollected > DefaultMaxBytes {
				collecting = false
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("failed to read file %s: %w", displayPath, err)
	}

	if startLine >= totalLines {
		return agent.AgentToolResult{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", startLine+1, totalLines)
	}

	// Now we have only the needed lines; use applyReadFilters for consistent
	// truncation/formatting but build a synthetic "full file" that starts at
	// our offset so the function computes correct line numbers. We need to
	// tell it the total file lines too, so we reconstruct things carefully.
	//
	// Instead of applyReadFilters (which re-splits), do the formatting inline
	// for efficiency.
	return formatPartialRead(displayPath, collected, startLine, totalLines, offset, limit)
}

// formatPartialRead formats the output for a partial read, handling truncation
// and continuation messages. collected contains lines starting at startLine (0-indexed).
func formatPartialRead(path string, collected []string, startLine, totalLines int, offset, limit *int) (agent.AgentToolResult, error) {
	// Determine how many lines the user wants.
	wantLines := len(collected)
	if limit != nil && *limit < wantLines {
		wantLines = *limit
	}
	// Cap at DefaultMaxLines.
	if wantLines > DefaultMaxLines {
		wantLines = DefaultMaxLines
	}

	// Apply byte limit.
	byteCount := 0
	actualLines := 0
	for i := 0; i < wantLines && i < len(collected); i++ {
		lineBytes := len(collected[i])
		if i == 0 && lineBytes > DefaultMaxBytes {
			// First line exceeds byte limit.
			startLineDisplay := startLine + 1
			firstLineSize := FormatSize(lineBytes)
			outputText := fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
				startLineDisplay, firstLineSize, FormatSize(DefaultMaxBytes), startLineDisplay, path, DefaultMaxBytes)
			return agent.AgentToolResult{
				Content: []core.ToolResultContent{{Type: "text", Text: outputText}},
			}, nil
		}
		if byteCount+lineBytes+1 > DefaultMaxBytes && i > 0 {
			break
		}
		byteCount += lineBytes + 1
		actualLines++
	}

	output := strings.Join(collected[:actualLines], "\n")
	startLineDisplay := startLine + 1
	endLineDisplay := startLine + actualLines

	truncatedByLines := actualLines < wantLines || (limit == nil && actualLines < len(collected))
	truncatedByBytes := byteCount > DefaultMaxBytes

	if truncatedByBytes {
		nextOffset := endLineDisplay + 1
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
			startLineDisplay, endLineDisplay, totalLines, FormatSize(DefaultMaxBytes), nextOffset)
	} else if truncatedByLines || (limit == nil && actualLines >= DefaultMaxLines && endLineDisplay < totalLines) {
		nextOffset := endLineDisplay + 1
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
			startLineDisplay, endLineDisplay, totalLines, nextOffset)
	} else if limit != nil && endLineDisplay < totalLines {
		remaining := totalLines - endLineDisplay
		nextOffset := endLineDisplay + 1
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", remaining, nextOffset)
	}

	return agent.AgentToolResult{
		Content: []core.ToolResultContent{{Type: "text", Text: output}},
	}, nil
}

// applyReadFilters applies offset, limit, and truncation to already-loaded text content.
// This is the text-processing core of executeRead, extracted for reuse.
func applyReadFilters(path, textContent string, offset, limit *int) (agent.AgentToolResult, error) {
	allLines := strings.Split(textContent, "\n")
	totalFileLines := len(allLines)

	startLine := 0
	if offset != nil {
		startLine = *offset - 1
		if startLine < 0 {
			startLine = 0
		}
	}
	startLineDisplay := startLine + 1

	if startLine >= len(allLines) {
		return agent.AgentToolResult{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", *offset, len(allLines))
	}

	var selectedContent string
	var userLimitedLines *int
	if limit != nil {
		endLine := startLine + *limit
		if endLine > len(allLines) {
			endLine = len(allLines)
		}
		selectedContent = strings.Join(allLines[startLine:endLine], "\n")
		n := endLine - startLine
		userLimitedLines = &n
	} else {
		selectedContent = strings.Join(allLines[startLine:], "\n")
	}

	truncation := TruncateHead(selectedContent, TruncationOptions{})

	var outputText string
	var details *ReadToolDetails

	if truncation.FirstLineExceedsLimit {
		firstLineSize := FormatSize(len(allLines[startLine]))
		outputText = fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startLineDisplay, firstLineSize, FormatSize(DefaultMaxBytes), startLineDisplay, path, DefaultMaxBytes)
		details = &ReadToolDetails{Truncation: &truncation}
	} else if truncation.Truncated {
		endLineDisplay := startLineDisplay + truncation.OutputLines - 1
		nextOffset := endLineDisplay + 1
		outputText = truncation.Content
		if truncation.TruncatedBy == "lines" {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, FormatSize(DefaultMaxBytes), nextOffset)
		}
		details = &ReadToolDetails{Truncation: &truncation}
	} else if userLimitedLines != nil && startLine+*userLimitedLines < len(allLines) {
		remaining := len(allLines) - (startLine + *userLimitedLines)
		nextOffset := startLine + *userLimitedLines + 1
		outputText = truncation.Content
		outputText += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", remaining, nextOffset)
	} else {
		outputText = truncation.Content
	}

	result := agent.AgentToolResult{
		Content: []core.ToolResultContent{{Type: "text", Text: outputText}},
	}
	if details != nil {
		result.Details = details
	}
	return result, nil
}

// NewReadToolWithReader creates a read tool that delegates text file reads to readFn.
// Image files are still read locally (ACP clients don't expose binary file reading).
func NewReadToolWithReader(cwd string, readFn ReadFileFn) agent.AgentTool {
	t := NewReadTool(cwd)
	orig := t.Execute
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		path, _ := params["path"].(string)
		if path == "" {
			return agent.AgentToolResult{}, fmt.Errorf("path is required")
		}
		absolutePath := ResolveReadPath(path, cwd)
		// Delegate images to original (ACP has no binary read).
		ext := strings.ToLower(filepath.Ext(absolutePath))
		if _, isImage := SupportedImageExtensions[ext]; isImage {
			return orig(ctx, toolCallID, params, onUpdate)
		}
		// Delegate text reads to the provided function.
		content, err := readFn(ctx, absolutePath)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("failed to read %s: %w", path, err)
		}
		var offset *int
		if v, ok := params["offset"].(float64); ok {
			i := int(v)
			offset = &i
		}
		var limit *int
		if v, ok := params["limit"].(float64); ok {
			i := int(v)
			limit = &i
		}
		// Hash + if_hash apply to whole-file reads only (see executeRead).
		if offset == nil && limit == nil {
			hash := readContentHash([]byte(content))
			if ifHash, _ := params["if_hash"].(string); ifHash != "" && ifHash == hash {
				return unchangedStub(hash), nil
			}
			res, err := applyReadFilters(path, content, offset, limit)
			if err != nil {
				return res, err
			}
			return appendReadHash(res, hash), nil
		}
		return applyReadFilters(path, content, offset, limit)
	}
	return t
}
