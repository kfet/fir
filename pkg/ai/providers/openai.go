// Ported from: packages/ai/src/providers/openai-completions.ts
// Upstream hash: 41039e8d
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/ai/jsonparse"
	"github.com/kfet/fir/pkg/ai/ratelimit"
	firlog "github.com/kfet/fir/pkg/log"
)

// --- SSE types for OpenAI Chat Completions API ---

type openaiChunk struct {
	ID      string         `json:"id"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage"`
}

type openaiChoice struct {
	Index        int          `json:"index"`
	Delta        openaiDelta  `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
	Usage        *openaiUsage `json:"usage"`
}

type openaiDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          *string          `json:"content"`
	ReasoningContent *string          `json:"reasoning_content"`
	Reasoning        *string          `json:"reasoning"`
	ReasoningText    *string          `json:"reasoning_text"`
	ToolCalls        []openaiToolCall `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	Index    int                 `json:"index"`
	ID       string              `json:"id,omitempty"`
	Type     string              `json:"type,omitempty"`
	Function *openaiToolCallFunc `json:"function,omitempty"`
}

type openaiToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// --- Block tracking ---

type openaiBlock struct {
	contentType string // "text", "thinking", "toolCall"
	partialArgs string
}

// --- Compat detection (mirrors TS detectCompat/getCompat) ---

// resolvedCompat holds fully resolved compatibility settings for an OpenAI-compatible provider.
type resolvedCompat struct {
	SupportsStore                    bool
	SupportsDeveloperRole            bool
	SupportsReasoningEffort          bool
	ReasoningEffortMap               map[string]string
	SupportsUsageInStreaming         bool
	MaxTokensField                   ai.MaxTokensField
	RequiresToolResultName           bool
	RequiresAssistantAfterToolResult bool
	RequiresThinkingAsText           bool
	ThinkingFormat                   ai.ThinkingFormat
	SupportsStrictMode               bool
}

// detectCompat auto-detects compat settings from the model's provider and base URL.
func detectCompat(model *ai.Model) resolvedCompat {
	provider := model.Provider
	baseURL := model.BaseURL

	isZai := provider == "zai" || strings.Contains(baseURL, "api.z.ai")

	isNonStandard := provider == "cerebras" || strings.Contains(baseURL, "cerebras.ai") ||
		provider == "xai" || strings.Contains(baseURL, "api.x.ai") ||
		strings.Contains(baseURL, "chutes.ai") || strings.Contains(baseURL, "deepseek.com") ||
		isZai || provider == "opencode" || strings.Contains(baseURL, "opencode.ai")

	useMaxTokens := strings.Contains(baseURL, "chutes.ai")

	isGrok := provider == "xai" || strings.Contains(baseURL, "api.x.ai")
	isGroq := provider == "groq" || strings.Contains(baseURL, "groq.com")

	maxField := ai.MaxTokensFieldMaxCompletionTokens
	if useMaxTokens {
		maxField = ai.MaxTokensFieldMaxTokens
	}

	thinkingFmt := ai.ThinkingFormatOpenAI
	if isZai {
		thinkingFmt = ai.ThinkingFormatZAI
	}

	var reasoningEffortMap map[string]string
	if isGroq && model.ID == "qwen/qwen3-32b" {
		reasoningEffortMap = map[string]string{
			"minimal": "default",
			"low":     "default",
			"medium":  "default",
			"high":    "default",
			"xhigh":   "default",
		}
	}

	return resolvedCompat{
		SupportsStore:                    !isNonStandard,
		SupportsDeveloperRole:            !isNonStandard,
		SupportsReasoningEffort:          !isGrok && !isZai,
		ReasoningEffortMap:               reasoningEffortMap,
		SupportsUsageInStreaming:         true,
		MaxTokensField:                   maxField,
		RequiresToolResultName:           false,
		RequiresAssistantAfterToolResult: false,
		RequiresThinkingAsText:           false,
		ThinkingFormat:                   thinkingFmt,
		SupportsStrictMode:               true,
	}
}

// getCompat merges explicit model.Compat with auto-detected defaults.
func getCompat(model *ai.Model) resolvedCompat {
	detected := detectCompat(model)
	c := model.GetOpenAICompletionsCompat()
	if c == nil {
		return detected
	}

	if c.SupportsStore != nil {
		detected.SupportsStore = *c.SupportsStore
	}
	if c.SupportsDeveloperRole != nil {
		detected.SupportsDeveloperRole = *c.SupportsDeveloperRole
	}
	if c.SupportsReasoningEffort != nil {
		detected.SupportsReasoningEffort = *c.SupportsReasoningEffort
	}
	if c.ReasoningEffortMap != nil {
		detected.ReasoningEffortMap = c.ReasoningEffortMap
	}
	if c.SupportsUsageInStreaming != nil {
		detected.SupportsUsageInStreaming = *c.SupportsUsageInStreaming
	}
	if c.MaxTokensField != "" {
		detected.MaxTokensField = c.MaxTokensField
	}
	if c.RequiresToolResultName != nil {
		detected.RequiresToolResultName = *c.RequiresToolResultName
	}
	if c.RequiresAssistantAfterToolResult != nil {
		detected.RequiresAssistantAfterToolResult = *c.RequiresAssistantAfterToolResult
	}
	if c.RequiresThinkingAsText != nil {
		detected.RequiresThinkingAsText = *c.RequiresThinkingAsText
	}
	if c.ThinkingFormat != "" {
		detected.ThinkingFormat = c.ThinkingFormat
	}
	if c.SupportsStrictMode != nil {
		detected.SupportsStrictMode = *c.SupportsStrictMode
	}

	return detected
}

// hasToolHistory checks if conversation messages contain tool calls or tool results.
func hasToolHistory(messages []ai.Message) bool {
	for _, msg := range messages {
		if msg.AsToolResult() != nil {
			return true
		}
		if a := msg.AsAssistant(); a != nil {
			for _, block := range a.Content {
				if block.IsToolCall() {
					return true
				}
			}
		}
	}
	return false
}

// StreamOpenAICompletions implements SSE streaming for OpenAI Chat Completions API.
func StreamOpenAICompletions(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()

	go func() {
		output := &ai.AssistantMessage{
			Role:       ai.RoleAssistant,
			Content:    []ai.AssistantContent{},
			Api:        model.Api,
			Provider:   model.Provider,
			Model:      model.ID,
			Usage:      ai.ZeroUsage(),
			StopReason: ai.StopReasonStop,
			Timestamp:  time.Now().UnixMilli(),
		}

		err := streamOpenAIHTTP(ctx, model, prompt, options, output, stream)
		if err != nil {
			// Retry transient server errors before surfacing
			if ctx.Err() == nil && ratelimit.IsRetryableError(err.Error()) {
				for retry := 1; retry <= 2; retry++ {
					firlog.Debug("openai transient error, retrying", "attempt", retry, "err", err)
					select {
					case <-ctx.Done():
						err = ctx.Err()
					case <-time.After(time.Duration(retry) * time.Second):
					}
					if ctx.Err() != nil {
						break
					}
					output.Content = nil
					output.Usage = ai.ZeroUsage()
					output.StopReason = ai.StopReasonStop
					output.ErrorMessage = ""
					err = streamOpenAIHTTP(ctx, model, prompt, options, output, stream)
					if err == nil || ctx.Err() != nil || !ratelimit.IsRetryableError(err.Error()) {
						break
					}
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					output.StopReason = ai.StopReasonAborted
				} else {
					output.StopReason = ai.StopReasonError
				}
				output.ErrorMessage = err.Error()
				stream.Push(ai.AssistantMessageEvent{
					Type:   ai.EventError,
					Reason: output.StopReason,
					Error:  output,
				})
				stream.End(nil)
				return
			}
		}

		firlog.Debug("openai response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
		stream.End(nil)
	}()

	return stream
}

func streamOpenAIHTTP(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) error {
	apiKey := ""
	if options != nil {
		apiKey = options.ApiKey
	}
	if apiKey == "" {
		apiKey = envkeys.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		return errors.New(noAPIKeyError(model.Provider, apiKeyErrorFromOpts(options)))
	}

	body, err := buildOpenAIRequestBody(model, prompt, options)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if options != nil && options.OnPayload != nil {
		var rawBody map[string]any
		if jsonErr := json.Unmarshal(body, &rawBody); jsonErr == nil {
			if next := options.OnPayload(rawBody, model); next != nil {
				if body, err = json.Marshal(next); err != nil {
					return fmt.Errorf("re-marshaling payload: %w", err)
				}
			}
		}
	}

	url := openAIChatCompletionsURL(model.BaseURL)

	firlog.Debug("openai request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	// Build auth + model headers (options merged after provider-specific logic below)
	baseHeaders := BuildRequestHeaders(
		map[string]string{"Authorization": "Bearer " + apiKey},
		model, nil,
	)
	ApplyHeaders(req, baseHeaders)

	// Copilot-specific headers
	if model.Provider == "github-copilot" {
		msgs := prompt.Messages
		isAgentCall := false
		if len(msgs) > 0 {
			last := msgs[len(msgs)-1]
			isAgentCall = last.Role() != ai.RoleUser
		}
		if isAgentCall {
			req.Header.Set("X-Initiator", "agent")
		} else {
			req.Header.Set("X-Initiator", "user")
		}
		req.Header.Set("Openai-Intent", "conversation-edits")

		// Check for images in conversation
		for _, msg := range msgs {
			if u := msg.AsUser(); u != nil {
				if content, ok := u.Content.([]any); ok {
					for _, item := range content {
						if m, ok := item.(map[string]any); ok {
							if t, _ := m["type"].(string); t == "image" {
								req.Header.Set("Copilot-Vision-Request", "true")
								goto doneImageCheck
							}
						}
					}
				}
			}
		}
	doneImageCheck:
	}

	// Option-level headers (override all)
	if options != nil {
		for k, v := range options.Headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		firlog.Warn("openai HTTP error", "model", model.ID, "err", err)
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		firlog.Warn("openai error response", "model", model.ID, "status", resp.StatusCode)
		return fmt.Errorf("%d: %s", resp.StatusCode, string(bodyBytes))
	}

	stream.Push(ai.AssistantMessageEvent{
		Type:    ai.EventStart,
		Partial: output,
	})

	return parseOpenAISSE(resp.Body, model, output, stream)
}

func parseOpenAISSE(
	reader io.Reader,
	model *ai.Model,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var blocks []openaiBlock
	var currentBlockIdx int = -1

	finishCurrentBlock := func() {
		if currentBlockIdx < 0 || currentBlockIdx >= len(output.Content) {
			return
		}
		c := output.Content[currentBlockIdx]
		switch {
		case c.IsText():
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventTextEnd,
				ContentIndex: currentBlockIdx,
				Content:      c.Text.Text,
				Partial:      output,
			})
		case c.IsThinking():
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventThinkingEnd,
				ContentIndex: currentBlockIdx,
				Content:      c.Thinking.Thinking,
				Partial:      output,
			})
		case c.IsToolCall():
			if currentBlockIdx < len(blocks) && blocks[currentBlockIdx].partialArgs != "" {
				parsed := jsonparse.ParseStreamingJSON(blocks[currentBlockIdx].partialArgs)
				c.ToolCall.Arguments = parsed
				output.Content[currentBlockIdx] = c
			}
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventToolcallEnd,
				ContentIndex: currentBlockIdx,
				ToolCall:     c.ToolCall,
				Partial:      output,
			})
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk openaiChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Track response ID (same across all chunks in a single completion)
		if chunk.ID != "" && output.ResponseID == "" {
			output.ResponseID = chunk.ID
		}

		// Usage
		if chunk.Usage != nil {
			output.Usage = parseChunkUsage(chunk.Usage, model)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		// Fallback: some providers (e.g. Moonshot) return usage in choice.usage
		if chunk.Usage == nil && choice.Usage != nil {
			output.Usage = parseChunkUsage(choice.Usage, model)
		}

		if choice.FinishReason != nil {
			result := mapOpenAIStopReason(*choice.FinishReason)
			output.StopReason = result.StopReason
			if result.ErrorMessage != "" {
				output.ErrorMessage = result.ErrorMessage
			}
		}

		delta := choice.Delta

		// Text content
		if delta.Content != nil && *delta.Content != "" {
			if currentBlockIdx < 0 || !output.Content[currentBlockIdx].IsText() {
				finishCurrentBlock()
				output.Content = append(output.Content, ai.NewTextContent(""))
				blocks = append(blocks, openaiBlock{contentType: "text"})
				currentBlockIdx = len(output.Content) - 1
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventTextStart,
					ContentIndex: currentBlockIdx,
					Partial:      output,
				})
			}
			c := output.Content[currentBlockIdx]
			c.Text.Text += *delta.Content
			output.Content[currentBlockIdx] = c
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventTextDelta,
				ContentIndex: currentBlockIdx,
				Delta:        *delta.Content,
				Partial:      output,
			})
		}

		// Reasoning / thinking — use first non-empty reasoning field
		var reasoningDelta string
		if delta.ReasoningContent != nil && *delta.ReasoningContent != "" {
			reasoningDelta = *delta.ReasoningContent
		} else if delta.Reasoning != nil && *delta.Reasoning != "" {
			reasoningDelta = *delta.Reasoning
		} else if delta.ReasoningText != nil && *delta.ReasoningText != "" {
			reasoningDelta = *delta.ReasoningText
		}
		if reasoningDelta != "" {
			if currentBlockIdx < 0 || !output.Content[currentBlockIdx].IsThinking() {
				finishCurrentBlock()
				output.Content = append(output.Content, ai.NewThinkingContent(""))
				blocks = append(blocks, openaiBlock{contentType: "thinking"})
				currentBlockIdx = len(output.Content) - 1
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventThinkingStart,
					ContentIndex: currentBlockIdx,
					Partial:      output,
				})
			}
			c := output.Content[currentBlockIdx]
			c.Thinking.Thinking += reasoningDelta
			output.Content[currentBlockIdx] = c
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventThinkingDelta,
				ContentIndex: currentBlockIdx,
				Delta:        reasoningDelta,
				Partial:      output,
			})
		}

		// Tool calls
		for _, tc := range delta.ToolCalls {
			if tc.ID != "" && (currentBlockIdx < 0 || !output.Content[currentBlockIdx].IsToolCall() ||
				output.Content[currentBlockIdx].ToolCall.ID != tc.ID) {
				finishCurrentBlock()
				name := ""
				if tc.Function != nil {
					name = tc.Function.Name
				}
				output.Content = append(output.Content, ai.NewToolCallContent(tc.ID, name, map[string]any{}))
				blocks = append(blocks, openaiBlock{contentType: "toolCall"})
				currentBlockIdx = len(output.Content) - 1
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventToolcallStart,
					ContentIndex: currentBlockIdx,
					Partial:      output,
				})
			}

			c := output.Content[currentBlockIdx]
			if c.IsToolCall() {
				if tc.ID != "" {
					c.ToolCall.ID = tc.ID
				}
				if tc.Function != nil {
					if tc.Function.Name != "" {
						c.ToolCall.Name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						blocks[currentBlockIdx].partialArgs += tc.Function.Arguments
						c.ToolCall.Arguments = jsonparse.ParseStreamingJSON(blocks[currentBlockIdx].partialArgs)
					}
				}
				output.Content[currentBlockIdx] = c
				argDelta := ""
				if tc.Function != nil {
					argDelta = tc.Function.Arguments
				}
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventToolcallDelta,
					ContentIndex: currentBlockIdx,
					Delta:        argDelta,
					Partial:      output,
				})
			}
		}
	}

	finishCurrentBlock()
	return scanner.Err()
}

func parseChunkUsage(u *openaiUsage, model *ai.Model) ai.Usage {
	cachedTokens := 0
	if u.PromptTokensDetails != nil {
		cachedTokens = u.PromptTokensDetails.CachedTokens
	}
	reasoningTokens := 0
	if u.CompletionTokensDetails != nil {
		reasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	// OpenAI includes cached tokens in prompt_tokens, subtract to get non-cached input.
	input := u.PromptTokens - cachedTokens
	// Add reasoning tokens to output (some providers omit them from total_tokens).
	outputTokens := u.CompletionTokens + reasoningTokens
	usage := ai.Usage{
		Input:       input,
		Output:      outputTokens,
		CacheRead:   cachedTokens,
		TotalTokens: input + outputTokens + cachedTokens,
	}
	ai.CalculateCost(model, &usage)
	return usage
}

type openaiStopResult struct {
	StopReason   ai.StopReason
	ErrorMessage string
}

func mapOpenAIStopReason(reason string) openaiStopResult {
	switch reason {
	case "stop", "end":
		return openaiStopResult{StopReason: ai.StopReasonStop}
	case "length":
		return openaiStopResult{StopReason: ai.StopReasonLength}
	case "tool_calls", "function_call":
		return openaiStopResult{StopReason: ai.StopReasonToolUse}
	case "content_filter":
		return openaiStopResult{StopReason: ai.StopReasonError}
	default:
		// Unknown finish reason: treat as error with diagnostic
		if reason != "" {
			return openaiStopResult{
				StopReason:   ai.StopReasonError,
				ErrorMessage: fmt.Sprintf("Unknown finish_reason: %s", reason),
			}
		}
		return openaiStopResult{StopReason: ai.StopReasonStop}
	}
}

// --- Request body building ---

func buildOpenAIRequestBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) ([]byte, error) {
	compat := getCompat(model)

	body := map[string]any{
		"model":  model.ID,
		"stream": true,
	}

	// stream_options (conditional)
	if compat.SupportsUsageInStreaming {
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	// store: false for providers that support it
	if compat.SupportsStore {
		body["store"] = false
	}

	// Max tokens — use the correct field name per provider
	maxTokens := 0
	if options != nil && options.MaxTokens != nil {
		maxTokens = *options.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = model.MaxTokens
		if maxTokens > 32000 {
			maxTokens = 32000
		}
	}
	if compat.MaxTokensField == ai.MaxTokensFieldMaxTokens {
		body["max_tokens"] = maxTokens
	} else {
		body["max_completion_tokens"] = maxTokens
	}

	// Temperature
	if options != nil && options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}

	// Messages
	messages := convertOpenAIMessages(ctx, model, compat)
	maybeAddOpenRouterAnthropicCacheControl(model, messages)
	body["messages"] = messages

	// Tools
	if len(ctx.Tools) > 0 {
		body["tools"] = convertOpenAITools(ctx.Tools, compat)
	} else if hasToolHistory(ctx.Messages) {
		// Anthropic via LiteLLM/proxy requires tools param when conversation has tool_calls/tool_results
		body["tools"] = []any{}
	}

	// Tool choice
	if options != nil && options.ToolChoice != "" {
		body["tool_choice"] = options.ToolChoice
	}

	// Thinking / reasoning format
	if options != nil && options.ReasoningEffort != "" && model.Reasoning {
		switch compat.ThinkingFormat {
		case ai.ThinkingFormatZAI:
			body["enable_thinking"] = true
		case ai.ThinkingFormatQwen:
			body["enable_thinking"] = true
		case ai.ThinkingFormatQwenChatTpl:
			body["chat_template_kwargs"] = map[string]any{"enable_thinking": true}
		case ai.ThinkingFormatOpenRouter:
			effort := string(options.ReasoningEffort)
			if mapped, ok := compat.ReasoningEffortMap[effort]; ok {
				effort = mapped
			}
			body["reasoning"] = map[string]any{"effort": effort}
		default:
			if compat.SupportsReasoningEffort {
				effort := string(options.ReasoningEffort)
				if mapped, ok := compat.ReasoningEffortMap[effort]; ok {
					effort = mapped
				}
				body["reasoning_effort"] = effort
			}
		}
	} else if compat.ThinkingFormat == ai.ThinkingFormatZAI && model.Reasoning {
		body["enable_thinking"] = false
	} else if compat.ThinkingFormat == ai.ThinkingFormatQwen && model.Reasoning {
		body["enable_thinking"] = false
	} else if compat.ThinkingFormat == ai.ThinkingFormatQwenChatTpl && model.Reasoning {
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	} else if compat.ThinkingFormat == ai.ThinkingFormatOpenRouter && model.Reasoning {
		body["reasoning"] = map[string]any{"effort": "none"}
	}

	// OpenRouter provider routing preferences
	if strings.Contains(model.BaseURL, "openrouter.ai") {
		if c := model.GetOpenAICompletionsCompat(); c != nil && c.OpenRouterRouting != nil {
			body["provider"] = c.OpenRouterRouting
		}
	}

	// Vercel AI Gateway provider routing preferences
	if strings.Contains(model.BaseURL, "ai-gateway.vercel.sh") {
		if c := model.GetOpenAICompletionsCompat(); c != nil && c.VercelGatewayRouting != nil {
			r := c.VercelGatewayRouting
			if len(r.Only) > 0 || len(r.Order) > 0 {
				gw := map[string]any{}
				if len(r.Only) > 0 {
					gw["only"] = r.Only
				}
				if len(r.Order) > 0 {
					gw["order"] = r.Order
				}
				body["providerOptions"] = map[string]any{"gateway": gw}
			}
		}
	}

	return json.Marshal(body)
}

// normalizeOpenAIToolCallID normalizes tool call IDs for OpenAI-compatible providers.
func normalizeOpenAIToolCallID(id string, model *ai.Model, compat resolvedCompat) string {
	// Handle pipe-separated IDs from OpenAI Responses API
	if strings.Contains(id, "|") {
		parts := strings.SplitN(id, "|", 2)
		callID := parts[0]
		// Sanitize to allowed chars and truncate to 40 chars
		var b strings.Builder
		for _, c := range callID {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
				b.WriteRune(c)
			} else {
				b.WriteByte('_')
			}
			if b.Len() >= 40 {
				break
			}
		}
		return b.String()
	}

	if model.Provider == "openai" && len(id) > 40 {
		return id[:40]
	}

	// Copilot Claude models need Anthropic ID format
	if model.Provider == "github-copilot" && strings.Contains(strings.ToLower(model.ID), "claude") {
		var b strings.Builder
		for _, c := range id {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
				b.WriteRune(c)
			} else {
				b.WriteByte('_')
			}
			if b.Len() >= 64 {
				break
			}
		}
		return b.String()
	}

	return id
}

func convertOpenAIMessages(ctx ai.Context, model *ai.Model, compat resolvedCompat) []map[string]any {
	var messages []map[string]any

	// Tool ID normalizer that captures compat
	normalizeToolID := func(id string, m *ai.Model, _ *ai.AssistantMessage) string {
		return normalizeOpenAIToolCallID(id, m, compat)
	}

	// Transform messages for cross-provider compatibility
	transformed := TransformMessages(ctx.Messages, model, normalizeToolID)

	// System prompt — use developer role for reasoning models if supported
	if ctx.SystemPrompt != "" {
		role := "system"
		if model.Reasoning && compat.SupportsDeveloperRole {
			role = "developer"
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": ctx.SystemPrompt,
		})
	}

	var lastRole string

	for i := 0; i < len(transformed); i++ {
		msg := &transformed[i]

		// Insert synthetic assistant message after tool result if required
		if compat.RequiresAssistantAfterToolResult && lastRole == "toolResult" && msg.Role() == ai.RoleUser {
			messages = append(messages, map[string]any{
				"role":    "assistant",
				"content": "I have processed the tool results.",
			})
		}

		if u := msg.AsUser(); u != nil {
			switch content := u.Content.(type) {
			case string:
				if strings.TrimSpace(content) != "" {
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": content,
					})
				}
			case []any:
				var parts []map[string]any
				for _, item := range content {
					if m, ok := item.(map[string]any); ok {
						t, _ := m["type"].(string)
						if t == "text" {
							text, _ := m["text"].(string)
							parts = append(parts, map[string]any{
								"type": "text",
								"text": text,
							})
						} else if t == "image" && model.SupportsImages() {
							data, _ := m["data"].(string)
							mime, _ := m["mimeType"].(string)
							parts = append(parts, map[string]any{
								"type": "image_url",
								"image_url": map[string]any{
									"url": fmt.Sprintf("data:%s;base64,%s", mime, data),
								},
							})
						}
					}
				}
				// Filter out images if model doesn't support them
				if !model.SupportsImages() {
					var filtered []map[string]any
					for _, p := range parts {
						if t, _ := p["type"].(string); t != "image_url" {
							filtered = append(filtered, p)
						}
					}
					parts = filtered
				}
				if len(parts) > 0 {
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": parts,
					})
				}
			}
			lastRole = "user"

		} else if a := msg.AsAssistant(); a != nil {
			assistantMsg := map[string]any{
				"role": "assistant",
			}

			// Text blocks
			var textParts []map[string]any
			for _, block := range a.Content {
				if block.IsText() && strings.TrimSpace(block.Text.Text) != "" {
					textParts = append(textParts, map[string]any{
						"type": "text",
						"text": block.Text.Text,
					})
				}
			}

			// Always send assistant content as a plain string. Sending as an
			// array of {type:"text", text:"..."} objects is non-standard and
			// causes some models (e.g. DeepSeek V3.2 via NVIDIA NIM) to mirror
			// the content-block structure literally in their output.
			if len(textParts) > 0 {
				var sb strings.Builder
				for _, p := range textParts {
					sb.WriteString(p["text"].(string))
				}
				assistantMsg["content"] = sb.String()
			} else if compat.RequiresAssistantAfterToolResult {
				// Mistral requires non-null content
				assistantMsg["content"] = ""
			}

			// Thinking blocks
			var thinkingBlocks []ai.ThinkingContent
			for _, block := range a.Content {
				if block.IsThinking() && strings.TrimSpace(block.Thinking.Thinking) != "" {
					thinkingBlocks = append(thinkingBlocks, *block.Thinking)
				}
			}
			if len(thinkingBlocks) > 0 {
				if compat.RequiresThinkingAsText {
					// Convert thinking to plain text (no tags)
					var sb strings.Builder
					for j, tb := range thinkingBlocks {
						if j > 0 {
							sb.WriteString("\n\n")
						}
						sb.WriteString(tb.Thinking)
					}
					thinkingText := sb.String()
					if content, ok := assistantMsg["content"].([]map[string]any); ok {
						assistantMsg["content"] = append([]map[string]any{{"type": "text", "text": thinkingText}}, content...)
					} else {
						assistantMsg["content"] = []map[string]any{{"type": "text", "text": thinkingText}}
					}
				} else {
					// Use signature from first thinking block for providers like llama.cpp
					sig := thinkingBlocks[0].ThinkingSignature
					if sig != "" {
						var sb strings.Builder
						for j, tb := range thinkingBlocks {
							if j > 0 {
								sb.WriteString("\n")
							}
							sb.WriteString(tb.Thinking)
						}
						assistantMsg[sig] = sb.String()
					}
				}
			}

			// Tool calls
			var toolCalls []map[string]any
			for _, block := range a.Content {
				if block.IsToolCall() {
					tc := block.ToolCall
					argsJSON, _ := json.Marshal(tc.Arguments)
					toolCalls = append(toolCalls, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": string(argsJSON),
						},
					})
				}
			}
			if len(toolCalls) > 0 {
				assistantMsg["tool_calls"] = toolCalls
			}

			// Skip empty assistant messages (no content and no tool calls)
			hasContent := false
			if c, ok := assistantMsg["content"]; ok && c != nil {
				switch v := c.(type) {
				case string:
					hasContent = len(v) > 0
				case []map[string]any:
					hasContent = len(v) > 0
				default:
					hasContent = true
				}
			}
			if !hasContent && len(toolCalls) == 0 {
				lastRole = "assistant"
				continue
			}

			messages = append(messages, assistantMsg)
			lastRole = "assistant"

		} else if tr := msg.AsToolResult(); tr != nil {
			var imageBlocks []map[string]any

			// Collect consecutive tool results
			for {
				toolMsg := transformed[i].AsToolResult()
				if toolMsg == nil {
					break
				}

				// Extract text content
				var textParts []string
				for _, c := range toolMsg.Content {
					if c.IsText() {
						textParts = append(textParts, c.Text)
					}
				}
				textResult := strings.Join(textParts, "\n")

				// Check for images
				hasImages := false
				for _, c := range toolMsg.Content {
					if c.IsImage() {
						hasImages = true
						break
					}
				}

				content := textResult
				if content == "" && hasImages {
					content = "(see attached image)"
				}

				toolResultMsg := map[string]any{
					"role":         "tool",
					"content":      content,
					"tool_call_id": toolMsg.ToolCallID,
				}
				if compat.RequiresToolResultName && toolMsg.ToolName != "" {
					toolResultMsg["name"] = toolMsg.ToolName
				}
				messages = append(messages, toolResultMsg)

				// Collect images for later
				if hasImages && model.SupportsImages() {
					for _, c := range toolMsg.Content {
						if c.IsImage() {
							imageBlocks = append(imageBlocks, map[string]any{
								"type": "image_url",
								"image_url": map[string]any{
									"url": fmt.Sprintf("data:%s;base64,%s", c.MimeType, c.Data),
								},
							})
						}
					}
				}

				if i+1 < len(transformed) && transformed[i+1].Role() == ai.RoleToolResult {
					i++
				} else {
					break
				}
			}

			// If we have images from tool results, send as user message
			if len(imageBlocks) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					messages = append(messages, map[string]any{
						"role":    "assistant",
						"content": "I have processed the tool results.",
					})
				}
				content := append([]map[string]any{{"type": "text", "text": "Attached image(s) from tool result:"}}, imageBlocks...)
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": content,
				})
				lastRole = "user"
			} else {
				lastRole = "toolResult"
			}
			continue
		}
	}

	return messages
}

// maybeAddOpenRouterAnthropicCacheControl adds Anthropic-style cache_control for OpenRouter.
func maybeAddOpenRouterAnthropicCacheControl(model *ai.Model, messages []map[string]any) {
	if model.Provider != "openrouter" || !strings.HasPrefix(model.ID, "anthropic/") {
		return
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		role, _ := msg["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}
		content := msg["content"]
		if s, ok := content.(string); ok {
			msg["content"] = []map[string]any{
				{"type": "text", "text": s, "cache_control": map[string]any{"type": "ephemeral"}},
			}
			return
		}
		if arr, ok := content.([]map[string]any); ok {
			for j := len(arr) - 1; j >= 0; j-- {
				if t, _ := arr[j]["type"].(string); t == "text" {
					arr[j]["cache_control"] = map[string]any{"type": "ephemeral"}
					return
				}
			}
		}
	}
}

func convertOpenAITools(tools []ai.Tool, compat resolvedCompat) []map[string]any {
	var result []map[string]any
	for _, tool := range tools {
		schema := tool.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		fn := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  schema,
		}
		// Only include strict if provider supports it. Some reject unknown fields.
		if compat.SupportsStrictMode {
			fn["strict"] = false
		}
		result = append(result, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return result
}

// --- StreamSimple wrapper ---

// StreamSimpleOpenAICompletions wraps StreamOpenAICompletions with reasoning support.
func StreamSimpleOpenAICompletions(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.ApiKey
	}
	if apiKey == "" {
		apiKey = envkeys.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		return errorStreamProvider(model, noAPIKeyError(model.Provider, apiKeyErrorFromSimpleOpts(options)))
	}

	base := BuildBaseOptions(model, options, apiKey)

	if options != nil && options.Reasoning != "" && model.Reasoning {
		reasoningEffort := ClampReasoning(options.Reasoning)
		// Check if model supports xhigh (don't clamp to "high" for those)
		if ai.SupportsXhigh(model) {
			reasoningEffort = options.Reasoning
		}
		if reasoningEffort != "" {
			base.ReasoningEffort = reasoningEffort
		}
	}

	return StreamOpenAICompletions(ctx, model, prompt, base)
}

// RegisterOpenAICompletions registers the OpenAI Completions provider with the given registry.
func RegisterOpenAICompletions(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiOpenAICompletions,
		Stream:       StreamOpenAICompletions,
		StreamSimple: StreamSimpleOpenAICompletions,
	}, "builtin")
}
