// Ported from: packages/ai/src/providers/google-shared.ts
// Upstream hash: 1caadb2e
package providers

import (
	"regexp"
	"strings"

	"github.com/kfet/tau/pkg/ai"
)

// --- Thinking support ---

// IsThinkingPart returns true if a part has thought: true.
func IsThinkingPart(thought bool) bool {
	return thought
}

// RetainThoughtSignature preserves the last non-empty signature within a streaming block.
func RetainThoughtSignature(existing, incoming string) string {
	if incoming != "" {
		return incoming
	}
	return existing
}

// base64SignaturePattern matches valid base64 strings.
var base64SignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/]+=*$`)

// IsValidThoughtSignature checks if a signature is valid base64 with proper padding.
func IsValidThoughtSignature(sig string) bool {
	if sig == "" {
		return false
	}
	if len(sig)%4 != 0 {
		return false
	}
	return base64SignaturePattern.MatchString(sig)
}

// ResolveThoughtSignature keeps signatures only from the same provider/model with valid base64.
func ResolveThoughtSignature(isSameProviderAndModel bool, signature string) string {
	if isSameProviderAndModel && IsValidThoughtSignature(signature) {
		return signature
	}
	return ""
}

// RequiresToolCallId returns true for models that need explicit tool call IDs in function calls/responses.
func RequiresToolCallId(modelID string) bool {
	return strings.HasPrefix(modelID, "claude-") || strings.HasPrefix(modelID, "gpt-oss-")
}

// NormalizeToolCallId normalizes a tool call ID for models that require it.
func NormalizeToolCallId(modelID, id string) string {
	if !RequiresToolCallId(modelID) {
		return id
	}
	// Replace non-alphanumeric/underscore/hyphen with underscore, truncate to 64
	result := make([]byte, 0, len(id))
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			result = append(result, byte(c))
		} else {
			result = append(result, '_')
		}
	}
	if len(result) > 64 {
		result = result[:64]
	}
	return string(result)
}

// --- Message conversion ---

// GoogleContent is the Gemini API content format.
type GoogleContent struct {
	Role  string           `json:"role"`
	Parts []GooglePart     `json:"parts"`
}

// GooglePart is a part in a Gemini content message.
type GooglePart struct {
	Text             string                 `json:"text,omitempty"`
	InlineData       *GoogleInlineData      `json:"inlineData,omitempty"`
	FunctionCall     *GoogleFunctionCall    `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionResp    `json:"functionResponse,omitempty"`
	Thought          *bool                  `json:"thought,omitempty"`
	ThoughtSignature string                 `json:"thoughtSignature,omitempty"`
}

// GoogleInlineData is inline image data.
type GoogleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GoogleFunctionCall is a function call in a Gemini response.
type GoogleFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
	ID   string         `json:"id,omitempty"`
}

// GoogleFunctionResp is a function response for Gemini.
type GoogleFunctionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	Parts    []GooglePart   `json:"parts,omitempty"`
	ID       string         `json:"id,omitempty"`
}

// ConvertGoogleMessages converts internal messages to Gemini Content[] format.
func ConvertGoogleMessages(model *ai.Model, ctx ai.Context) []GoogleContent {
	var contents []GoogleContent
	transformed := TransformMessages(ctx.Messages, model, func(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
		return NormalizeToolCallId(model.ID, id)
	})

	for _, msg := range transformed {
		if u := msg.AsUser(); u != nil {
			parts := convertUserParts(model, u)
			if len(parts) > 0 {
				contents = append(contents, GoogleContent{Role: "user", Parts: parts})
			}
		} else if a := msg.AsAssistant(); a != nil {
			parts := convertAssistantParts(model, a)
			if len(parts) > 0 {
				contents = append(contents, GoogleContent{Role: "model", Parts: parts})
			}
		} else if tr := msg.AsToolResult(); tr != nil {
			contents = convertToolResultParts(model, tr, contents)
		}
	}

	return contents
}

func convertUserParts(model *ai.Model, u *ai.UserMessage) []GooglePart {
	switch c := u.Content.(type) {
	case string:
		if strings.TrimSpace(c) == "" {
			return nil
		}
		return []GooglePart{{Text: SanitizeSurrogates(c)}}
	case []any:
		var parts []GooglePart
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				switch m["type"] {
				case "text":
					if text, ok := m["text"].(string); ok {
						parts = append(parts, GooglePart{Text: SanitizeSurrogates(text)})
					}
				case "image":
					if hasInput(model, "image") {
						data, _ := m["data"].(string)
						mime, _ := m["mimeType"].(string)
						parts = append(parts, GooglePart{
							InlineData: &GoogleInlineData{MimeType: mime, Data: data},
						})
					}
				}
			}
		}
		return parts
	}
	return nil
}

func convertAssistantParts(model *ai.Model, a *ai.AssistantMessage) []GooglePart {
	var parts []GooglePart
	isSameProviderAndModel := a.Provider == model.Provider && a.Model == model.ID

	for _, block := range a.Content {
		switch {
		case block.IsText():
			text := block.Text.Text
			if strings.TrimSpace(text) == "" {
				continue
			}
			sig := ResolveThoughtSignature(isSameProviderAndModel, block.Text.TextSignature)
			p := GooglePart{Text: SanitizeSurrogates(text)}
			if sig != "" {
				p.ThoughtSignature = sig
			}
			parts = append(parts, p)

		case block.IsThinking():
			thinking := block.Thinking.Thinking
			if strings.TrimSpace(thinking) == "" {
				continue
			}
			if isSameProviderAndModel {
				sig := ResolveThoughtSignature(isSameProviderAndModel, block.Thinking.ThinkingSignature)
				t := true
				p := GooglePart{Text: SanitizeSurrogates(thinking), Thought: &t}
				if sig != "" {
					p.ThoughtSignature = sig
				}
				parts = append(parts, p)
			} else {
				parts = append(parts, GooglePart{Text: SanitizeSurrogates(thinking)})
			}

		case block.IsToolCall():
			tc := block.ToolCall
			sig := ResolveThoughtSignature(isSameProviderAndModel, tc.ThoughtSignature)

			isGemini3 := strings.Contains(strings.ToLower(model.ID), "gemini-3")
			if isGemini3 && sig == "" {
				// Convert unsigned function calls to text for Gemini 3
				parts = append(parts, GooglePart{
					Text: "[Historical context: a different model called tool \"" + tc.Name + "\". Do not mimic this format - use proper function calling.]",
				})
			} else {
				fc := &GoogleFunctionCall{
					Name: tc.Name,
					Args: tc.Arguments,
				}
				if RequiresToolCallId(model.ID) {
					fc.ID = tc.ID
				}
				p := GooglePart{FunctionCall: fc}
				if sig != "" {
					p.ThoughtSignature = sig
				}
				parts = append(parts, p)
			}
		}
	}

	return parts
}

func convertToolResultParts(model *ai.Model, tr *ai.ToolResultMessage, contents []GoogleContent) []GoogleContent {
	var textParts []string
	var imageParts []GooglePart
	for _, c := range tr.Content {
		if c.IsText() {
			textParts = append(textParts, c.Text)
		} else if c.IsImage() && hasInput(model, "image") {
			imageParts = append(imageParts, GooglePart{
				InlineData: &GoogleInlineData{MimeType: c.MimeType, Data: c.Data},
			})
		}
	}

	textResult := SanitizeSurrogates(strings.Join(textParts, "\n"))
	hasText := len(textResult) > 0
	hasImages := len(imageParts) > 0

	responseValue := textResult
	if !hasText && hasImages {
		responseValue = "(see attached image)"
	}

	supportsMultimodal := strings.Contains(model.ID, "gemini-3")

	respKey := "output"
	if tr.IsError {
		respKey = "error"
	}

	fr := &GoogleFunctionResp{
		Name:     tr.ToolName,
		Response: map[string]any{respKey: responseValue},
	}
	if hasImages && supportsMultimodal {
		fr.Parts = imageParts
	}
	if RequiresToolCallId(model.ID) {
		fr.ID = tr.ToolCallID
	}

	frPart := GooglePart{FunctionResponse: fr}

	// Merge tool results into existing user turn if present
	if len(contents) > 0 {
		last := &contents[len(contents)-1]
		if last.Role == "user" && len(last.Parts) > 0 && last.Parts[0].FunctionResponse != nil {
			last.Parts = append(last.Parts, frPart)
			// Add non-multimodal images in a separate user message
			if hasImages && !supportsMultimodal {
				imgParts := append([]GooglePart{{Text: "Tool result image:"}}, imageParts...)
				contents = append(contents, GoogleContent{Role: "user", Parts: imgParts})
			}
			return contents
		}
	}

	contents = append(contents, GoogleContent{Role: "user", Parts: []GooglePart{frPart}})
	if hasImages && !supportsMultimodal {
		imgParts := append([]GooglePart{{Text: "Tool result image:"}}, imageParts...)
		contents = append(contents, GoogleContent{Role: "user", Parts: imgParts})
	}
	return contents
}

// --- Tools ---

// ConvertGoogleTools converts tools to Gemini function declarations format.
func ConvertGoogleTools(tools []ai.Tool, useParameters bool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	var decls []map[string]any
	for _, tool := range tools {
		decl := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
		}
		if useParameters {
			decl["parameters"] = tool.Parameters
		} else {
			decl["parametersJsonSchema"] = tool.Parameters
		}
		decls = append(decls, decl)
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

// MapGoogleToolChoice maps a tool choice string to Gemini FunctionCallingConfigMode.
func MapGoogleToolChoice(choice string) string {
	switch choice {
	case "auto":
		return "AUTO"
	case "none":
		return "NONE"
	case "any":
		return "ANY"
	default:
		return "AUTO"
	}
}

// MapGoogleStopReason maps a Gemini FinishReason string to our StopReason.
func MapGoogleStopReason(reason string) ai.StopReason {
	switch reason {
	case "STOP":
		return ai.StopReasonStop
	case "MAX_TOKENS":
		return ai.StopReasonLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII",
		"FINISH_REASON_UNSPECIFIED", "OTHER", "LANGUAGE", "MALFORMED_FUNCTION_CALL",
		"UNEXPECTED_TOOL_CALL", "NO_IMAGE",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION", "IMAGE_OTHER":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

// SanitizeSurrogates replaces unpaired surrogates with the replacement character.
func SanitizeSurrogates(s string) string {
	// Go strings are valid UTF-8, so unpaired surrogates shouldn't normally occur.
	// But we handle them defensively by replacing the replacement character if present.
	return strings.Map(func(r rune) rune {
		if r == 0xFFFD {
			return 0xFFFD // Already replacement character
		}
		return r
	}, s)
}

// hasInput checks if a model supports a given input type.
func hasInput(model *ai.Model, inputType string) bool {
	for _, inp := range model.Input {
		if inp == inputType {
			return true
		}
	}
	return false
}
