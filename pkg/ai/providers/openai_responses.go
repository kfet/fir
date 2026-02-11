// Ported from: packages/ai/src/providers/openai-responses.ts + openai-responses-shared.ts
// Upstream hash: 1caadb2e
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kfet/pi-go/pkg/ai"
)

// --- OpenAI Responses API SSE event types ---

type responsesItem struct {
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Role     string         `json:"role"`
	CallID   string         `json:"call_id"`
	Name     string         `json:"name"`
	Args     string         `json:"arguments"`
	Content  []responsesPart `json:"content"`
	Status   string         `json:"status"`
	Summary  []responsesSummaryPart `json:"summary"`
}

type responsesPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Refusal  string `json:"refusal"`
}

type responsesSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsageDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responsesUsage struct {
	InputTokens        int                    `json:"input_tokens"`
	OutputTokens       int                    `json:"output_tokens"`
	TotalTokens        int                    `json:"total_tokens"`
	InputTokensDetails *responsesUsageDetails `json:"input_tokens_details"`
}

type responsesResponse struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Usage       *responsesUsage `json:"usage"`
	ServiceTier string          `json:"service_tier"`
}

// StreamOpenAIResponses implements streaming for the OpenAI Responses API.
func StreamOpenAIResponses(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
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

		defer func() {
			stream.End(nil)
		}()

		apiKey := ""
		if options != nil {
			apiKey = options.ApiKey
		}
		if apiKey == "" {
			apiKey = ai.GetEnvApiKey(model.Provider)
		}
		if apiKey == "" {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = fmt.Sprintf("no API key for provider: %s", model.Provider)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		body, err := buildOpenAIResponsesBody(model, prompt, options)
		if err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = fmt.Sprintf("building request: %v", err)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		baseURL := model.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		url := strings.TrimRight(baseURL, "/") + "/v1/responses"

		headers := map[string]string{
			"Authorization": "Bearer " + apiKey,
		}
		for k, v := range model.Headers {
			headers[k] = v
		}
		if options != nil {
			for k, v := range options.Headers {
				headers[k] = v
			}
		}

		sseEvents, sseErr := DefaultSSEClient.Stream(ctx, url, headers, bytes.NewReader(body))

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

		proc := &responsesSSEProcessor{output: output, stream: stream, model: model}
		errFromSSE := processResponsesSSEStream(proc, sseEvents, sseErr)
		if errFromSSE != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = errFromSSE.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
	}()

	return stream
}

func mapOpenAIResponsesStatus(status string) ai.StopReason {
	switch status {
	case "completed":
		return ai.StopReasonStop
	case "incomplete":
		return ai.StopReasonLength
	case "failed", "cancelled":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

// --- Request body building ---

func buildOpenAIResponsesBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) ([]byte, error) {
	body := map[string]any{
		"model":  model.ID,
		"stream": true,
		"store":  false,
	}

	// Messages (input)
	input := convertResponsesInput(model, ctx)
	body["input"] = input

	// Max tokens
	if options != nil && options.MaxTokens != nil {
		body["max_output_tokens"] = *options.MaxTokens
	}

	// Temperature
	if options != nil && options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}

	// Session ID for prompt caching
	if options != nil && options.SessionID != "" {
		body["prompt_cache_key"] = options.SessionID
	}

	// Tools
	if len(ctx.Tools) > 0 {
		body["tools"] = convertResponsesTools(ctx.Tools, false)
	}

	// Reasoning
	if model.Reasoning {
		if options != nil && options.ReasoningEffort != "" {
			effort := string(options.ReasoningEffort)
			if effort == "" {
				effort = "medium"
			}
			body["reasoning"] = map[string]any{
				"effort":  effort,
				"summary": "auto",
			}
			body["include"] = []string{"reasoning.encrypted_content"}
		} else if strings.HasPrefix(model.Name, "gpt-5") {
			// GPT-5 requires explicit reasoning disable
			// https://community.openai.com/t/need-reasoning-false-option-for-gpt-5/1351588/7
			input = append(input, map[string]any{
				"role": "developer",
				"content": []map[string]any{
					{"type": "input_text", "text": "# Juice: 0 !important"},
				},
			})
			body["input"] = input
		}
	}

	return json.Marshal(body)
}

// convertResponsesInput converts ai.Context to OpenAI Responses API input format.
func convertResponsesInput(model *ai.Model, ctx ai.Context) []any {
	var input []any

	// System prompt
	if ctx.SystemPrompt != "" {
		role := "system"
		if model.Reasoning {
			role = "developer"
		}
		input = append(input, map[string]any{
			"role":    role,
			"content": ctx.SystemPrompt,
		})
	}

	// Messages
	transformed := TransformMessages(ctx.Messages, model, nil)
	for _, msg := range transformed {
		if msg.AsUser() != nil {
			um := msg.AsUser()
			if s, ok := um.Content.(string); ok && strings.TrimSpace(s) != "" {
				input = append(input, map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "input_text", "text": s},
					},
				})
			}
		} else if msg.AsAssistant() != nil {
			am := msg.AsAssistant()
			for _, block := range am.Content {
				switch {
				case block.IsText():
					msgID := block.Text.TextSignature
					if msgID == "" {
						msgID = fmt.Sprintf("msg_%d", len(input))
					}
					input = append(input, map[string]any{
						"type":   "message",
						"role":   "assistant",
						"id":     msgID,
						"status": "completed",
						"content": []map[string]any{
							{"type": "output_text", "text": block.Text.Text, "annotations": []any{}},
						},
					})

				case block.IsThinking():
					if block.Thinking.ThinkingSignature != "" {
						// Re-serialize the reasoning item from the signature
						var item map[string]any
						if err := json.Unmarshal([]byte(block.Thinking.ThinkingSignature), &item); err == nil {
							input = append(input, item)
						}
					}

				case block.IsToolCall():
					tc := block.ToolCall
					parts := strings.SplitN(tc.ID, "|", 2)
					callID := parts[0]
					itemID := ""
					if len(parts) > 1 {
						itemID = parts[1]
					}
					argsJSON, _ := json.Marshal(tc.Arguments)
					item := map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      tc.Name,
						"arguments": string(argsJSON),
					}
					if itemID != "" {
						item["id"] = itemID
					}
					input = append(input, item)
				}
			}
		} else if msg.AsToolResult() != nil {
			tr := msg.AsToolResult()
			var text string
			for _, c := range tr.Content {
				if c.IsText() {
					text += c.Text
				}
			}
			parts := strings.SplitN(tr.ToolCallID, "|", 2)
			callID := parts[0]
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  text,
			})
		}
	}

	return input
}

// --- StreamSimple wrapper ---

func StreamSimpleOpenAIResponses(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
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

	if options != nil && options.Reasoning != "" && model.Reasoning {
		reasoningEffort := ClampReasoning(options.Reasoning)
		if ai.SupportsXhigh(model) {
			reasoningEffort = options.Reasoning
		}
		if reasoningEffort != "" {
			base.ReasoningEffort = reasoningEffort
		}
	}

	return StreamOpenAIResponses(ctx, model, prompt, base)
}

// RegisterOpenAIResponses registers the OpenAI Responses provider.
func RegisterOpenAIResponses(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiOpenAIResponses,
		Stream:       StreamOpenAIResponses,
		StreamSimple: StreamSimpleOpenAIResponses,
	}, "builtin")
}
