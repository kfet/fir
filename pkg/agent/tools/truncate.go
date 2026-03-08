// Ported from: packages/coding-agent/src/core/tools/truncate.ts
// Upstream hash: 1caadb2e
package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Default truncation limits.
const (
	DefaultMaxLines      = 2000
	DefaultMaxBytes      = 50 * 1024 // 50KB
	GrepMaxLineLength    = 500       // Max chars per grep match line
)

// TruncationResult describes the outcome of a truncation operation.
type TruncationResult struct {
	Content              string // The (possibly truncated) content
	Truncated            bool   // Whether truncation occurred
	TruncatedBy          string // "lines", "bytes", or "" if not truncated
	TotalLines           int    // Total lines in original content
	TotalBytes           int    // Total bytes in original content
	OutputLines          int    // Lines in truncated output
	OutputBytes          int    // Bytes in truncated output
	LastLinePartial      bool   // Whether the first line (tail) was partially truncated
	FirstLineExceedsLimit bool  // Whether the first line exceeds the byte limit (head)
	MaxLines             int    // The max lines limit applied
	MaxBytes             int    // The max bytes limit applied
}

// TruncationOptions configures truncation limits.
type TruncationOptions struct {
	MaxLines int // 0 means use DefaultMaxLines
	MaxBytes int // 0 means use DefaultMaxBytes
}

func (o TruncationOptions) maxLines() int {
	if o.MaxLines > 0 {
		return o.MaxLines
	}
	return DefaultMaxLines
}

func (o TruncationOptions) maxBytes() int {
	if o.MaxBytes > 0 {
		return o.MaxBytes
	}
	return DefaultMaxBytes
}

// FormatSize formats bytes as human-readable size.
func FormatSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	} else {
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}

// TruncateHead truncates content from the head (keeps first N lines/bytes).
// Suitable for file reads where you want to see the beginning.
// Never returns partial lines. If the first line exceeds the byte limit,
// returns empty content with FirstLineExceedsLimit=true.
func TruncateHead(content string, opts TruncationOptions) TruncationResult {
	maxLines := opts.maxLines()
	maxBytes := opts.maxBytes()

	totalBytes := len(content)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// No truncation needed
	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	// Check if first line alone exceeds byte limit
	firstLineBytes := len(lines[0])
	if firstLineBytes > maxBytes {
		return TruncationResult{
			Content:               "",
			Truncated:             true,
			TruncatedBy:           "bytes",
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           0,
			OutputBytes:           0,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	// Collect complete lines that fit
	var outputLines []string
	outputBytesCount := 0
	truncatedBy := "lines"

	for i := 0; i < len(lines) && i < maxLines; i++ {
		lineBytes := len(lines[i])
		if i > 0 {
			lineBytes++ // +1 for newline
		}

		if outputBytesCount+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}

		outputLines = append(outputLines, lines[i])
		outputBytesCount += lineBytes
	}

	// If we exited due to line limit
	if len(outputLines) >= maxLines && outputBytesCount <= maxBytes {
		truncatedBy = "lines"
	}

	outputContent := strings.Join(outputLines, "\n")
	finalOutputBytes := len(outputContent)

	return TruncationResult{
		Content:     outputContent,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(outputLines),
		OutputBytes: finalOutputBytes,
		MaxLines:    maxLines,
		MaxBytes:    maxBytes,
	}
}

// TruncateTail truncates content from the tail (keeps last N lines/bytes).
// Suitable for bash output where you want to see the end (errors, final results).
// May return partial first line if the last line of original content exceeds the byte limit.
func TruncateTail(content string, opts TruncationOptions) TruncationResult {
	maxLines := opts.maxLines()
	maxBytes := opts.maxBytes()

	totalBytes := len(content)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// No truncation needed
	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	// Work backwards from the end
	var outputLines []string
	outputBytesCount := 0
	truncatedBy := "lines"
	lastLinePartial := false

	for i := len(lines) - 1; i >= 0 && len(outputLines) < maxLines; i-- {
		lineBytes := len(lines[i])
		if len(outputLines) > 0 {
			lineBytes++ // +1 for newline
		}

		if outputBytesCount+lineBytes > maxBytes {
			truncatedBy = "bytes"
			// Edge case: if we haven't added ANY lines yet, take the end of this line (partial)
			if len(outputLines) == 0 {
				truncatedLine := truncateStringToBytesFromEnd(lines[i], maxBytes)
				outputLines = append(outputLines, truncatedLine)
				outputBytesCount = len(truncatedLine)
				lastLinePartial = true
			}
			break
		}

		// Prepend
		outputLines = append([]string{lines[i]}, outputLines...)
		outputBytesCount += lineBytes
	}

	// If we exited due to line limit
	if len(outputLines) >= maxLines && outputBytesCount <= maxBytes {
		truncatedBy = "lines"
	}

	outputContent := strings.Join(outputLines, "\n")
	finalOutputBytes := len(outputContent)

	return TruncationResult{
		Content:         outputContent,
		Truncated:       true,
		TruncatedBy:     truncatedBy,
		TotalLines:      totalLines,
		TotalBytes:      totalBytes,
		OutputLines:     len(outputLines),
		OutputBytes:     finalOutputBytes,
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines,
		MaxBytes:        maxBytes,
	}
}

// truncateStringToBytesFromEnd truncates a string to fit within maxBytes, keeping the end.
// Handles multi-byte UTF-8 characters correctly.
func truncateStringToBytesFromEnd(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}

	// Start from the end, skip back maxBytes
	start := len(s) - maxBytes

	// Find a valid UTF-8 boundary (not in the middle of a multi-byte char)
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}

	return s[start:]
}

// TruncateLine truncates a single line to maxChars, adding a [truncated] suffix.
// Used for grep match lines.
func TruncateLine(line string, maxChars int) (text string, wasTruncated bool) {
	if maxChars <= 0 {
		maxChars = GrepMaxLineLength
	}
	runes := []rune(line)
	if len(runes) <= maxChars {
		return line, false
	}
	return string(runes[:maxChars]) + "... [truncated]", true
}
