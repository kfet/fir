// Ported from: packages/ai/src/providers/openai-codex-responses.ts
// Upstream hash: 41039e8d
package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/ai/oauth"
	firlog "github.com/kfet/fir/pkg/log"
)

// --- Codex configuration ---

const (
	defaultCodexBaseURL = "https://chatgpt.com/backend-api"
	codexMaxRetries     = 3
	codexBaseDelayMS    = 1000
)

// codexToolCallProviders is the set of providers whose tool call IDs need normalization.
var codexToolCallProviders = map[string]bool{
	"openai":       true,
	"openai-codex": true,
	"opencode":     true,
}

// --- Helpers ---

// extractAccountID extracts the ChatGPT account ID from a JWT token.
func extractAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("failed to extract accountId from token: invalid token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to extract accountId from token: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to extract accountId from token: %w", err)
	}
	authClaim, ok := claims[oauth.JWTClaimPath].(map[string]any)
	if !ok {
		return "", fmt.Errorf("failed to extract accountId from token: no auth claim")
	}
	accountID, ok := authClaim["chatgpt_account_id"].(string)
	if !ok || accountID == "" {
		return "", fmt.Errorf("failed to extract accountId from token: no account ID")
	}
	return accountID, nil
}

// resolveCodexURL resolves the Codex API endpoint URL.
func resolveCodexURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = defaultCodexBaseURL
	}
	raw = strings.TrimRight(raw, "/")
	if strings.HasSuffix(raw, "/codex/responses") {
		return raw
	}
	if strings.HasSuffix(raw, "/codex") {
		return raw + "/responses"
	}
	return raw + "/codex/responses"
}

// clampCodexReasoningEffort adjusts reasoning effort for specific Codex models.
func clampCodexReasoningEffort(modelID, effort string) string {
	id := modelID
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		id = id[idx+1:]
	}
	// No Codex model supports "max" today — clamp up-front so the
	// model-specific rules below only deal with valid effort values.
	if effort == "max" {
		effort = "xhigh"
	}
	switch {
	case (strings.HasPrefix(id, "gpt-5.2") || strings.HasPrefix(id, "gpt-5.3") || strings.HasPrefix(id, "gpt-5.4")) && effort == "minimal":
		return "low"
	case id == "gpt-5.1" && effort == "xhigh":
		return "high"
	case id == "gpt-5.1-codex-mini":
		if effort == "high" || effort == "xhigh" {
			return "high"
		}
		return "medium"
	default:
		return effort
	}
}

// isRetryableCodexError checks if an HTTP status/error is retryable.
func isRetryableCodexError(statusCode int, errorText string) bool {
	switch statusCode {
	case 429, 500, 502, 503, 504:
		return true
	}
	lower := strings.ToLower(errorText)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "service unavailable") ||
		strings.Contains(lower, "upstream connect") ||
		strings.Contains(lower, "connection refused")
}

// --- Stream function ---

// StreamOpenAICodexResponses implements streaming for the OpenAI Codex Responses API.
func StreamOpenAICodexResponses(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
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

		accountID, err := extractAccountID(apiKey)
		if err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		body, err := buildCodexRequestBody(model, prompt, options)
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

		url := resolveCodexURL(model.BaseURL)
		firlog.Debug("codex request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))

		sseHeaders := buildCodexSSEHeaders(model.Headers, options, accountID, apiKey)
		wsHeaders := buildCodexWebSocketHeaders(model.Headers, options, accountID, apiKey)

		// Determine transport preference
		transport := ai.TransportSSE
		if options != nil && options.Transport != "" {
			transport = options.Transport
		}

		// Try WebSocket transport if requested
		if transport != ai.TransportSSE {
			wsURL := resolveCodexWebSocketURL(model.BaseURL)
			wsStarted, wsErr := processWebSocketStream(ctx, wsURL, body, wsHeaders, output, stream, model, options)
			if wsStarted || transport == ai.TransportWebSocket {
				if wsErr != nil {
					output.StopReason = ai.StopReasonError
					output.ErrorMessage = wsErr.Error()
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
					return
				}
				if ctx.Err() != nil {
					output.StopReason = ai.StopReasonAborted
					output.ErrorMessage = "Request was aborted"
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonAborted, Error: output})
					return
				}
				stream.Push(ai.AssistantMessageEvent{
					Type:    ai.EventDone,
					Reason:  output.StopReason,
					Message: output,
				})
				return
			}
			// transport == "auto" and WS failed before start: fall through to SSE
		}

		// Retry loop for transient errors (SSE path)
		var sseEvents <-chan SSEEvent
		var sseErr <-chan error
		var lastErr error

		for attempt := 0; attempt <= codexMaxRetries; attempt++ {
			if ctx != nil {
				select {
				case <-ctx.Done():
					output.StopReason = ai.StopReasonAborted
					output.ErrorMessage = "Request was aborted"
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonAborted, Error: output})
					return
				default:
				}
			}

			sseEvents, sseErr = DefaultSSEClient.Stream(ctx, url, sseHeaders, bytes.NewReader(body))

			// Check if SSE setup succeeded by trying to read first event
			// For now, we proceed and handle errors in the event loop
			break // SSE client handles connection errors via sseErr channel
		}

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

		// Use shared processor for the event stream, but map Codex-specific events
		proc := &responsesSSEProcessor{output: output, stream: stream, model: model}

		for evt := range sseEvents {
			if evt.Data == "" || evt.Data == "[DONE]" {
				continue
			}

			// Map Codex-specific events to standard OpenAI Responses events
			data := mapCodexEvent(evt.Data)
			if data == "" {
				continue
			}

			done, err := proc.processEvent(data)
			if err != nil {
				lastErr = err
				break
			}
			if done {
				break
			}
		}

		// Check for SSE errors
		if lastErr == nil {
			select {
			case err := <-sseErr:
				if err != nil {
					lastErr = err
				}
			default:
			}
		}

		if lastErr != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = lastErr.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		firlog.Debug("codex response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
	}()

	return stream
}

// mapCodexEvent maps Codex-specific SSE events to standard OpenAI Responses events.
func mapCodexEvent(data string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return ""
	}
	return mapCodexEventFromMap(raw)
}

// normalizeCodexStatus normalizes a Codex response status.
func normalizeCodexStatus(status string) string {
	switch status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
		return status
	default:
		return ""
	}
}

// --- Request body ---

func buildCodexRequestBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) ([]byte, error) {
	// Build messages without system prompt (Codex uses "instructions" field)
	input := convertResponsesInputNoSystem(model, ctx)

	body := map[string]any{
		"model":               model.ID,
		"store":               false,
		"stream":              true,
		"instructions":        ctx.SystemPrompt,
		"input":               input,
		"text":                map[string]any{"verbosity": "medium"},
		"include":             []string{"reasoning.encrypted_content"},
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
	}

	if options != nil && options.SessionID != "" {
		body["prompt_cache_key"] = options.SessionID
	}

	if options != nil && options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}

	if len(ctx.Tools) > 0 {
		// Codex uses strict: null (we omit strict field)
		var tools []map[string]any
		for _, tool := range ctx.Tools {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			})
		}
		body["tools"] = tools
	}

	if options != nil && string(options.ReasoningEffort) != "" && model.Reasoning {
		body["reasoning"] = map[string]any{
			"effort":  clampCodexReasoningEffort(model.ID, string(options.ReasoningEffort)),
			"summary": "auto",
		}
	}

	return json.Marshal(body)
}

// convertResponsesInputNoSystem converts messages to OpenAI Responses format without system prompt.
func convertResponsesInputNoSystem(model *ai.Model, ctx ai.Context) []any {
	// Save system prompt, clear it, convert, restore
	origSystem := ctx.SystemPrompt
	ctx.SystemPrompt = ""
	input := convertResponsesInput(model, ctx)
	ctx.SystemPrompt = origSystem
	return input
}

// --- Headers ---

func buildCodexBaseHeaders(modelHeaders map[string]string, options *ai.StreamOptions, accountID, apiKey string) map[string]string {
	// Use a temporary model to leverage BuildRequestHeaders merge order.
	tmpModel := &ai.Model{Headers: modelHeaders}
	headers := BuildRequestHeaders(
		map[string]string{
			"Authorization":      "Bearer " + apiKey,
			"chatgpt-account-id": accountID,
			"originator":         "fir",
			"User-Agent":         fmt.Sprintf("fir (%s %s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version()),
			"accept":             "text/event-stream",
			"content-type":       "application/json",
		},
		tmpModel, options,
	)
	return headers
}

func buildCodexSSEHeaders(modelHeaders map[string]string, options *ai.StreamOptions, accountID, apiKey string) map[string]string {
	headers := buildCodexBaseHeaders(modelHeaders, options, accountID, apiKey)
	headers["OpenAI-Beta"] = "responses=experimental"
	if options != nil && options.SessionID != "" {
		headers["session_id"] = options.SessionID
	}
	return headers
}

func buildCodexWebSocketHeaders(modelHeaders map[string]string, options *ai.StreamOptions, accountID, apiKey string) map[string]string {
	headers := buildCodexBaseHeaders(modelHeaders, options, accountID, apiKey)
	// WebSocket requests must NOT include OpenAI-Beta header
	if options != nil && options.SessionID != "" {
		headers["session_id"] = options.SessionID
	}
	return headers
}

// --- Simple wrapper ---

// StreamSimpleOpenAICodexResponses wraps StreamOpenAICodexResponses with SimpleStreamOptions.
func StreamSimpleOpenAICodexResponses(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
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

	return StreamOpenAICodexResponses(ctx, model, prompt, base)
}

// RegisterOpenAICodexResponses registers the OpenAI Codex Responses provider.
func RegisterOpenAICodexResponses(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiOpenAICodexResponses,
		Stream:       StreamOpenAICodexResponses,
		StreamSimple: StreamSimpleOpenAICodexResponses,
	}, "builtin")
}
