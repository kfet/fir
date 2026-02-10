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

		// Track current item for stateful SSE processing
		type itemState struct {
			itemType    string
			id          string
			callID      string
			name        string
			contentIdx  int
			partialJSON string
		}
		var current *itemState

		for evt := range sseEvents {
			if evt.Data == "" || evt.Data == "[DONE]" {
				continue
			}

			var raw map[string]any
			if err := json.Unmarshal([]byte(evt.Data), &raw); err != nil {
				continue
			}

			eventType, _ := raw["type"].(string)

			switch eventType {
			case "response.output_item.added":
				itemRaw, _ := raw["item"].(map[string]any)
				if itemRaw == nil {
					continue
				}
				iType, _ := itemRaw["type"].(string)

				switch iType {
				case "message":
					idx := len(output.Content)
					output.Content = append(output.Content, ai.NewTextContent(""))
					current = &itemState{
						itemType:   "message",
						id:         jsonString(itemRaw, "id"),
						contentIdx: idx,
					}
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventTextStart,
						ContentIndex: idx,
						Partial:      output,
					})

				case "reasoning":
					idx := len(output.Content)
					output.Content = append(output.Content, ai.NewThinkingContent(""))
					current = &itemState{
						itemType:   "reasoning",
						contentIdx: idx,
					}
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingStart,
						ContentIndex: idx,
						Partial:      output,
					})

				case "function_call":
					idx := len(output.Content)
					callID, _ := itemRaw["call_id"].(string)
					fcID, _ := itemRaw["id"].(string)
					fcName, _ := itemRaw["name"].(string)
					combinedID := callID
					if fcID != "" {
						combinedID = callID + "|" + fcID
					}
					output.Content = append(output.Content, ai.NewToolCallContent(combinedID, fcName, map[string]any{}))
					current = &itemState{
						itemType:    "function_call",
						id:          fcID,
						callID:      callID,
						name:        fcName,
						contentIdx:  idx,
						partialJSON: "",
					}
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventToolcallStart,
						ContentIndex: idx,
						Partial:      output,
					})
				}

			case "response.output_text.delta":
				if current != nil && current.itemType == "message" {
					delta, _ := raw["delta"].(string)
					if delta != "" {
						c := output.Content[current.contentIdx]
						c.Text.Text += delta
						output.Content[current.contentIdx] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextDelta,
							ContentIndex: current.contentIdx,
							Delta:        delta,
							Partial:      output,
						})
					}
				}

			case "response.refusal.delta":
				if current != nil && current.itemType == "message" {
					delta, _ := raw["delta"].(string)
					if delta != "" {
						c := output.Content[current.contentIdx]
						c.Text.Text += delta
						output.Content[current.contentIdx] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextDelta,
							ContentIndex: current.contentIdx,
							Delta:        delta,
							Partial:      output,
						})
					}
				}

			case "response.reasoning_summary_text.delta":
				if current != nil && current.itemType == "reasoning" {
					delta, _ := raw["delta"].(string)
					if delta != "" {
						c := output.Content[current.contentIdx]
						c.Thinking.Thinking += delta
						output.Content[current.contentIdx] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventThinkingDelta,
							ContentIndex: current.contentIdx,
							Delta:        delta,
							Partial:      output,
						})
					}
				}

			case "response.reasoning_summary_part.done":
				if current != nil && current.itemType == "reasoning" {
					c := output.Content[current.contentIdx]
					c.Thinking.Thinking += "\n\n"
					output.Content[current.contentIdx] = c
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingDelta,
						ContentIndex: current.contentIdx,
						Delta:        "\n\n",
						Partial:      output,
					})
				}

			case "response.function_call_arguments.delta":
				if current != nil && current.itemType == "function_call" {
					delta, _ := raw["delta"].(string)
					if delta != "" {
						current.partialJSON += delta
						parsed := ai.ParseStreamingJSON(current.partialJSON)
						c := output.Content[current.contentIdx]
						c.ToolCall.Arguments = parsed
						output.Content[current.contentIdx] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventToolcallDelta,
							ContentIndex: current.contentIdx,
							Delta:        delta,
							Partial:      output,
						})
					}
				}

			case "response.function_call_arguments.done":
				if current != nil && current.itemType == "function_call" {
					argsStr, _ := raw["arguments"].(string)
					if argsStr != "" {
						current.partialJSON = argsStr
						parsed := ai.ParseStreamingJSON(argsStr)
						c := output.Content[current.contentIdx]
						c.ToolCall.Arguments = parsed
						output.Content[current.contentIdx] = c
					}
				}

			case "response.output_item.done":
				if current == nil {
					continue
				}
				itemRaw, _ := raw["item"].(map[string]any)
				iType := ""
				if itemRaw != nil {
					iType, _ = itemRaw["type"].(string)
				}

				switch iType {
				case "message":
					// Finalize text
					idx := current.contentIdx
					finalText := output.Content[idx].Text.Text
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
					c := output.Content[idx]
					c.Text.Text = finalText
					// Store item ID as text signature
					if id, ok := itemRaw["id"].(string); ok {
						c.Text.TextSignature = id
					}
					output.Content[idx] = c
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventTextEnd,
						ContentIndex: idx,
						Content:      finalText,
						Partial:      output,
					})

				case "reasoning":
					idx := current.contentIdx
					finalThinking := output.Content[idx].Thinking.Thinking
					// Store reasoning item as signature (JSON)
					if sigJSON, err := json.Marshal(itemRaw); err == nil {
						c := output.Content[idx]
						c.Thinking.ThinkingSignature = string(sigJSON)
						output.Content[idx] = c
					}
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingEnd,
						ContentIndex: idx,
						Content:      finalThinking,
						Partial:      output,
					})

				case "function_call":
					idx := current.contentIdx
					// Parse final arguments
					if argsStr, ok := itemRaw["arguments"].(string); ok && argsStr != "" {
						var args map[string]any
						if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
							c := output.Content[idx]
							c.ToolCall.Arguments = args
							output.Content[idx] = c
						}
					}
					tc := output.Content[idx].ToolCall
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventToolcallEnd,
						ContentIndex: idx,
						ToolCall:     tc,
						Partial:      output,
					})
				}
				current = nil

			case "response.completed":
				respRaw, _ := raw["response"].(map[string]any)
				if respRaw != nil {
					if usageRaw, ok := respRaw["usage"].(map[string]any); ok {
						cachedTokens := 0
						if details, ok := usageRaw["input_tokens_details"].(map[string]any); ok {
							cachedTokens = jsonInt(details, "cached_tokens")
						}
						inputTokens := jsonInt(usageRaw, "input_tokens")
						output.Usage.Input = inputTokens - cachedTokens
						output.Usage.Output = jsonInt(usageRaw, "output_tokens")
						output.Usage.CacheRead = cachedTokens
						output.Usage.TotalTokens = jsonInt(usageRaw, "total_tokens")
						ai.CalculateCost(model, &output.Usage)
					}

					status, _ := respRaw["status"].(string)
					output.StopReason = mapOpenAIResponsesStatus(status)

					// If there are tool calls, override stop reason
					for _, c := range output.Content {
						if c.IsToolCall() {
							if output.StopReason == ai.StopReasonStop {
								output.StopReason = ai.StopReasonToolUse
							}
							break
						}
					}
				}

			case "error":
				code, _ := raw["code"].(string)
				message, _ := raw["message"].(string)
				errMsg := fmt.Sprintf("Error Code %s: %s", code, message)
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = errMsg
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return

			case "response.failed":
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = "Unknown error"
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return
			}
		}

		// Check for SSE-level errors
		select {
		case err := <-sseErr:
			if err != nil {
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = err.Error()
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return
			}
		default:
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
		var tools []map[string]any
		for _, tool := range ctx.Tools {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
				"strict":      false,
			})
		}
		body["tools"] = tools
	}

	// Reasoning
	if model.Reasoning {
		// Reasoning config can be extended via headers/options later
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
