// Ported from: packages/ai/src/providers/openai-completions.ts
// Upstream hash: 1caadb2e
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kfet/pi-go/pkg/ai"
)

// --- SSE types for OpenAI Chat Completions API ---

type openaiChunk struct {
	ID      string         `json:"id"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage"`
}

type openaiChoice struct {
	Index        int               `json:"index"`
	Delta        openaiDelta       `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

type openaiDelta struct {
	Role             string              `json:"role,omitempty"`
	Content          *string             `json:"content"`
	ReasoningContent *string             `json:"reasoning_content"`
	Reasoning        *string             `json:"reasoning"`
	ReasoningText    *string             `json:"reasoning_text"`
	ToolCalls        []openaiToolCall    `json:"tool_calls,omitempty"`
}

type openaiToolCall struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function *openaiToolCallFunc  `json:"function,omitempty"`
}

type openaiToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
		apiKey = ai.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		return fmt.Errorf("no API key for provider: %s", model.Provider)
	}

	body, err := buildOpenAIRequestBody(model, prompt, options)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	baseURL := model.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	url := strings.TrimRight(baseURL, "/") + "/v1/chat/completions"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	for k, v := range model.Headers {
		req.Header.Set(k, v)
	}
	if options != nil {
		for k, v := range options.Headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
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
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

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
				parsed := ai.ParseStreamingJSON(blocks[currentBlockIdx].partialArgs)
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

		// Usage
		if chunk.Usage != nil {
			cachedTokens := 0
			if chunk.Usage.PromptTokensDetails != nil {
				cachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
			input := chunk.Usage.PromptTokens - cachedTokens
			// CompletionTokens already includes reasoning tokens per OpenAI's API.
			output.Usage = ai.Usage{
				Input:       input,
				Output:      chunk.Usage.CompletionTokens,
				CacheRead:   cachedTokens,
				TotalTokens: input + chunk.Usage.CompletionTokens + cachedTokens,
			}
			ai.CalculateCost(model, &output.Usage)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		if choice.FinishReason != nil {
			output.StopReason = mapOpenAIStopReason(*choice.FinishReason)
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

		// Reasoning / thinking
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
						c.ToolCall.Arguments = ai.ParseStreamingJSON(blocks[currentBlockIdx].partialArgs)
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

func mapOpenAIStopReason(reason string) ai.StopReason {
	switch reason {
	case "stop":
		return ai.StopReasonStop
	case "length":
		return ai.StopReasonLength
	case "tool_calls":
		return ai.StopReasonToolUse
	case "content_filter":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

// --- Request body building ---

func buildOpenAIRequestBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) ([]byte, error) {
	body := map[string]any{
		"model":  model.ID,
		"stream": true,
	}

	// Max tokens
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
	body["max_completion_tokens"] = maxTokens

	// Temperature
	if options != nil && options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}

	// Stream options for usage
	body["stream_options"] = map[string]any{"include_usage": true}

	// Messages
	messages := convertOpenAIMessages(ctx, model)
	body["messages"] = messages

	// Tools
	if len(ctx.Tools) > 0 {
		body["tools"] = convertOpenAITools(ctx.Tools)
	}

	return json.Marshal(body)
}

func convertOpenAIMessages(ctx ai.Context, model *ai.Model) []map[string]any {
	var messages []map[string]any

	// System prompt
	if ctx.SystemPrompt != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": ctx.SystemPrompt,
		})
	}

	// Transform for cross-provider compat
	transformed := TransformMessages(ctx.Messages, model, nil)

	for _, msg := range transformed {
		if msg.AsUser() != nil {
			um := msg.AsUser()
			switch c := um.Content.(type) {
			case string:
				if strings.TrimSpace(c) != "" {
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": c,
					})
				}
			}
		} else if msg.AsAssistant() != nil {
			am := msg.AsAssistant()
			m := map[string]any{"role": "assistant"}

			var textParts []string
			var toolCalls []map[string]any

			for _, block := range am.Content {
				switch {
				case block.IsText():
					textParts = append(textParts, block.Text.Text)
				case block.IsThinking():
					// Thinking blocks omitted for OpenAI format
				case block.IsToolCall():
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

			if len(textParts) > 0 {
				m["content"] = strings.Join(textParts, "")
			}
			if len(toolCalls) > 0 {
				m["tool_calls"] = toolCalls
			}
			messages = append(messages, m)
		} else if msg.AsToolResult() != nil {
			tr := msg.AsToolResult()
			var text string
			for _, c := range tr.Content {
				if c.IsText() {
					text += c.Text
				}
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": tr.ToolCallID,
				"content":      text,
			})
		}
	}

	return messages
}

func convertOpenAITools(tools []ai.Tool) []map[string]any {
	var result []map[string]any
	for _, tool := range tools {
		schema := tool.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  schema,
				"strict":      true,
			},
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
		apiKey = ai.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		return errorStreamProvider(model, fmt.Sprintf("no API key for provider: %s", model.Provider))
	}

	base := BuildBaseOptions(model, options, apiKey)

	if options != nil && options.Reasoning != "" {
		level := ClampReasoning(options.Reasoning)
		if level != "" {
			// Add reasoning_effort to the request via headers or params
			// For now, just pass through — reasoning is provider-specific
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
