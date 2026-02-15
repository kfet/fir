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

	"github.com/kfet/tau/pkg/ai"
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

	retention := resolveCacheRetention("")
	if options != nil {
		retention = resolveCacheRetention(options.CacheRetention)
	}
	canCache := supportsBedrockPromptCaching(model) && retention != ai.CacheNone

	// System prompt
	if ctx.SystemPrompt != "" {
		sysBlocks := []map[string]any{
			{"text": ctx.SystemPrompt},
		}
		if canCache {
			cp := map[string]any{"type": "default"}
			if retention == ai.CacheLong {
				cp["ttl"] = "ONE_HOUR"
			}
			sysBlocks = append(sysBlocks, map[string]any{"cachePoint": cp})
		}
		body["system"] = sysBlocks
	}

	// Messages
	var messages []map[string]any
	normalizeID := func(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
		var b strings.Builder
		b.Grow(min(len(id), 64))
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
	transformed := TransformMessages(ctx.Messages, model, normalizeID)
	for i := 0; i < len(transformed); i++ {
		msg := &transformed[i]

		if u := msg.AsUser(); u != nil {
			switch content := u.Content.(type) {
			case string:
				if strings.TrimSpace(content) != "" {
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": []map[string]any{{"text": content}},
					})
				}
			case []any:
				var blocks []map[string]any
				for _, item := range content {
					if m, ok := item.(map[string]any); ok {
						switch m["type"] {
						case "text":
							if text, ok := m["text"].(string); ok {
								blocks = append(blocks, map[string]any{"text": text})
							}
						case "image":
							if model.SupportsImages() {
								data, _ := m["data"].(string)
								mime, _ := m["mimeType"].(string)
								blocks = append(blocks, map[string]any{
									"image": map[string]any{
										"format": bedrockImageFormat(mime),
										"source": map[string]any{"bytes": data},
									},
								})
							}
						}
					}
				}
				if len(blocks) > 0 {
					messages = append(messages, map[string]any{
						"role":    "user",
						"content": blocks,
					})
				}
			}
		} else if a := msg.AsAssistant(); a != nil {
			var contentBlocks []map[string]any
			for _, block := range a.Content {
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
						reasoning := map[string]any{
							"text": block.Thinking.Thinking,
						}
						// Only include signature for Anthropic Claude models
						if supportsBedrockThinkingSignature(model) {
							reasoning["signature"] = block.Thinking.ThinkingSignature
						}
						contentBlocks = append(contentBlocks, map[string]any{
							"reasoningContent": map[string]any{
								"reasoningText": reasoning,
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
		} else if tr := msg.AsToolResult(); tr != nil {
			// Collect consecutive tool results into a single user message
			var toolResults []map[string]any
			for {
				tr := transformed[i].AsToolResult()
				if tr == nil {
					break
				}
				var content []map[string]any
				for _, c := range tr.Content {
					if c.IsText() {
						content = append(content, map[string]any{"text": c.Text})
					} else if c.IsImage() && model.SupportsImages() {
						content = append(content, map[string]any{
							"image": map[string]any{
								"format": bedrockImageFormat(c.MimeType),
								"source": map[string]any{"bytes": c.Data},
							},
						})
					}
				}
				if len(content) == 0 {
					content = []map[string]any{{"text": ""}}
				}
				status := "success"
				if tr.IsError {
					status = "error"
				}
				toolResults = append(toolResults, map[string]any{
					"toolResult": map[string]any{
						"toolUseId": tr.ToolCallID,
						"content":   content,
						"status":    status,
					},
				})
				if i+1 < len(transformed) && transformed[i+1].Role() == ai.RoleToolResult {
					i++
				} else {
					break
				}
			}
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": toolResults,
			})
		}
	}

	// Add cache point to last user message
	if canCache && len(messages) > 0 {
		last := messages[len(messages)-1]
		if role, _ := last["role"].(string); role == "user" {
			if content, ok := last["content"].([]map[string]any); ok {
				cp := map[string]any{"type": "default"}
				if retention == ai.CacheLong {
					cp["ttl"] = "ONE_HOUR"
				}
				last["content"] = append(content, map[string]any{"cachePoint": cp})
			}
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

	// Tools with tool choice
	toolChoice := ""
	if options != nil {
		toolChoice = options.ToolChoice
	}
	if len(ctx.Tools) > 0 && toolChoice != "none" {
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
		toolConfig := map[string]any{"tools": tools}
		switch toolChoice {
		case "auto":
			toolConfig["toolChoice"] = map[string]any{"auto": map[string]any{}}
		case "any":
			toolConfig["toolChoice"] = map[string]any{"any": map[string]any{}}
		case "":
			// No explicit tool choice
		default:
			// Specific tool name
			toolConfig["toolChoice"] = map[string]any{"tool": map[string]any{"name": toolChoice}}
		}
		body["toolConfig"] = toolConfig
	}

	// Thinking/reasoning config (via headers from StreamSimple)
	if options != nil && options.Headers != nil {
		if reasoning := options.Headers["x-bedrock-reasoning"]; reasoning != "" {
			if strings.Contains(model.ID, "anthropic.claude") && model.Reasoning {
				additionalFields := map[string]any{}
				if supportsBedrockAdaptiveThinking(model.ID) {
					additionalFields["thinking"] = map[string]any{"type": "adaptive"}
					additionalFields["output_config"] = map[string]any{
						"effort": mapThinkingLevelToEffort(ai.ThinkingLevel(reasoning)),
					}
				} else {
					budget := 1024
					if b := options.Headers["x-bedrock-thinking-budget"]; b != "" {
						fmt.Sscanf(b, "%d", &budget)
					}
					additionalFields["thinking"] = map[string]any{
						"type":          "enabled",
						"budget_tokens": budget,
					}
					if options.Headers["x-bedrock-interleaved-thinking"] == "true" {
						additionalFields["anthropic_beta"] = []string{"interleaved-thinking-2025-05-14"}
					}
				}
				body["additionalModelRequestFields"] = additionalFields
			}
		}
	}

	return json.Marshal(body)
}

// supportsBedrockPromptCaching checks if the model supports prompt caching.
func supportsBedrockPromptCaching(model *ai.Model) bool {
	if model.Cost.CacheRead > 0 || model.Cost.CacheWrite > 0 {
		return true
	}
	id := strings.ToLower(model.ID)
	if strings.Contains(id, "claude") && (strings.Contains(id, "-4-") || strings.Contains(id, "-4.")) {
		return true
	}
	if strings.Contains(id, "claude-3-7-sonnet") {
		return true
	}
	if strings.Contains(id, "claude-3-5-haiku") {
		return true
	}
	return false
}

// supportsBedrockThinkingSignature checks if the model supports thinking signatures.
// Only Anthropic Claude models support the signature field in reasoningContent.
func supportsBedrockThinkingSignature(model *ai.Model) bool {
	id := strings.ToLower(model.ID)
	return strings.Contains(id, "anthropic.claude") || strings.Contains(id, "anthropic/claude")
}

// supportsBedrockAdaptiveThinking checks if the model supports adaptive thinking.
func supportsBedrockAdaptiveThinking(modelID string) bool {
	return strings.Contains(modelID, "opus-4-6") || strings.Contains(modelID, "opus-4.6")
}

// bedrockImageFormat maps MIME types to Bedrock image format strings.
func bedrockImageFormat(mimeType string) string {
	switch mimeType {
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "jpeg"
	}
}

// --- StreamSimple wrapper ---

func StreamSimpleBedrock(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	base := BuildBaseOptions(model, options, "")

	if options == nil || options.Reasoning == "" {
		return StreamBedrock(ctx, model, prompt, base)
	}

	if base.Headers == nil {
		base.Headers = map[string]string{}
	}

	effort := ClampReasoning(options.Reasoning)

	if strings.Contains(model.ID, "anthropic.claude") && model.Reasoning {
		if supportsBedrockAdaptiveThinking(model.ID) {
			base.Headers["x-bedrock-reasoning"] = string(effort)
		} else {
			maxTokens := 0
			if base.MaxTokens != nil {
				maxTokens = *base.MaxTokens
			}
			adjustedMax, thinkingBudget := AdjustMaxTokensForThinking(
				maxTokens, model.MaxTokens, effort, options.ThinkingBudgets)
			base.MaxTokens = &adjustedMax
			base.Headers["x-bedrock-reasoning"] = string(effort)
			base.Headers["x-bedrock-thinking-budget"] = fmt.Sprintf("%d", thinkingBudget)
			base.Headers["x-bedrock-interleaved-thinking"] = "true"
		}
	}

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
