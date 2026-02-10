// Ported from: packages/ai/src/providers/amazon-bedrock.ts
// Upstream hash: 1caadb2e
//
// NOTE: The TS version uses the AWS SDK (@aws-sdk/client-bedrock-runtime)
// which handles SigV4 signing internally. This Go port uses raw HTTP and
// expects either:
// - A proxy/gateway that handles auth (AWS_BEDROCK_SKIP_AUTH=1 pattern)
// - Pre-signed URLs in model.BaseURL
// - Future: native SigV4 signing without CGo
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

// --- Bedrock Converse Stream event types ---
// These mirror the ConverseStream response event structure.

// StreamBedrock implements streaming for Amazon Bedrock's ConverseStream API.
func StreamBedrock(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
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
		// Bedrock doesn't use API keys like other providers — it uses AWS credentials.
		// For now, we allow empty apiKey since auth may be handled by proxy/gateway.

		body, err := buildBedrockRequestBody(model, prompt, options)
		if err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = fmt.Sprintf("building request: %v", err)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		baseURL := model.BaseURL
		if baseURL == "" {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = "bedrock requires baseURL to be set (direct API or proxy)"
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}
		url := strings.TrimRight(baseURL, "/") + "/model/" + model.ID + "/converse-stream"

		headers := map[string]string{}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
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

		// Track content block indices
		type blockState struct {
			contentIdx  int
			partialJSON string
		}
		blocks := map[int]*blockState{}

		for evt := range sseEvents {
			if evt.Data == "" || evt.Data == "[DONE]" {
				continue
			}

			var raw map[string]any
			if err := json.Unmarshal([]byte(evt.Data), &raw); err != nil {
				continue
			}

			// messageStart
			if _, ok := raw["messageStart"]; ok {
				// Already sent EventStart above
				continue
			}

			// contentBlockStart
			if cbs, ok := raw["contentBlockStart"].(map[string]any); ok {
				blockIdx := jsonInt(cbs, "contentBlockIndex")
				if start, ok := cbs["start"].(map[string]any); ok {
					if toolUse, ok := start["toolUse"].(map[string]any); ok {
						idx := len(output.Content)
						toolID, _ := toolUse["toolUseId"].(string)
						toolName, _ := toolUse["name"].(string)
						output.Content = append(output.Content, ai.NewToolCallContent(toolID, toolName, map[string]any{}))
						blocks[blockIdx] = &blockState{contentIdx: idx}
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventToolcallStart,
							ContentIndex: idx,
							Partial:      output,
						})
					}
				}
				continue
			}

			// contentBlockDelta
			if cbd, ok := raw["contentBlockDelta"].(map[string]any); ok {
				blockIdx := jsonInt(cbd, "contentBlockIndex")
				delta, _ := cbd["delta"].(map[string]any)
				if delta == nil {
					continue
				}

				bs := blocks[blockIdx]

				// Text delta
				if text, ok := delta["text"].(string); ok {
					if bs == nil {
						// Create new text block
						idx := len(output.Content)
						output.Content = append(output.Content, ai.NewTextContent(""))
						bs = &blockState{contentIdx: idx}
						blocks[blockIdx] = bs
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextStart,
							ContentIndex: idx,
							Partial:      output,
						})
					}
					c := output.Content[bs.contentIdx]
					c.Text.Text += text
					output.Content[bs.contentIdx] = c
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventTextDelta,
						ContentIndex: bs.contentIdx,
						Delta:        text,
						Partial:      output,
					})
				}

				// Tool use delta
				if toolUse, ok := delta["toolUse"].(map[string]any); ok && bs != nil {
					if input, ok := toolUse["input"].(string); ok {
						bs.partialJSON += input
						parsed := ai.ParseStreamingJSON(bs.partialJSON)
						c := output.Content[bs.contentIdx]
						c.ToolCall.Arguments = parsed
						output.Content[bs.contentIdx] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventToolcallDelta,
							ContentIndex: bs.contentIdx,
							Delta:        input,
							Partial:      output,
						})
					}
				}

				// Reasoning/thinking delta
				if rc, ok := delta["reasoningContent"].(map[string]any); ok {
					if bs == nil {
						// Create new thinking block
						idx := len(output.Content)
						output.Content = append(output.Content, ai.NewThinkingContent(""))
						bs = &blockState{contentIdx: idx}
						blocks[blockIdx] = bs
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventThinkingStart,
							ContentIndex: idx,
							Partial:      output,
						})
					}
					if text, ok := rc["text"].(string); ok && text != "" {
						c := output.Content[bs.contentIdx]
						c.Thinking.Thinking += text
						output.Content[bs.contentIdx] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventThinkingDelta,
							ContentIndex: bs.contentIdx,
							Delta:        text,
							Partial:      output,
						})
					}
					if sig, ok := rc["signature"].(string); ok && sig != "" {
						c := output.Content[bs.contentIdx]
						c.Thinking.ThinkingSignature += sig
						output.Content[bs.contentIdx] = c
					}
				}
				continue
			}

			// contentBlockStop
			if cbs, ok := raw["contentBlockStop"].(map[string]any); ok {
				blockIdx := jsonInt(cbs, "contentBlockIndex")
				bs := blocks[blockIdx]
				if bs == nil {
					continue
				}
				idx := bs.contentIdx
				c := output.Content[idx]

				switch {
				case c.IsText():
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventTextEnd,
						ContentIndex: idx,
						Content:      c.Text.Text,
						Partial:      output,
					})
				case c.IsThinking():
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingEnd,
						ContentIndex: idx,
						Content:      c.Thinking.Thinking,
						Partial:      output,
					})
				case c.IsToolCall():
					if bs.partialJSON != "" {
						parsed := ai.ParseStreamingJSON(bs.partialJSON)
						c.ToolCall.Arguments = parsed
						output.Content[idx] = c
					}
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventToolcallEnd,
						ContentIndex: idx,
						ToolCall:     c.ToolCall,
						Partial:      output,
					})
				}
				delete(blocks, blockIdx)
				continue
			}

			// messageStop
			if ms, ok := raw["messageStop"].(map[string]any); ok {
				reason, _ := ms["stopReason"].(string)
				output.StopReason = mapBedrockStopReason(reason)
				continue
			}

			// metadata
			if meta, ok := raw["metadata"].(map[string]any); ok {
				if usage, ok := meta["usage"].(map[string]any); ok {
					output.Usage.Input = jsonInt(usage, "inputTokens")
					output.Usage.Output = jsonInt(usage, "outputTokens")
					output.Usage.CacheRead = jsonInt(usage, "cacheReadInputTokens")
					output.Usage.CacheWrite = jsonInt(usage, "cacheWriteInputTokens")
					output.Usage.TotalTokens = jsonInt(usage, "totalTokens")
					if output.Usage.TotalTokens == 0 {
						output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output
					}
					ai.CalculateCost(model, &output.Usage)
				}
				continue
			}

			// Error events
			for _, errKey := range []string{
				"internalServerException",
				"modelStreamErrorException",
				"validationException",
				"throttlingException",
				"serviceUnavailableException",
			} {
				if errObj, ok := raw[errKey].(map[string]any); ok {
					errMsg, _ := errObj["message"].(string)
					output.StopReason = ai.StopReasonError
					output.ErrorMessage = fmt.Sprintf("%s: %s", errKey, errMsg)
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
					return
				}
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

func mapBedrockStopReason(reason string) ai.StopReason {
	switch reason {
	case "end_turn", "stop_sequence":
		return ai.StopReasonStop
	case "max_tokens", "model_context_window_exceeded":
		return ai.StopReasonLength
	case "tool_use":
		return ai.StopReasonToolUse
	default:
		return ai.StopReasonError
	}
}

// --- Request body building ---

func buildBedrockRequestBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) ([]byte, error) {
	body := map[string]any{
		"modelId": model.ID,
	}

	// System prompt
	if ctx.SystemPrompt != "" {
		body["system"] = []map[string]any{
			{"text": ctx.SystemPrompt},
		}
	}

	// Messages
	var messages []map[string]any
	transformed := TransformMessages(ctx.Messages, model, nil)
	for _, msg := range transformed {
		if msg.AsUser() != nil {
			um := msg.AsUser()
			if s, ok := um.Content.(string); ok && strings.TrimSpace(s) != "" {
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": []map[string]any{{"text": s}},
				})
			}
		} else if msg.AsAssistant() != nil {
			am := msg.AsAssistant()
			var contentBlocks []map[string]any
			for _, block := range am.Content {
				switch {
				case block.IsText():
					if strings.TrimSpace(block.Text.Text) != "" {
						contentBlocks = append(contentBlocks, map[string]any{"text": block.Text.Text})
					}
				case block.IsToolCall():
					tc := block.ToolCall
					contentBlocks = append(contentBlocks, map[string]any{
						"toolUse": map[string]any{
							"toolUseId": tc.ID,
							"name":      tc.Name,
							"input":     tc.Arguments,
						},
					})
				case block.IsThinking():
					if strings.TrimSpace(block.Thinking.Thinking) != "" {
						contentBlocks = append(contentBlocks, map[string]any{
							"reasoningContent": map[string]any{
								"reasoningText": map[string]any{
									"text":      block.Thinking.Thinking,
									"signature": block.Thinking.ThinkingSignature,
								},
							},
						})
					}
				}
			}
			if len(contentBlocks) > 0 {
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": contentBlocks,
				})
			}
		} else if msg.AsToolResult() != nil {
			tr := msg.AsToolResult()
			var content []map[string]any
			for _, c := range tr.Content {
				if c.IsText() {
					content = append(content, map[string]any{"text": c.Text})
				}
			}
			if len(content) == 0 {
				content = []map[string]any{{"text": ""}}
			}
			status := "success"
			if tr.IsError {
				status = "error"
			}
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"toolResult": map[string]any{
							"toolUseId": tr.ToolCallID,
							"content":   content,
							"status":    status,
						},
					},
				},
			})
		}
	}
	body["messages"] = messages

	// Inference config
	inferenceConfig := map[string]any{}
	if options != nil && options.MaxTokens != nil {
		inferenceConfig["maxTokens"] = *options.MaxTokens
	}
	if options != nil && options.Temperature != nil {
		inferenceConfig["temperature"] = *options.Temperature
	}
	if len(inferenceConfig) > 0 {
		body["inferenceConfig"] = inferenceConfig
	}

	// Tools
	if len(ctx.Tools) > 0 {
		var tools []map[string]any
		for _, tool := range ctx.Tools {
			tools = append(tools, map[string]any{
				"toolSpec": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"inputSchema": map[string]any{"json": tool.Parameters},
				},
			})
		}
		body["toolConfig"] = map[string]any{
			"tools": tools,
		}
	}

	return json.Marshal(body)
}

// --- StreamSimple wrapper ---

func StreamSimpleBedrock(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	base := BuildBaseOptions(model, options, "")
	return StreamBedrock(ctx, model, prompt, base)
}

// RegisterBedrock registers the Amazon Bedrock provider.
func RegisterBedrock(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiBedrockConverseStream,
		Stream:       StreamBedrock,
		StreamSimple: StreamSimpleBedrock,
	}, "builtin")
}
