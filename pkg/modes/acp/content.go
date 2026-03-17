// ACP content extraction and formatting.
// Split from helpers.go.
package acp

import (
	"regexp"
	"strings"

	"github.com/kfet/fir/pkg/ai"

	acpsdk "github.com/coder/acp-go-sdk"
)

// codeFenceRegexp matches pi's diff format code fences.
var codeFenceRegexp = regexp.MustCompile("(?m)^`{3,}")

// ExtractPromptContent extracts text and images from ACP content blocks.
func ExtractPromptContent(blocks []acpsdk.ContentBlock) (text string, images []ai.ImageContent) {
	var textParts []string
	for _, block := range blocks {
		if block.Text != nil {
			textParts = append(textParts, block.Text.Text)
		} else if block.Image != nil {
			images = append(images, ai.ImageContent{
				Type:     "image",
				MimeType: block.Image.MimeType,
				Data:     block.Image.Data,
			})
		} else if block.Resource != nil && block.Resource.Resource.TextResourceContents != nil {
			textParts = append(textParts, block.Resource.Resource.TextResourceContents.Text)
		} else if block.ResourceLink != nil && strings.HasPrefix(block.ResourceLink.Uri, "file://") {
			textParts = append(textParts, "@"+block.ResourceLink.Uri[7:])
		}
	}
	return strings.Join(textParts, "\n"), images
}

// MarkdownEscape wraps text in code fences, using enough backticks to avoid conflicts.
func MarkdownEscape(text string) string {
	fence := "```"
	for _, match := range codeFenceRegexp.FindAllString(text, -1) {
		for len(match) >= len(fence) {
			fence += "`"
		}
	}
	suffix := ""
	if !strings.HasSuffix(text, "\n") {
		suffix = "\n"
	}
	return fence + "\n" + text + suffix + fence
}

// extractResultText extracts text from a tool result's content array.
func extractResultText(result interface{}) string {
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return ""
	}
	contentArr, ok := resultMap["content"].([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range contentArr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "text" {
			if text, ok := m["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
