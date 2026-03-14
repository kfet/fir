// Ported from: packages/ai/src/providers/openai-responses-shared.ts
// Upstream hash: f04d9bc4
package providers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/jsonparse"
)

// responsesSSEProcessor processes SSE events for the OpenAI Responses API format.
// It is shared between openai_responses.go and azure_openai_responses.go.
type responsesSSEProcessor struct {
	output  *ai.AssistantMessage
	stream  *ai.AssistantMessageEventStream
	model   *ai.Model
	current *responsesItemState
}

type responsesItemState struct {
	itemType    string
	id          string
	callID      string
	name        string
	contentIdx  int
	partialJSON string
}

// processEvent processes a single SSE event in the OpenAI Responses format.
// Returns (done, error) — done=true means stream is finished.
func (p *responsesSSEProcessor) processEvent(data string) (bool, error) {
	if data == "" || data == "[DONE]" {
		return false, nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return false, nil // skip unparseable events
	}

	eventType, _ := raw["type"].(string)

	switch eventType {
	case "response.output_item.added":
		p.handleOutputItemAdded(raw)

	case "response.output_text.delta":
		p.handleTextDelta(raw)

	case "response.refusal.delta":
		p.handleRefusalDelta(raw)

	case "response.reasoning_summary_text.delta":
		p.handleReasoningDelta(raw)

	case "response.reasoning_summary_part.done":
		p.handleReasoningPartDone()

	case "response.function_call_arguments.delta":
		p.handleFunctionCallArgsDelta(raw)

	case "response.function_call_arguments.done":
		p.handleFunctionCallArgsDone(raw)

	case "response.output_item.done":
		p.handleOutputItemDone(raw)

	case "response.completed":
		p.handleResponseCompleted(raw)

	case "error":
		code, _ := raw["code"].(string)
		message, _ := raw["message"].(string)
		return true, fmt.Errorf("Error Code %s: %s", code, message)

	case "response.failed":
		respRaw, _ := raw["response"].(map[string]any)
		var msg string
		if respRaw != nil {
			if errRaw, ok := respRaw["error"].(map[string]any); ok && errRaw != nil {
				code, _ := errRaw["code"].(string)
				errMsg, _ := errRaw["message"].(string)
				if code == "" {
					code = "unknown"
				}
				if errMsg == "" {
					errMsg = "no message"
				}
				msg = fmt.Sprintf("%s: %s", code, errMsg)
			} else if details, ok := respRaw["incomplete_details"].(map[string]any); ok && details != nil {
				reason, _ := details["reason"].(string)
				if reason != "" {
					msg = fmt.Sprintf("incomplete: %s", reason)
				}
			}
		}
		if msg == "" {
			msg = "Unknown error (no error details in response)"
		}
		return true, fmt.Errorf("%s", msg)
	}

	return false, nil
}

func (p *responsesSSEProcessor) handleOutputItemAdded(raw map[string]any) {
	itemRaw, _ := raw["item"].(map[string]any)
	if itemRaw == nil {
		return
	}
	iType, _ := itemRaw["type"].(string)

	switch iType {
	case "message":
		idx := len(p.output.Content)
		p.output.Content = append(p.output.Content, ai.NewTextContent(""))
		p.current = &responsesItemState{
			itemType:   "message",
			id:         jsonString(itemRaw, "id"),
			contentIdx: idx,
		}
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextStart,
			ContentIndex: idx,
			Partial:      p.output,
		})

	case "reasoning":
		idx := len(p.output.Content)
		p.output.Content = append(p.output.Content, ai.NewThinkingContent(""))
		p.current = &responsesItemState{
			itemType:   "reasoning",
			contentIdx: idx,
		}
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventThinkingStart,
			ContentIndex: idx,
			Partial:      p.output,
		})

	case "function_call":
		idx := len(p.output.Content)
		callID, _ := itemRaw["call_id"].(string)
		fcID, _ := itemRaw["id"].(string)
		fcName, _ := itemRaw["name"].(string)
		combinedID := callID
		if fcID != "" {
			combinedID = callID + "|" + fcID
		}
		p.output.Content = append(p.output.Content, ai.NewToolCallContent(combinedID, fcName, map[string]any{}))
		p.current = &responsesItemState{
			itemType:    "function_call",
			id:          fcID,
			callID:      callID,
			name:        fcName,
			contentIdx:  idx,
			partialJSON: "",
		}
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventToolcallStart,
			ContentIndex: idx,
			Partial:      p.output,
		})

	case "shell_call":
		// Server-side hosted shell — show commands being executed.
		idx := len(p.output.Content)
		text := formatShellCall(itemRaw)
		p.output.Content = append(p.output.Content, ai.NewTextContent(text))
		// Store the raw item JSON for multi-turn replay.
		itemJSON, _ := json.Marshal(itemRaw)
		p.current = &responsesItemState{
			itemType:    "shell_call",
			contentIdx:  idx,
			partialJSON: string(itemJSON),
		}
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextStart,
			ContentIndex: idx,
			Partial:      p.output,
		})
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextDelta,
			ContentIndex: idx,
			Delta:        text,
			Partial:      p.output,
		})

	case "shell_call_output":
		// Server-side hosted shell output.
		idx := len(p.output.Content)
		text := formatShellCallOutput(itemRaw)
		p.output.Content = append(p.output.Content, ai.NewTextContent(text))
		itemJSON, _ := json.Marshal(itemRaw)
		p.current = &responsesItemState{
			itemType:    "shell_call_output",
			contentIdx:  idx,
			partialJSON: string(itemJSON),
		}
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextStart,
			ContentIndex: idx,
			Partial:      p.output,
		})
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextDelta,
			ContentIndex: idx,
			Delta:        text,
			Partial:      p.output,
		})
	}
}

func (p *responsesSSEProcessor) handleTextDelta(raw map[string]any) {
	if p.current == nil || p.current.itemType != "message" {
		return
	}
	delta, _ := raw["delta"].(string)
	if delta == "" {
		return
	}
	c := p.output.Content[p.current.contentIdx]
	c.Text.Text += delta
	p.output.Content[p.current.contentIdx] = c
	p.stream.Push(ai.AssistantMessageEvent{
		Type:         ai.EventTextDelta,
		ContentIndex: p.current.contentIdx,
		Delta:        delta,
		Partial:      p.output,
	})
}

func (p *responsesSSEProcessor) handleRefusalDelta(raw map[string]any) {
	if p.current == nil || p.current.itemType != "message" {
		return
	}
	delta, _ := raw["delta"].(string)
	if delta == "" {
		return
	}
	c := p.output.Content[p.current.contentIdx]
	c.Text.Text += delta
	p.output.Content[p.current.contentIdx] = c
	p.stream.Push(ai.AssistantMessageEvent{
		Type:         ai.EventTextDelta,
		ContentIndex: p.current.contentIdx,
		Delta:        delta,
		Partial:      p.output,
	})
}

func (p *responsesSSEProcessor) handleReasoningDelta(raw map[string]any) {
	if p.current == nil || p.current.itemType != "reasoning" {
		return
	}
	delta, _ := raw["delta"].(string)
	if delta == "" {
		return
	}
	c := p.output.Content[p.current.contentIdx]
	c.Thinking.Thinking += delta
	p.output.Content[p.current.contentIdx] = c
	p.stream.Push(ai.AssistantMessageEvent{
		Type:         ai.EventThinkingDelta,
		ContentIndex: p.current.contentIdx,
		Delta:        delta,
		Partial:      p.output,
	})
}

func (p *responsesSSEProcessor) handleReasoningPartDone() {
	if p.current == nil || p.current.itemType != "reasoning" {
		return
	}
	c := p.output.Content[p.current.contentIdx]
	c.Thinking.Thinking += "\n\n"
	p.output.Content[p.current.contentIdx] = c
	p.stream.Push(ai.AssistantMessageEvent{
		Type:         ai.EventThinkingDelta,
		ContentIndex: p.current.contentIdx,
		Delta:        "\n\n",
		Partial:      p.output,
	})
}

func (p *responsesSSEProcessor) handleFunctionCallArgsDelta(raw map[string]any) {
	if p.current == nil || p.current.itemType != "function_call" {
		return
	}
	delta, _ := raw["delta"].(string)
	if delta == "" {
		return
	}
	p.current.partialJSON += delta
	parsed := jsonparse.ParseStreamingJSON(p.current.partialJSON)
	c := p.output.Content[p.current.contentIdx]
	c.ToolCall.Arguments = parsed
	p.output.Content[p.current.contentIdx] = c
	p.stream.Push(ai.AssistantMessageEvent{
		Type:         ai.EventToolcallDelta,
		ContentIndex: p.current.contentIdx,
		Delta:        delta,
		Partial:      p.output,
	})
}

func (p *responsesSSEProcessor) handleFunctionCallArgsDone(raw map[string]any) {
	if p.current == nil || p.current.itemType != "function_call" {
		return
	}
	argsStr, _ := raw["arguments"].(string)
	if argsStr == "" {
		return
	}
	p.current.partialJSON = argsStr
	parsed := jsonparse.ParseStreamingJSON(argsStr)
	c := p.output.Content[p.current.contentIdx]
	c.ToolCall.Arguments = parsed
	p.output.Content[p.current.contentIdx] = c
}

func (p *responsesSSEProcessor) handleOutputItemDone(raw map[string]any) {
	if p.current == nil {
		return
	}
	itemRaw, _ := raw["item"].(map[string]any)
	iType := ""
	if itemRaw != nil {
		iType, _ = itemRaw["type"].(string)
	}

	switch iType {
	case "message":
		idx := p.current.contentIdx
		finalText := p.output.Content[idx].Text.Text
		if itemRaw != nil {
			if contents, ok := itemRaw["content"].([]any); ok {
				var textParts []string
				for _, c := range contents {
					cm, _ := c.(map[string]any)
					if cm != nil {
						ct, _ := cm["type"].(string)
						if ct == "output_text" {
							t, _ := cm["text"].(string)
							textParts = append(textParts, t)
						} else if ct == "refusal" {
							r, _ := cm["refusal"].(string)
							textParts = append(textParts, r)
						}
					}
				}
				if len(textParts) > 0 {
					finalText = strings.Join(textParts, "")
				}
			}
		}
		c := p.output.Content[idx]
		c.Text.Text = finalText
		if id, ok := itemRaw["id"].(string); ok {
			phase, _ := itemRaw["phase"].(string)
			c.Text.TextSignature = encodeTextSignatureV1(id, phase)
		}
		p.output.Content[idx] = c
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextEnd,
			ContentIndex: idx,
			Content:      finalText,
			Partial:      p.output,
		})

	case "reasoning":
		idx := p.current.contentIdx
		finalThinking := p.output.Content[idx].Thinking.Thinking
		if sigJSON, err := json.Marshal(itemRaw); err == nil {
			c := p.output.Content[idx]
			c.Thinking.ThinkingSignature = string(sigJSON)
			p.output.Content[idx] = c
		}
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventThinkingEnd,
			ContentIndex: idx,
			Content:      finalThinking,
			Partial:      p.output,
		})

	case "function_call":
		idx := p.current.contentIdx
		if argsStr, ok := itemRaw["arguments"].(string); ok && argsStr != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
				c := p.output.Content[idx]
				c.ToolCall.Arguments = args
				p.output.Content[idx] = c
			}
		}
		tc := p.output.Content[idx].ToolCall
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventToolcallEnd,
			ContentIndex: idx,
			ToolCall:     tc,
			Partial:      p.output,
		})

	case "shell_call":
		idx := p.current.contentIdx
		// Update text with final item data and store raw JSON for multi-turn replay.
		text := formatShellCall(itemRaw)
		c := p.output.Content[idx]
		c.Text.Text = text
		if itemJSON, err := json.Marshal(itemRaw); err == nil {
			c.Text.TextSignature = "shell:" + string(itemJSON)
		}
		p.output.Content[idx] = c
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextEnd,
			ContentIndex: idx,
			Content:      text,
			Partial:      p.output,
		})

	case "shell_call_output":
		idx := p.current.contentIdx
		text := formatShellCallOutput(itemRaw)
		c := p.output.Content[idx]
		c.Text.Text = text
		if itemJSON, err := json.Marshal(itemRaw); err == nil {
			c.Text.TextSignature = "shell_output:" + string(itemJSON)
		}
		p.output.Content[idx] = c
		p.stream.Push(ai.AssistantMessageEvent{
			Type:         ai.EventTextEnd,
			ContentIndex: idx,
			Content:      text,
			Partial:      p.output,
		})
	}
	p.current = nil
}

func (p *responsesSSEProcessor) handleResponseCompleted(raw map[string]any) {
	respRaw, _ := raw["response"].(map[string]any)
	if respRaw == nil {
		return
	}
	if usageRaw, ok := respRaw["usage"].(map[string]any); ok {
		cachedTokens := 0
		if details, ok := usageRaw["input_tokens_details"].(map[string]any); ok {
			cachedTokens = jsonInt(details, "cached_tokens")
		}
		inputTokens := jsonInt(usageRaw, "input_tokens")
		p.output.Usage.Input = inputTokens - cachedTokens
		p.output.Usage.Output = jsonInt(usageRaw, "output_tokens")
		p.output.Usage.CacheRead = cachedTokens
		p.output.Usage.TotalTokens = jsonInt(usageRaw, "total_tokens")
		ai.CalculateCost(p.model, &p.output.Usage)
	}

	status, _ := respRaw["status"].(string)
	p.output.StopReason = mapOpenAIResponsesStatus(status)

	for _, c := range p.output.Content {
		if c.IsToolCall() {
			if p.output.StopReason == ai.StopReasonStop {
				p.output.StopReason = ai.StopReasonToolUse
			}
			break
		}
	}
}

// processResponsesSSEStream processes an SSE event stream using the shared processor.
// Returns an error if the stream encounters an error, or nil on success.
func processResponsesSSEStream(proc *responsesSSEProcessor, sseEvents <-chan SSEEvent, sseErr <-chan error) error {
	for evt := range sseEvents {
		done, err := proc.processEvent(evt.Data)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}

	// Check for SSE-level errors
	select {
	case err := <-sseErr:
		if err != nil {
			return err
		}
	default:
	}

	return nil
}

// responsesToolCallProviders is the set of providers whose tool call IDs need normalization.
var responsesToolCallProviders = map[string]bool{
	"openai":                 true,
	"openai-codex":           true,
	"opencode":               true,
	"azure-openai-responses": true,
}

// normalizeResponsesToolCallID normalizes tool call IDs for the OpenAI Responses API.
// IDs with "|" are split into callId|itemId and each part is sanitized.
func normalizeResponsesToolCallID(id string, model *ai.Model, _ *ai.AssistantMessage) string {
	if !responsesToolCallProviders[string(model.Provider)] {
		return id
	}
	if !strings.Contains(id, "|") {
		return id
	}
	parts := strings.SplitN(id, "|", 2)
	callID := sanitizeIDChars(parts[0])
	itemID := sanitizeIDChars(parts[1])

	// OpenAI Responses API requires item id to start with "fc"
	if !strings.HasPrefix(itemID, "fc") {
		itemID = "fc_" + itemID
	}

	// Truncate to 64 chars
	if len(callID) > 64 {
		callID = callID[:64]
	}
	if len(itemID) > 64 {
		itemID = itemID[:64]
	}

	// Strip trailing underscores (OpenAI Codex rejects them)
	callID = strings.TrimRight(callID, "_")
	itemID = strings.TrimRight(itemID, "_")

	return callID + "|" + itemID
}

// sanitizeIDChars replaces non-alphanumeric characters (except - and _) with _.
func sanitizeIDChars(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// convertResponsesTools converts tools to OpenAI Responses API format.
func convertResponsesTools(tools []ai.Tool, strict bool) []map[string]any {
	var result []map[string]any
	for _, tool := range tools {
		result = append(result, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
			"strict":      strict,
		})
	}
	return result
}

// shortHash generates a fast deterministic hash for shortening long strings.
func shortHash(s string) string {
	h1 := uint32(0xdeadbeef)
	h2 := uint32(0x41c6ce57)
	for i := 0; i < len(s); i++ {
		ch := uint32(s[i])
		h1 = (h1 ^ ch) * 2654435761
		h2 = (h2 ^ ch) * 1597334677
	}
	h1 = ((h1 ^ (h1 >> 16)) * 2246822507) ^ ((h2 ^ (h2 >> 13)) * 3266489909)
	h2 = ((h2 ^ (h2 >> 16)) * 2246822507) ^ ((h1 ^ (h1 >> 13)) * 3266489909)
	return fmt.Sprintf("%s%s", uint32ToBase36(h2), uint32ToBase36(h1))
}

func uint32ToBase36(n uint32) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var result []byte
	for n > 0 {
		result = append([]byte{digits[n%36]}, result...)
		n /= 36
	}
	return string(result)
}

// --- TextSignatureV1 encoding/parsing ---

// encodeTextSignatureV1 encodes an ID and optional phase into a JSON string.
func encodeTextSignatureV1(id string, phase string) string {
	m := map[string]any{"v": 1, "id": id}
	if phase != "" {
		m["phase"] = phase
	}
	b, _ := json.Marshal(m)
	return string(b)
}

type parsedTextSignature struct {
	ID    string
	Phase string
}

// parseTextSignature parses a TextSignatureV1 JSON string, or returns the raw string as legacy ID.
func parseTextSignature(sig string) *parsedTextSignature {
	if sig == "" {
		return nil
	}
	if strings.HasPrefix(sig, "{") {
		var parsed struct {
			V     int    `json:"v"`
			ID    string `json:"id"`
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal([]byte(sig), &parsed); err == nil && parsed.V == 1 && parsed.ID != "" {
			if parsed.Phase == "commentary" || parsed.Phase == "final_answer" {
				return &parsedTextSignature{ID: parsed.ID, Phase: parsed.Phase}
			}
			return &parsedTextSignature{ID: parsed.ID}
		}
	}
	return &parsedTextSignature{ID: sig}
}

// formatShellCall formats a shell_call item as readable text showing the commands.
func formatShellCall(item map[string]any) string {
	var b strings.Builder
	b.WriteString("[shell] ")
	if action, ok := item["action"].(map[string]any); ok {
		if cmds, ok := action["commands"].([]any); ok {
			for i, cmd := range cmds {
				if i > 0 {
					b.WriteString(" && ")
				}
				if s, ok := cmd.(string); ok {
					b.WriteString(s)
				}
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

// formatShellCallOutput formats a shell_call_output item as readable text.
func formatShellCallOutput(item map[string]any) string {
	var b strings.Builder
	if output, ok := item["output"].([]any); ok {
		for _, entry := range output {
			em, _ := entry.(map[string]any)
			if em == nil {
				continue
			}
			if stdout, ok := em["stdout"].(string); ok && stdout != "" {
				b.WriteString(stdout)
			}
			if stderr, ok := em["stderr"].(string); ok && stderr != "" {
				b.WriteString(stderr)
			}
		}
	}
	// Fallback: single-string output field.
	if b.Len() == 0 {
		if out, ok := item["output"].(string); ok {
			b.WriteString(out)
		}
	}
	return b.String()
}
