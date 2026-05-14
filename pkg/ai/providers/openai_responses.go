// Ported from: packages/ai/src/providers/openai-responses.ts + openai-responses-shared.ts
// Upstream hash: 036bde0a
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/ai/ratelimit"
	firlog "github.com/kfet/fir/pkg/log"
)

// --- OpenAI Responses API SSE event types ---

type responsesItem struct {
	Type    string                 `json:"type"`
	ID      string                 `json:"id"`
	Role    string                 `json:"role"`
	CallID  string                 `json:"call_id"`
	Name    string                 `json:"name"`
	Args    string                 `json:"arguments"`
	Content []responsesPart        `json:"content"`
	Status  string                 `json:"status"`
	Summary []responsesSummaryPart `json:"summary"`
}

type responsesPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
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
			apiKey = envkeys.GetEnvApiKey(model.Provider)
		}
		if apiKey == "" {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = noAPIKeyError(model.Provider, apiKeyErrorFromOpts(options))
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
		if options != nil && options.OnPayload != nil {
			var rawBody map[string]any
			if jsonErr := json.Unmarshal(body, &rawBody); jsonErr == nil {
				if next := options.OnPayload(rawBody, model); next != nil {
					if body, err = json.Marshal(next); err != nil {
						output.StopReason = ai.StopReasonError
						output.ErrorMessage = fmt.Sprintf("re-marshaling payload: %v", err)
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
						return
					}
				}
			}
		}

		url := openAIResponsesURL(ResolveCloudflareBaseURL(model))

		authHeaders := map[string]string{"Authorization": "Bearer " + apiKey}
		applyCloudflareAuthHeaders(model.Provider, authHeaders, apiKey)
		headers := BuildRequestHeaders(authHeaders, model, options)

		// Match upstream: when caching is enabled, set session headers so
		// providers that key cache on the session pick it up.
		// OpenAIResponsesCompat controls whether session_id is sent.
		compat := getOpenAIResponsesCompat(model)
		if options != nil && options.SessionID != "" && options.CacheRetention != ai.CacheNone {
			if compat.SendSessionIdHeader {
				headers["session_id"] = options.SessionID
			}
			headers["x-client-request-id"] = options.SessionID
		}

		firlog.Debug("openai-responses request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))
		traceWireMessages("openai-responses", body)

		const maxRetries = 3
		var lastErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			if attempt > 0 {
				firlog.Debug("openai-responses retry", "attempt", attempt, "lastErr", lastErr)
				select {
				case <-ctx.Done():
					lastErr = ctx.Err()
				case <-time.After(openaiRetryDelay(attempt)):
				}
				if ctx.Err() != nil {
					break
				}
			}
			sseEvents, sseErr := DefaultSSEClient.Stream(ctx, url, headers, bytes.NewReader(body))

			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

			proc := &responsesSSEProcessor{output: output, stream: stream, model: model}
			errFromSSE := processResponsesSSEStream(proc, sseEvents, sseErr)
			if errFromSSE == nil {
				lastErr = nil
				break
			}
			lastErr = errFromSSE

			// Only retry if it's a transient server error
			if attempt < maxRetries-1 && ctx.Err() == nil && ratelimit.IsRetryableError(errFromSSE.Error()) {
				// Reset output for retry
				output.Content = nil
				output.Usage = ai.ZeroUsage()
				output.StopReason = ""
				output.ErrorMessage = ""
				continue
			}
			break
		}
		if lastErr != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = lastErr.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		// Prune empty/whitespace-only text blocks from the final stored
		// response — same rationale as Anthropic's streamer (see
		// pruneEmptyAssistantTextBlocks). The Responses streamer eagerly
		// allocates a `NewTextContent("")` on `output_item.added` for
		// type=message; when no `output_text.delta` arrives (e.g. the
		// turn is reasoning→function_call only), that empty block leaks
		// into stored Content and breaks cross-provider replay.
		output.Content = pruneEmptyAssistantTextBlocks(output.Content)
		firlog.Debug("openai-responses response complete", "model", model.ID, "stopReason", output.StopReason)
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
	if options != nil && options.SessionID != "" && options.CacheRetention != ai.CacheNone {
		body["prompt_cache_key"] = options.SessionID
		if compat := getOpenAIResponsesCompat(model); options.CacheRetention == ai.CacheLong && compat.SupportsLongCacheRetention {
			body["prompt_cache_retention"] = "24h"
		}
	}

	// Tools
	tools := convertResponsesTools(ctx.Tools, false)

	// Add hosted shell tool when code_execution is configured and model supports it.
	if options != nil && supportsHostedShell(model) {
		for _, st := range options.ServerTools {
			if strings.HasPrefix(st.Type, "code_execution") {
				tools = append(tools, map[string]any{
					"type":        "shell",
					"environment": map[string]any{"type": "container_auto"},
				})
				break
			}
		}
	}

	if len(tools) > 0 {
		body["tools"] = tools
	}

	// Reasoning
	isPoe := model.Provider == ai.ProviderPoe || strings.Contains(model.BaseURL, "poe.com")
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
			// `include: ["reasoning.encrypted_content"]` is an
			// OpenAI/Azure-specific feature; third-party proxies
			// like Poe reject unknown include values.
			if !isPoe {
				body["include"] = []string{"reasoning.encrypted_content"}
			}
		} else if model.Provider != ai.ProviderGitHubCopilot && !isPoe {
			// Poe's reasoning.effort enum is {low,medium,high,xhigh};
			// "none" would 400. Just omit the field — Poe will use
			// its default.
			body["reasoning"] = map[string]any{"effort": "none"}
		}
	}

	return json.Marshal(body)
}

// modelSupportsImage reports whether the model accepts image input.
func modelSupportsImage(model *ai.Model) bool {
	for _, m := range model.Input {
		if m == ai.InputImage {
			return true
		}
	}
	return false
}

// convertResponsesInput converts ai.Context to OpenAI Responses API input format.
func convertResponsesInput(model *ai.Model, ctx ai.Context) []any {
	supportsImage := modelSupportsImage(model)
	var input []any

	// System prompt
	if ctx.SystemPrompt != "" {
		role := "system"
		// Poe's proxy only accepts system/user/assistant; the
		// `developer` role used by OpenAI reasoning models is rejected.
		isPoe := model.Provider == ai.ProviderPoe || strings.Contains(model.BaseURL, "poe.com")
		if model.Reasoning && !isPoe {
			role = "developer"
		}
		input = append(input, map[string]any{
			"role":    role,
			"content": ctx.SystemPrompt,
		})
	}

	// Messages — normalize tool call IDs for Responses API compatibility
	transformed := TransformMessages(ctx.Messages, model, normalizeResponsesToolCallID)
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
			} else if parts, ok := um.Content.([]any); ok {
				var content []map[string]any
				for _, p := range parts {
					switch v := p.(type) {
					case ai.TextContent:
						content = append(content, map[string]any{"type": "input_text", "text": v.Text})
					case ai.ImageContent:
						if supportsImage {
							content = append(content, map[string]any{
								"type":      "input_image",
								"detail":    "auto",
								"image_url": fmt.Sprintf("data:%s;base64,%s", v.MimeType, v.Data),
							})
						}
					}
				}
				if len(content) > 0 {
					input = append(input, map[string]any{
						"role":    "user",
						"content": content,
					})
				}
			}
		} else if msg.AsAssistant() != nil {
			am := msg.AsAssistant()
			for _, block := range am.Content {
				if block.IsText() {
					sig := block.Text.TextSignature

					// Replay shell_call / shell_call_output items as raw objects.
					if strings.HasPrefix(sig, "shell:") || strings.HasPrefix(sig, "shell_output:") {
						rawJSON := sig[strings.Index(sig, ":")+1:]
						var item map[string]any
						if err := json.Unmarshal([]byte(rawJSON), &item); err == nil {
							input = append(input, item)
						}
						continue
					}

					parsed := parseTextSignature(sig)
					var msgID string
					if parsed != nil {
						msgID = parsed.ID
					}
					if msgID == "" {
						msgID = fmt.Sprintf("msg_%d", len(input))
					} else if len(msgID) > 64 {
						msgID = "msg_" + shortHash(msgID)
					}
					entry := map[string]any{
						"type":   "message",
						"role":   "assistant",
						"id":     msgID,
						"status": "completed",
						"content": []map[string]any{
							{"type": "output_text", "text": block.Text.Text, "annotations": []any{}},
						},
					}
					if parsed != nil && parsed.Phase != "" {
						entry["phase"] = parsed.Phase
					}
					input = append(input, entry)

				} else if block.IsThinking() {
					if block.Thinking.ThinkingSignature != "" {
						// Re-serialize the reasoning item from the signature
						var item map[string]any
						if err := json.Unmarshal([]byte(block.Thinking.ThinkingSignature), &item); err == nil {
							input = append(input, item)
						}
					}

				} else if block.IsToolCall() {
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
			var textParts []string
			var hasImages bool
			for _, c := range tr.Content {
				if c.IsText() {
					textParts = append(textParts, c.Text)
				} else if c.IsImage() {
					hasImages = true
				}
			}
			hasText := len(textParts) > 0
			text := strings.Join(textParts, "\n")
			idParts := strings.SplitN(tr.ToolCallID, "|", 2)
			callID := idParts[0]

			// Images go inline in the function_call_output when the model supports them.
			// Otherwise fall back to a plain text output string.
			if hasImages && supportsImage {
				var contentParts []map[string]any
				if hasText {
					contentParts = append(contentParts, map[string]any{
						"type": "input_text",
						"text": text,
					})
				}
				for _, c := range tr.Content {
					if c.IsImage() {
						contentParts = append(contentParts, map[string]any{
							"type":      "input_image",
							"detail":    "auto",
							"image_url": fmt.Sprintf("data:%s;base64,%s", c.MimeType, c.Data),
						})
					}
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  contentParts,
				})
			} else {
				if text == "" {
					text = "(see attached image)"
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  text,
				})
			}
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
		apiKey = envkeys.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		return errorStreamProvider(model, noAPIKeyError(model.Provider, apiKeyErrorFromSimpleOpts(options)))
	}

	base := BuildBaseOptions(model, options, apiKey)

	if options != nil && options.Reasoning != "" && model.Reasoning {
		reasoningEffort := ClampReasoningForModel(options.Reasoning, model)
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

// supportsHostedShell returns true if the model supports the OpenAI hosted shell tool.
// Currently only GPT-5+ and o3/o4 models support it.
func supportsHostedShell(model *ai.Model) bool {
	if model == nil {
		return false
	}
	id := strings.ToLower(model.ID)
	if strings.HasPrefix(id, "gpt-5") {
		return true
	}
	if strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") {
		return true
	}
	// Codex models support shell.
	if strings.HasPrefix(id, "codex") {
		return true
	}
	return false
}

// resolvedOpenAIResponsesCompat is the resolved OpenAIResponsesCompat with defaults applied.
type resolvedOpenAIResponsesCompat struct {
	SendSessionIdHeader        bool
	SupportsLongCacheRetention bool
}

// getOpenAIResponsesCompat returns resolved OpenAIResponsesCompat settings for a model.
func getOpenAIResponsesCompat(model *ai.Model) resolvedOpenAIResponsesCompat {
	result := resolvedOpenAIResponsesCompat{
		SendSessionIdHeader:        true,
		SupportsLongCacheRetention: true,
	}
	if compat := model.GetOpenAIResponsesCompat(); compat != nil {
		if compat.SendSessionIdHeader != nil {
			result.SendSessionIdHeader = *compat.SendSessionIdHeader
		}
		if compat.SupportsLongCacheRetention != nil {
			result.SupportsLongCacheRetention = *compat.SupportsLongCacheRetention
		}
	}
	return result
}
