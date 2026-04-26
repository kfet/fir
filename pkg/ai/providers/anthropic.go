// Ported from: packages/ai/src/providers/anthropic.ts
// Upstream hash: 48aa882
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/ai/jsonparse"
	"github.com/kfet/fir/pkg/ai/ratelimit"
	firlog "github.com/kfet/fir/pkg/log"
)

// Tool-name translation for the anthropic OAuth (Claude Pro/Max) path.
//
// Claude Code's OAuth endpoint expects the canonical tool names it ships
// with (e.g. "Read", "Bash", "KillShell"). fir's built-in tools use their
// own names ("read", "bash", "bash_kill", ...). The mapping between the
// two is *not* baked into this file — it is supplied at handshake time by
// the anthropic-auth builtin extension via RegisterToolNameAliases, so
// all the Claude-Code-specific naming lives next to the OAuth flow.
//
// ccToolLookup is kept (empty by default) for the unit tests and for any
// future provider-adapter that wants to add a hardcoded baseline.
var ccToolLookup = map[string]string{}

var (
	// extToolAliases holds per-extension alias maps registered via
	// RegisterToolNameAliases. Keys are extension names; values map our
	// tool name (lowercased) → canonical provider-side tool name.
	extToolAliasesMu sync.RWMutex
	extToolAliases   = map[string]map[string]string{}
)

// RegisterToolNameAliases registers a static mapping from fir tool names to
// canonical provider-side (Claude-Code) tool names for the given extension.
// It is intended to be called once per extension at handshake time by the
// extension manager; subsequent calls for the same extName replace the
// previous map.
//
// Keys are fir tool names (e.g. "bash_kill", "read"); values are the
// canonical names expected by the provider (e.g. "KillShell", "Read").
// The map is consulted by toClaudeCodeName (outbound: fir → LLM) and
// fromClaudeCodeName (inbound: LLM → fir) so tool-name translation
// round-trips correctly regardless of which tool subset a session has.
func RegisterToolNameAliases(extName string, aliases map[string]string) {
	extToolAliasesMu.Lock()
	defer extToolAliasesMu.Unlock()
	if len(aliases) == 0 {
		delete(extToolAliases, extName)
		return
	}
	cp := make(map[string]string, len(aliases))
	for k, v := range aliases {
		if k == "" || v == "" {
			continue
		}
		cp[strings.ToLower(k)] = v
	}
	extToolAliases[extName] = cp
}

// UnregisterToolNameAliases removes a previously-registered alias map.
// Safe to call when no map was registered.
func UnregisterToolNameAliases(extName string) {
	extToolAliasesMu.Lock()
	delete(extToolAliases, extName)
	extToolAliasesMu.Unlock()
}

// lookupToolAlias consults the extension-registered alias maps for a
// canonical provider-side tool name. Returns "" when no alias is registered.
func lookupToolAlias(ourName string) string {
	key := strings.ToLower(ourName)
	extToolAliasesMu.RLock()
	defer extToolAliasesMu.RUnlock()
	for _, m := range extToolAliases {
		if cc, ok := m[key]; ok {
			return cc
		}
	}
	return ""
}

// lookupToolAliasReverse returns the fir tool name for a given canonical
// provider-side name, if one is registered. Returns "" when no match.
func lookupToolAliasReverse(ccName string) string {
	extToolAliasesMu.RLock()
	defer extToolAliasesMu.RUnlock()
	for _, m := range extToolAliases {
		for ourLower, cc := range m {
			if strings.EqualFold(cc, ccName) {
				return ourLower
			}
		}
	}
	return ""
}

// anthropicPrefixGuards tracks prefix stability per session.
var anthropicPrefixGuards sync.Map // sessionID → *PrefixGuard

func toClaudeCodeName(name string) string {
	if cc, ok := ccToolLookup[strings.ToLower(name)]; ok {
		return cc
	}
	if cc := lookupToolAlias(name); cc != "" {
		return cc
	}
	return name
}

func fromClaudeCodeName(name string, tools []ai.Tool) string {
	lower := strings.ToLower(name)
	for _, t := range tools {
		if strings.ToLower(t.Name) == lower {
			return t.Name
		}
	}
	// Reverse alias lookup: Claude Code name → our tool name.
	// Uses extension-registered alias maps so tools whose fir name differs
	// from the canonical Claude-Code name (e.g. bash_kill / KillShell)
	// still resolve correctly when the session has the fir tool.
	if ours := lookupToolAliasReverse(name); ours != "" {
		for _, t := range tools {
			if strings.EqualFold(t.Name, ours) {
				return t.Name
			}
		}
	}
	return name
}

// resolveCacheRetention resolves the cache retention preference.
func resolveCacheRetention(cr ai.CacheRetention) ai.CacheRetention {
	if cr != "" {
		return cr
	}
	if os.Getenv("FIR_CACHE_RETENTION") == "long" {
		return ai.CacheLong
	}
	return ai.CacheShort
}

// isOAuthModel returns true if the model has been tagged with OAuth headers
// by the anthropic auth extension (via auth_modify_models).
func isOAuthModel(model *ai.Model) bool {
	return model != nil && model.Headers != nil && model.Headers["x-anthropic-oauth-beta-prefix"] != ""
}

// isAuthError returns true if the error indicates an authentication failure
// (e.g. expired OAuth token) that may be resolved by refreshing credentials.
func isAuthError(errType, errMsg string) bool {
	if errType == "authentication_error" || errType == "permission_error" {
		return true
	}
	lower := strings.ToLower(errMsg)
	return strings.Contains(lower, "401") ||
		strings.Contains(lower, "invalid authentication") ||
		strings.Contains(lower, "invalid x-api-key") ||
		strings.Contains(lower, "invalid api key")
}

func supportsAdaptiveThinking(modelID string) bool {
	return strings.Contains(modelID, "opus-4-6") || strings.Contains(modelID, "opus-4.6") ||
		strings.Contains(modelID, "opus-4-7") || strings.Contains(modelID, "opus-4.7") ||
		strings.Contains(modelID, "sonnet-4-6") || strings.Contains(modelID, "sonnet-4.6")
}

func matchesServerToolCapability(model *ai.Model, toolType string) bool {
	if model == nil {
		return false
	}
	if len(model.ServerTools) == 0 {
		// Backward-compatible default for custom models/providers that haven't declared capabilities yet.
		return true
	}
	if model.SupportsServerTool(toolType) {
		return true
	}
	// Accept short-name capabilities too.
	base := toolType
	if idx := strings.LastIndex(toolType, "_"); idx > 0 {
		base = toolType[:idx]
	}
	return model.SupportsServerTool(base)
}

func supportsModelCompaction(model *ai.Model) bool {
	if model == nil {
		return false
	}
	if model.Compaction {
		return true
	}
	// Legacy fallback until all built-ins include explicit capability metadata.
	if len(model.ServerTools) == 0 {
		id := model.ID
		return strings.Contains(id, "opus-4-6") || strings.Contains(id, "opus-4.6") ||
			strings.Contains(id, "sonnet-4-6") || strings.Contains(id, "sonnet-4.6")
	}
	return false
}

// mapThinkingLevelToEffort maps a ThinkingLevel to the Anthropic adaptive
// thinking "effort" header value. This is only consulted for models that
// pass supportsAdaptiveThinking — they all support "max"; only Opus 4.7
// (and later xhigh-aware models) keep a distinct "xhigh" effort, others
// clamp xhigh down to "high".
func mapThinkingLevelToEffort(level ai.ThinkingLevel, modelID string) string {
	switch level {
	case ai.ThinkingMinimal, ai.ThinkingLow:
		return "low"
	case ai.ThinkingMedium:
		return "medium"
	case ai.ThinkingHigh:
		return "high"
	case ai.ThinkingXHigh:
		if strings.Contains(modelID, "opus-4-7") || strings.Contains(modelID, "opus-4.7") {
			return "xhigh"
		}
		return "high"
	case ai.ThinkingMax:
		return "max"
	default:
		return "high"
	}
}

func mapAnthropicStopReason(reason string) ai.StopReason {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence", "compaction":
		return ai.StopReasonStop
	case "max_tokens":
		return ai.StopReasonLength
	case "tool_use":
		return ai.StopReasonToolUse
	case "refusal", "sensitive":
		return ai.StopReasonError
	default:
		return ai.StopReasonError
	}
}

// normalizeAnthropicToolCallID normalizes tool call IDs for Anthropic's format.
func normalizeAnthropicToolCallID(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
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

// anthropicRetryDelayFn may be replaced in tests to avoid real sleeps.
// The signature matches anthropicRetryDelay so tests can inject zero delays.
var anthropicRetryDelayFn func(errMsg string, attempt int, maxDelayMs *int) time.Duration

// anthropicRetryDelay returns the wait duration before retry attempt n (0-based).
// It honours any server-provided delay in the error message, then falls back to
// exponential backoff (5 s, 10 s, 20 s, …) capped by maxDelayMs when set.
func anthropicRetryDelay(errMsg string, attempt int, maxDelayMs *int) time.Duration {
	if anthropicRetryDelayFn != nil {
		return anthropicRetryDelayFn(errMsg, attempt, maxDelayMs)
	}
	delay := ratelimit.ExtractRetryDelayFromText(errMsg)
	if delay == 0 {
		secs := 5 * math.Pow(2, float64(attempt))
		delay = time.Duration(secs * float64(time.Second))
	}
	cap := 2 * time.Minute
	if maxDelayMs != nil && *maxDelayMs > 0 {
		cap = time.Duration(*maxDelayMs) * time.Millisecond
	}
	if delay > cap {
		delay = cap
	}
	return delay
}

// StreamAnthropic is the raw Anthropic streaming function.
// It automatically retries on overloaded / rate-limit errors (HTTP 529 or SSE
// overloaded_error) up to maxAnthropicRetries times using exponential backoff.
// EventStart is withheld until the first successful SSE event so that retried
// attempts are completely transparent to callers.
const maxAnthropicRetries = 5

func StreamAnthropic(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()

	go func() {
		defer stream.End(nil)

		apiKey := ""
		if options != nil {
			apiKey = options.ApiKey
		}
		if apiKey == "" {
			apiKey = envkeys.GetEnvApiKey(model.Provider)
		}
		if apiKey == "" {
			out := &ai.AssistantMessage{
				Role:         ai.RoleAssistant,
				Content:      []ai.AssistantContent{},
				Api:          model.Api,
				Provider:     model.Provider,
				Model:        model.ID,
				Usage:        ai.ZeroUsage(),
				StopReason:   ai.StopReasonError,
				ErrorMessage: noAPIKeyError(model.Provider, apiKeyErrorFromOpts(options)),
				Timestamp:    time.Now().UnixMilli(),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: out})
			return
		}

		oauthToken := isOAuthModel(model)
		params := buildAnthropicParams(model, prompt, oauthToken, options)
		if options != nil && options.OnPayload != nil {
			if next := options.OnPayload(params, model); next != nil {
				if m, ok := next.(map[string]any); ok {
					params = m
				}
			}
		}

		payload, err := json.Marshal(params)
		if err != nil {
			out := &ai.AssistantMessage{
				Role:         ai.RoleAssistant,
				Content:      []ai.AssistantContent{},
				Api:          model.Api,
				Provider:     model.Provider,
				Model:        model.ID,
				Usage:        ai.ZeroUsage(),
				StopReason:   ai.StopReasonError,
				ErrorMessage: fmt.Sprintf("marshal request: %v", err),
				Timestamp:    time.Now().UnixMilli(),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: out})
			return
		}

		url := strings.TrimRight(model.BaseURL, "/") + "/v1/messages"
		headers := buildAnthropicHeaders(model, apiKey, oauthToken, options, len(prompt.Tools) > 0)

		firlog.Debug("anthropic request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))

		var maxDelayMs *int
		if options != nil {
			maxDelayMs = options.MaxRetryDelayMs
		}

		lastErrMsg := ""

		for attempt := 0; attempt < maxAnthropicRetries; attempt++ {
			if attempt > 0 {
				delay := anthropicRetryDelay(lastErrMsg, attempt-1, maxDelayMs)
				firlog.Info("anthropic overloaded, retrying", "attempt", attempt, "delay", delay, "error", lastErrMsg)
				select {
				case <-ctx.Done():
					out := &ai.AssistantMessage{
						Role: ai.RoleAssistant, Content: []ai.AssistantContent{},
						Api: model.Api, Provider: model.Provider, Model: model.ID,
						Usage: ai.ZeroUsage(), StopReason: ai.StopReasonError,
						ErrorMessage: lastErrMsg, Timestamp: time.Now().UnixMilli(),
					}
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: out})
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: out})
					return
				case <-time.After(delay):
				}
			}

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

			sseEvents, sseErr := DefaultSSEClient.Stream(ctx, url, headers, bytes.NewReader(payload))

			// EventStart is emitted only after the first successful SSE event so that
			// an early overloaded_error can be retried without the caller seeing anything.
			startEmitted := false

			// Track block indices from Anthropic to our content array indices.
			type blockInfo struct {
				contentIdx  int
				partialJSON string
			}
			blocks := map[int]*blockInfo{}

			retryNeeded := false

		sseLoop:
			for evt := range sseEvents {
				if evt.Data == "" || evt.Data == "[DONE]" {
					continue
				}
				var raw map[string]any
				if err := json.Unmarshal([]byte(evt.Data), &raw); err != nil {
					continue
				}

				eventType, _ := raw["type"].(string)

				// Emit EventStart on the first non-error event.
				if !startEmitted && eventType != "error" {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
					startEmitted = true
				}

				switch eventType {
				case "message_start":
					if msg, ok := raw["message"].(map[string]any); ok {
						if id, ok := msg["id"].(string); ok && output.ResponseID == "" {
							output.ResponseID = id
						}
						if usage, ok := msg["usage"].(map[string]any); ok {
							updateAnthropicUsage(output, usage, model)
						}
					}

				case "content_block_start":
					idx := jsonInt(raw, "index")
					cb, _ := raw["content_block"].(map[string]any)
					if cb == nil {
						continue
					}
					blockType, _ := cb["type"].(string)
					contentIdx := len(output.Content)

					switch blockType {
					case "text":
						output.Content = append(output.Content, ai.NewTextContent(""))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
					case "thinking":
						output.Content = append(output.Content, ai.NewThinkingContent(""))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingStart, ContentIndex: contentIdx, Partial: output})
					case "redacted_thinking":
						// Redacted thinking: the full encrypted payload is in cb["data"].
						// There are no deltas — all content arrives in content_block_start.
						data, _ := cb["data"].(string)
						t := ai.NewThinkingContent("")
						t.Thinking.Redacted = true
						t.Thinking.ThinkingSignature = data
						output.Content = append(output.Content, t)
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingStart, ContentIndex: contentIdx, Partial: output})
					case "tool_use":
						toolID, _ := cb["id"].(string)
						toolName, _ := cb["name"].(string)
						if oauthToken {
							toolName = fromClaudeCodeName(toolName, prompt.Tools)
						}
						output.Content = append(output.Content, ai.NewToolCallContent(toolID, toolName, map[string]any{}))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallStart, ContentIndex: contentIdx, Partial: output})
					case "server_tool_use":
						// Server-side tool invocation (web_search, code_execution, etc.)
						// We emit a text block showing the tool is running.
						output.Content = append(output.Content, ai.NewTextContent(""))
						blocks[idx] = &blockInfo{contentIdx: contentIdx}
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
						continue // skip default blocks[idx] assignment below
					case "web_search_tool_result":
						// Server-side web search results — format as text summary.
						text := formatWebSearchResult(cb)
						output.Content = append(output.Content, ai.NewTextContent(text))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: contentIdx, Delta: text, Partial: output})
					case "code_execution_tool_result":
						// Server-side code execution results — format as text.
						text := formatCodeExecutionResult(cb)
						output.Content = append(output.Content, ai.NewTextContent(text))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: contentIdx, Delta: text, Partial: output})
					case "web_fetch_tool_result":
						// Server-side web fetch results — format as text.
						text := formatWebFetchResult(cb)
						output.Content = append(output.Content, ai.NewTextContent(text))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: contentIdx, Delta: text, Partial: output})
					case "tool_invocation":
						// Programmatic tool calling — server-side tool invocation.
						// These are informational; the API handles execution.
						toolName, _ := cb["tool_name"].(string)
						text := fmt.Sprintf("[calling %s]\n", toolName)
						output.Content = append(output.Content, ai.NewTextContent(text))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: contentIdx, Delta: text, Partial: output})
					case "tool_output":
						// Programmatic tool calling — server-side tool result.
						text := formatToolOutput(cb)
						output.Content = append(output.Content, ai.NewTextContent(text))
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: contentIdx, Partial: output})
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: contentIdx, Delta: text, Partial: output})
					}
					blocks[idx] = &blockInfo{contentIdx: contentIdx}

				case "content_block_delta":
					idx := jsonInt(raw, "index")
					bi := blocks[idx]
					if bi == nil {
						continue
					}
					delta, _ := raw["delta"].(map[string]any)
					if delta == nil {
						continue
					}
					deltaType, _ := delta["type"].(string)
					ci := bi.contentIdx

					switch deltaType {
					case "text_delta":
						text, _ := delta["text"].(string)
						if ci < len(output.Content) && output.Content[ci].IsText() {
							output.Content[ci].Text.Text += text
						}
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: ci, Delta: text, Partial: output})
					case "thinking_delta":
						thinking, _ := delta["thinking"].(string)
						if ci < len(output.Content) && output.Content[ci].IsThinking() {
							output.Content[ci].Thinking.Thinking += thinking
						}
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingDelta, ContentIndex: ci, Delta: thinking, Partial: output})
					case "input_json_delta":
						partial, _ := delta["partial_json"].(string)
						bi.partialJSON += partial
						if ci < len(output.Content) && output.Content[ci].IsToolCall() {
							output.Content[ci].ToolCall.Arguments = jsonparse.ParseStreamingJSON(bi.partialJSON)
						}
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallDelta, ContentIndex: ci, Delta: partial, Partial: output})
					case "signature_delta":
						sig, _ := delta["signature"].(string)
						if ci < len(output.Content) && output.Content[ci].IsThinking() {
							output.Content[ci].Thinking.ThinkingSignature += sig
						}
					}

				case "content_block_stop":
					idx := jsonInt(raw, "index")
					bi := blocks[idx]
					if bi == nil {
						continue
					}
					ci := bi.contentIdx
					if ci >= len(output.Content) {
						continue
					}
					c := &output.Content[ci]
					if c.IsText() {
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextEnd, ContentIndex: ci, Content: c.Text.Text, Partial: output})
					} else if c.IsThinking() {
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingEnd, ContentIndex: ci, Content: c.Thinking.Thinking, Partial: output})
					} else if c.IsToolCall() {
						c.ToolCall.Arguments = jsonparse.ParseStreamingJSON(bi.partialJSON)
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallEnd, ContentIndex: ci, ToolCall: c.ToolCall, Partial: output})
					}

				case "message_delta":
					if d, ok := raw["delta"].(map[string]any); ok {
						if sr, ok := d["stop_reason"].(string); ok {
							output.StopReason = mapAnthropicStopReason(sr)
						}
					}
					if usage, ok := raw["usage"].(map[string]any); ok {
						updateAnthropicUsage(output, usage, model)
					}

				case "error":
					errObj, _ := raw["error"].(map[string]any)
					errMsg, _ := errObj["message"].(string)
					if errMsg == "" {
						errMsg = "unknown Anthropic API error"
					}
					errType, _ := errObj["type"].(string)
					if errType != "" {
						errMsg = fmt.Sprintf("%s (%s)", errMsg, errType)
					}
					firlog.Warn("anthropic error", "type", errType, "err", errMsg)

					// Auth error (e.g. expired OAuth token) — refresh the key and retry.
					if !startEmitted && attempt < maxAnthropicRetries-1 && isAuthError(errType, errMsg) {
						refreshed := ""
						if options != nil && options.RefreshApiKey != nil {
							refreshed = options.RefreshApiKey(model.Provider)
						}
						if refreshed == "" {
							refreshed = envkeys.GetEnvApiKey(model.Provider)
						}
						if refreshed != "" && refreshed != apiKey {
							firlog.Info("anthropic auth error, refreshed token", "attempt", attempt)
							apiKey = refreshed
							headers = buildAnthropicHeaders(model, apiKey, oauthToken, options, len(prompt.Tools) > 0)
							lastErrMsg = errMsg
							retryNeeded = true
							break sseLoop
						}
					}

					// Retry transparently if streaming hasn't started yet and the
					// error is retryable (rate-limit or transient server error).
					if !startEmitted && attempt < maxAnthropicRetries-1 && ratelimit.IsRetryableError(errMsg) {
						lastErrMsg = errMsg
						retryNeeded = true
						break sseLoop
					}

					// Not retryable (or already streaming): surface the error.
					output.StopReason = ai.StopReasonError
					output.ErrorMessage = errMsg
					if !startEmitted {
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
					}
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
					return
				}
			}

			// Drain any remaining SSE events so the upstream goroutine can exit
			// cleanly.  In the normal path the for-range above already drained the
			// channel; in the early-break (retryNeeded) path this unblocks the
			// goroutine so it can close errCh and we can receive below.
			for range sseEvents {
			}

			// Collect the SSE-layer error (nil on clean end-of-stream).
			sseErrVal := <-sseErr

			if retryNeeded {
				// Overloaded SSE error before streaming began — loop to next attempt.
				continue
			}

			// Check for HTTP-level errors (e.g. 529 Overloaded, 401 Auth).
			if sseErrVal != nil {
				sseStatusCode := 0
				if sseErr, ok := sseErrVal.(*SSEError); ok {
					sseStatusCode = sseErr.StatusCode
					firlog.Warn("anthropic HTTP error",
						"status", sseErr.StatusCode,
						"message", sseErr.Message,
						"request_id", sseErr.RequestID,
						"body", sseErr.RawBody,
					)
				} else {
					firlog.Warn("anthropic SSE error", "err", sseErrVal)
				}
				errMsg := sseErrVal.Error()

				// Auth error at HTTP level — refresh token and retry.
				if !startEmitted && attempt < maxAnthropicRetries-1 && (sseStatusCode == 401 || sseStatusCode == 403) {
					refreshed := ""
					if options != nil && options.RefreshApiKey != nil {
						refreshed = options.RefreshApiKey(model.Provider)
					}
					if refreshed == "" {
						refreshed = envkeys.GetEnvApiKey(model.Provider)
					}
					if refreshed != "" && refreshed != apiKey {
						firlog.Info("anthropic HTTP auth error, refreshed token", "status", sseStatusCode, "attempt", attempt)
						apiKey = refreshed
						headers = buildAnthropicHeaders(model, apiKey, oauthToken, options, len(prompt.Tools) > 0)
						lastErrMsg = errMsg
						continue
					}
				}

				if !startEmitted && attempt < maxAnthropicRetries-1 && ratelimit.IsRetryableError(errMsg) {
					lastErrMsg = errMsg
					continue
				}
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = errMsg
				if !startEmitted {
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return
			}

			firlog.Debug("anthropic response complete", "model", model.ID, "stopReason", output.StopReason)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: output.StopReason, Message: output})
			return
		}

		// All retries exhausted — emit the last error.
		out := &ai.AssistantMessage{
			Role: ai.RoleAssistant, Content: []ai.AssistantContent{},
			Api: model.Api, Provider: model.Provider, Model: model.ID,
			Usage: ai.ZeroUsage(), StopReason: ai.StopReasonError,
			ErrorMessage: lastErrMsg, Timestamp: time.Now().UnixMilli(),
		}
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: out})
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: out})
	}()

	return stream
}

// StreamSimpleAnthropic is the simplified Anthropic streaming function with reasoning support.
func StreamSimpleAnthropic(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.ApiKey
	}
	if apiKey == "" {
		apiKey = envkeys.GetEnvApiKey(model.Provider)
	}

	base := BuildBaseOptions(model, options, apiKey)

	if options == nil || options.Reasoning == "" {
		return StreamAnthropic(ctx, model, prompt, base)
	}

	// Explicitly disable thinking when requested on a reasoning model
	if options.Reasoning == ai.ThinkingOff && model.Reasoning {
		if base.Headers == nil {
			base.Headers = map[string]string{}
		}
		base.Headers["x-anthropic-thinking-disabled"] = "true"
		return StreamAnthropic(ctx, model, prompt, base)
	}

	// For Opus 4.6+: adaptive thinking
	if supportsAdaptiveThinking(model.ID) {
		// Pass effort via header (custom extension)
		if base.Headers == nil {
			base.Headers = map[string]string{}
		}
		base.Headers["x-anthropic-thinking-effort"] = mapThinkingLevelToEffort(options.Reasoning, model.ID)
		return StreamAnthropic(ctx, model, prompt, base)
	}

	// Budget-based thinking for older models
	maxTokens := 0
	if base.MaxTokens != nil {
		maxTokens = *base.MaxTokens
	}
	adjustedMax, thinkingBudget := AdjustMaxTokensForThinking(
		maxTokens, model.MaxTokens, options.Reasoning, options.ThinkingBudgets,
	)
	base.MaxTokens = &adjustedMax

	// Store thinking config in headers for buildAnthropicParams to pick up
	if base.Headers == nil {
		base.Headers = map[string]string{}
	}
	base.Headers["x-anthropic-thinking-enabled"] = "true"
	base.Headers["x-anthropic-thinking-budget"] = fmt.Sprintf("%d", thinkingBudget)

	return StreamAnthropic(ctx, model, prompt, base)
}

// RegisterAnthropic registers the Anthropic provider in the given registry.
func RegisterAnthropic(r *ai.Registry) {
	r.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiAnthropicMessages,
		Stream:       StreamAnthropic,
		StreamSimple: StreamSimpleAnthropic,
	}, "builtin")
}

// --- Internal helpers ---

// isPoeAnthropic reports whether the model routes Anthropic /v1/messages
// calls through Poe (api.poe.com). Poe proxies to Bedrock and does not
// accept Anthropic beta headers or the x-api-key auth scheme; it expects
// Bearer auth with the Poe API key / OAuth access token.
func isPoeAnthropic(model *ai.Model) bool {
	if model == nil {
		return false
	}
	return model.Provider == "poe" || strings.Contains(model.BaseURL, "poe.com")
}

func buildAnthropicHeaders(model *ai.Model, apiKey string, oauthToken bool, options *ai.StreamOptions, hasTools bool) map[string]string {
	if isPoeAnthropic(model) {
		// Poe-specific: Bearer auth, no x-api-key, no anthropic-beta.
		// Poe proxies to Bedrock which rejects unknown beta flags, and
		// Anthropic's OAuth/system-prefix machinery does not apply.
		hdrs := map[string]string{
			"accept":            "application/json",
			"content-type":      "application/json",
			"anthropic-version": "2023-06-01",
			"authorization":     "Bearer " + apiKey,
		}
		filteredModel := &ai.Model{Headers: make(map[string]string, len(model.Headers))}
		for k, v := range model.Headers {
			if k == "x-anthropic-oauth-beta-prefix" || k == "x-anthropic-oauth-system-prefix" {
				continue
			}
			if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "x-api-key") || strings.EqualFold(k, "anthropic-beta") {
				continue
			}
			filteredModel.Headers[k] = v
		}
		return BuildRequestHeaders(hdrs, filteredModel, options, "x-anthropic-thinking-")
	}

	betaFeatures := []string{}

	// Fine-grained tool streaming: use legacy beta header only when
	// the provider doesn't support per-tool eager_input_streaming.
	if hasTools && !getAnthropicCompat(model).SupportsEagerToolInputStreaming {
		betaFeatures = append(betaFeatures, "fine-grained-tool-streaming-2025-05-14")
	}

	// Interleaved thinking beta (only for non-adaptive-thinking models)
	if options != nil && options.Headers != nil {
		if options.Headers["x-anthropic-thinking-disabled"] == "true" ||
			options.Headers["x-anthropic-thinking-enabled"] == "true" ||
			options.Headers["x-anthropic-thinking-effort"] != "" {
			if !supportsAdaptiveThinking(model.ID) {
				betaFeatures = append(betaFeatures, "interleaved-thinking-2025-05-14")
			}
		}
	}

	// Add server tool betas if needed.
	if options != nil {
		seen := map[string]bool{}
		for _, st := range options.ServerTools {
			if !matchesServerToolCapability(model, st.Type) {
				continue
			}
			var beta string
			switch {
			case strings.HasPrefix(st.Type, "web_search"):
				beta = "web-search-2025-03-05"
			case strings.HasPrefix(st.Type, "web_fetch"):
				beta = "web-fetch-2025-09-10"
			case strings.HasPrefix(st.Type, "code_execution"):
				beta = "code-execution-2025-08-25"
			}
			if beta != "" && !seen[beta] {
				seen[beta] = true
				betaFeatures = append(betaFeatures, beta)
			}
		}
		if options.Compaction != nil && options.Compaction.Enabled && supportsModelCompaction(model) {
			betaFeatures = append(betaFeatures, "compact-2026-01-12")
		}
	}

	// Build auth headers based on auth type
	authHeaders := map[string]string{
		"accept":            "application/json",
		"anthropic-version": "2023-06-01",
		"anthropic-dangerous-direct-browser-access": "true",
	}

	betaStr := strings.Join(betaFeatures, ",")

	if oauthToken {
		// OAuth beta prefix is set by the auth extension via model headers.
		oauthBetaPrefix := model.Headers["x-anthropic-oauth-beta-prefix"]
		if oauthBetaPrefix != "" {
			authHeaders["anthropic-beta"] = oauthBetaPrefix + "," + betaStr
		} else {
			authHeaders["anthropic-beta"] = betaStr
		}
		// Use the apiKey (resolved fresh via GetApiKey with auto-refresh)
		// as a Bearer token. This avoids using a stale token baked into
		// model.Headers at startup.
		authHeaders["authorization"] = "Bearer " + apiKey
	} else {
		authHeaders["anthropic-beta"] = betaStr
		authHeaders["x-api-key"] = apiKey
	}

	// Build final headers using the standard merge order, but filter
	// internal marker headers from model and thinking headers from options.
	// We create a filtered copy of model headers to exclude internal markers.
	filteredModel := &ai.Model{Headers: make(map[string]string, len(model.Headers))}
	for k, v := range model.Headers {
		if k == "x-anthropic-oauth-beta-prefix" || k == "x-anthropic-oauth-system-prefix" {
			continue
		}
		// Skip authorization from model headers — it's set fresh from
		// GetApiKey above to ensure token refresh is honoured.
		if oauthToken && strings.EqualFold(k, "authorization") {
			continue
		}
		filteredModel.Headers[k] = v
	}

	return BuildRequestHeaders(authHeaders, filteredModel, options, "x-anthropic-thinking-")
}

func buildAnthropicParams(model *ai.Model, ctx ai.Context, oauthToken bool, options *ai.StreamOptions) map[string]any {
	retention := resolveCacheRetention("")
	if options != nil {
		retention = resolveCacheRetention(options.CacheRetention)
	}

	maxTokens := model.MaxTokens / 3
	if options != nil && options.MaxTokens != nil {
		maxTokens = *options.MaxTokens
	}

	params := map[string]any{
		"model":      model.ID,
		"max_tokens": maxTokens,
		"stream":     true,
	}

	// System prompt
	var systemBlocks []map[string]any
	// OAuth models require the Claude Code identity prefix (set by the auth extension).
	// Don't attach a cache_control breakpoint here — it's a strict prefix of the
	// next system block's breakpoint, so it would burn one of the four available
	// breakpoints for no benefit.
	if oauthSystemPrefix, ok := model.Headers["x-anthropic-oauth-system-prefix"]; ok && oauthSystemPrefix != "" {
		block := map[string]any{
			"type": "text",
			"text": oauthSystemPrefix,
		}
		systemBlocks = append(systemBlocks, block)
	}
	if ctx.SystemPrompt != "" {
		block := map[string]any{
			"type": "text",
			"text": ctx.SystemPrompt,
		}
		if retention != ai.CacheNone {
			block["cache_control"] = cacheControlBlock(model, retention)
		}
		systemBlocks = append(systemBlocks, block)
	}
	if len(systemBlocks) > 0 {
		params["system"] = systemBlocks
	}

	// Messages
	msgs := convertAnthropicMessages(ctx.Messages, model, oauthToken, retention)
	params["messages"] = msgs

	// Check prefix stability for cache preservation
	if options != nil && options.SessionID != "" {
		v, _ := anthropicPrefixGuards.LoadOrStore(options.SessionID, NewPrefixGuard())
		guard := v.(*PrefixGuard)
		guard.Check(systemBlocks, msgs)
	}

	// Temperature
	if options != nil && options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}

	// Tools
	var allTools []map[string]any
	if len(ctx.Tools) > 0 {
		allTools = append(allTools, convertAnthropicTools(ctx.Tools, oauthToken)...)
	}
	if options != nil {
		for _, st := range options.ServerTools {
			if matchesServerToolCapability(model, st.Type) {
				allTools = append(allTools, convertAnthropicServerTool(st))
			} else {
				firlog.Debug("skipping unsupported server tool for model", "model", model.ID, "toolType", st.Type)
			}
		}
	}
	if len(allTools) > 0 {
		params["tools"] = allTools
	}

	// Thinking (from internal headers)
	if options != nil && options.Headers != nil {
		if options.Headers["x-anthropic-thinking-disabled"] == "true" {
			params["thinking"] = map[string]any{"type": "disabled"}
		} else if options.Headers["x-anthropic-thinking-enabled"] == "true" {
			budgetStr := options.Headers["x-anthropic-thinking-budget"]
			budget := 1024
			if budgetStr != "" {
				fmt.Sscanf(budgetStr, "%d", &budget)
			}
			params["thinking"] = map[string]any{
				"type":          "enabled",
				"budget_tokens": budget,
			}
		}
		if effort := options.Headers["x-anthropic-thinking-effort"]; effort != "" {
			params["thinking"] = map[string]any{"type": "adaptive"}
			params["output_config"] = map[string]any{"effort": effort}
		}
	}

	// Tool choice
	if options != nil && options.ToolChoice != "" {
		switch options.ToolChoice {
		case "auto", "any", "none":
			params["tool_choice"] = map[string]any{"type": options.ToolChoice}
		default:
			// Specific tool name
			params["tool_choice"] = map[string]any{"type": "tool", "name": options.ToolChoice}
		}
	}

	// Server-side compaction
	if options != nil && options.Compaction != nil && options.Compaction.Enabled {
		if supportsModelCompaction(model) {
			edit := map[string]any{
				"type": "compact_20260112",
			}
			if options.Compaction.TriggerTokens > 0 {
				edit["trigger"] = map[string]any{
					"type":  "input_tokens",
					"value": options.Compaction.TriggerTokens,
				}
			}
			if options.Compaction.Instructions != "" {
				edit["instructions"] = options.Compaction.Instructions
			}
			params["context_management"] = map[string]any{
				"edits": []map[string]any{edit},
			}
		} else {
			firlog.Debug("skipping server compaction — model does not support it", "model", model.ID)
		}
	}

	return params
}

func cacheControlBlock(model *ai.Model, retention ai.CacheRetention) map[string]any {
	cc := map[string]any{"type": "ephemeral"}
	if retention == ai.CacheLong && getAnthropicCompat(model).SupportsLongCacheRetention {
		cc["ttl"] = "1h"
	}
	return cc
}

// resolvedAnthropicCompat is the resolved AnthropicMessagesCompat with defaults applied.
type resolvedAnthropicCompat struct {
	SupportsEagerToolInputStreaming bool
	SupportsLongCacheRetention      bool
}

// getAnthropicCompat returns the resolved AnthropicMessagesCompat for a model.
func getAnthropicCompat(model *ai.Model) resolvedAnthropicCompat {
	result := resolvedAnthropicCompat{
		SupportsEagerToolInputStreaming: true,
		SupportsLongCacheRetention:      true,
	}
	if compat := model.GetAnthropicMessagesCompat(); compat != nil {
		if compat.SupportsEagerToolInputStreaming != nil {
			result.SupportsEagerToolInputStreaming = *compat.SupportsEagerToolInputStreaming
		}
		if compat.SupportsLongCacheRetention != nil {
			result.SupportsLongCacheRetention = *compat.SupportsLongCacheRetention
		}
	}
	return result
}

func convertAnthropicMessages(messages []ai.Message, model *ai.Model, oauthToken bool, retention ai.CacheRetention) []map[string]any {
	transformed := TransformMessages(messages, model, normalizeAnthropicToolCallID)
	var params []map[string]any

	for i := 0; i < len(transformed); i++ {
		msg := &transformed[i]

		switch msg.Role() {
		case ai.RoleUser:
			u := msg.AsUser()
			if u == nil {
				continue
			}
			switch content := u.Content.(type) {
			case string:
				if strings.TrimSpace(content) == "" {
					continue
				}
				// Always use block form so cache_control can be attached to the last block.
				params = append(params, map[string]any{
					"role":    "user",
					"content": []map[string]any{{"type": "text", "text": content}},
				})
			case []any:
				var blocks []map[string]any
				for _, item := range content {
					if m, ok := item.(map[string]any); ok {
						t, _ := m["type"].(string)
						if t == "text" {
							text, _ := m["text"].(string)
							if strings.TrimSpace(text) != "" {
								blocks = append(blocks, map[string]any{"type": "text", "text": text})
							}
						} else if t == "image" {
							data, _ := m["data"].(string)
							mime, _ := m["mimeType"].(string)
							if model.SupportsImages() {
								blocks = append(blocks, map[string]any{
									"type": "image",
									"source": map[string]any{
										"type":       "base64",
										"media_type": mime,
										"data":       data,
									},
								})
							}
						}
					}
				}
				if len(blocks) > 0 {
					params = append(params, map[string]any{"role": "user", "content": blocks})
				}
			}

		case ai.RoleAssistant:
			a := msg.AsAssistant()
			if a == nil {
				continue
			}
			var blocks []map[string]any
			for _, c := range a.Content {
				if c.IsText() {
					if strings.TrimSpace(c.Text.Text) == "" {
						continue
					}
					blocks = append(blocks, map[string]any{"type": "text", "text": c.Text.Text})
				} else if c.IsThinking() {
					if c.Thinking.Redacted {
						// Redacted thinking must be passed back verbatim.
						blocks = append(blocks, map[string]any{
							"type": "redacted_thinking",
							"data": c.Thinking.ThinkingSignature,
						})
					} else if strings.TrimSpace(c.Thinking.Thinking) == "" {
						continue
					} else if c.Thinking.ThinkingSignature == "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": c.Thinking.Thinking})
					} else {
						blocks = append(blocks, map[string]any{
							"type":      "thinking",
							"thinking":  c.Thinking.Thinking,
							"signature": c.Thinking.ThinkingSignature,
						})
					}
				} else if c.IsToolCall() {
					name := c.ToolCall.Name
					if oauthToken {
						name = toClaudeCodeName(name)
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    c.ToolCall.ID,
						"name":  name,
						"input": c.ToolCall.Arguments,
					})
				}
			}
			if len(blocks) > 0 {
				params = append(params, map[string]any{"role": "assistant", "content": blocks})
			}

		case ai.RoleToolResult:
			// Collect consecutive tool results
			var toolResults []map[string]any
			for {
				tr := transformed[i].AsToolResult()
				if tr == nil {
					break
				}
				content := convertToolResultContent(tr.Content, model)
				toolResults = append(toolResults, map[string]any{
					"type":        "tool_result",
					"tool_use_id": tr.ToolCallID,
					"content":     content,
					"is_error":    tr.IsError,
				})
				if i+1 < len(transformed) && transformed[i+1].Role() == ai.RoleToolResult {
					i++
				} else {
					break
				}
			}
			if len(toolResults) > 0 {
				params = append(params, map[string]any{"role": "user", "content": toolResults})
			}
		}
	}

	// Add cache_control to last user message
	if retention != ai.CacheNone && len(params) > 0 {
		last := params[len(params)-1]
		if role, _ := last["role"].(string); role == "user" {
			if content, ok := last["content"].([]map[string]any); ok && len(content) > 0 {
				content[len(content)-1]["cache_control"] = cacheControlBlock(model, retention)
			}
		}
	}

	return params
}

// convertToolResultContent converts tool result content to Anthropic format.
// Text-only results return a single string. Mixed results with images return an array of blocks.
func convertToolResultContent(content []ai.ToolResultContent, model *ai.Model) any {
	hasImages := false
	supportsImages := model.SupportsImages()
	for _, c := range content {
		if c.IsImage() && supportsImages {
			hasImages = true
			break
		}
	}

	if !hasImages {
		// Text-only: return as simple string
		var parts []string
		for _, c := range content {
			if c.IsText() || (c.Type == "" && c.Text != "") {
				parts = append(parts, c.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	// Mixed content: return as array of blocks
	var blocks []map[string]any
	for _, c := range content {
		if c.IsText() {
			blocks = append(blocks, map[string]any{"type": "text", "text": c.Text})
		} else if c.IsImage() && supportsImages {
			blocks = append(blocks, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": c.MimeType,
					"data":       c.Data,
				},
			})
		}
	}
	return blocks
}

func convertAnthropicTools(tools []ai.Tool, oauthToken bool) []map[string]any {
	// Sort tools alphabetically by name to keep the prompt-cache prefix
	// stable across registration races (extensions and MCP servers add
	// tools asynchronously, so insertion order varies between turns).
	sorted := make([]ai.Tool, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	result := make([]map[string]any, 0, len(sorted))
	for _, t := range sorted {
		name := t.Name
		if oauthToken {
			name = toClaudeCodeName(name)
		}
		schema, _ := t.Parameters.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)
		if required == nil {
			if reqAny, ok := schema["required"].([]any); ok {
				for _, r := range reqAny {
					if s, ok := r.(string); ok {
						required = append(required, s)
					}
				}
			}
		}

		result = append(result, map[string]any{
			"name":        name,
			"description": t.Description,
			"input_schema": map[string]any{
				"type":       "object",
				"properties": props,
				"required":   required,
			},
		})
	}
	return result
}

// Anthropic web search domain limits.
const (
	maxAllowedDomains = 10
	maxBlockedDomains = 25
)

// serverToolDefaultName derives the default tool name from the type identifier.
// e.g. "web_search_20250305" → "web_search", "code_execution_20250522" → "code_execution".
func serverToolDefaultName(toolType string) string {
	// Strip the trailing _YYYYMMDD version suffix.
	for i := len(toolType) - 1; i >= 0; i-- {
		if toolType[i] == '_' {
			// Check if everything after _ is digits (a date suffix).
			suffix := toolType[i+1:]
			allDigits := len(suffix) > 0
			for _, c := range suffix {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return toolType[:i]
			}
			break
		}
	}
	return toolType
}

// convertAnthropicServerTool converts an AnthropicServerTool to the Anthropic API format.
func convertAnthropicServerTool(st ai.AnthropicServerTool) map[string]any {
	name := st.Name
	if name == "" {
		name = serverToolDefaultName(st.Type)
	}
	tool := map[string]any{
		"type": st.Type,
		"name": name,
	}
	if st.MaxUses > 0 {
		tool["max_uses"] = st.MaxUses
	}
	if len(st.AllowedDomains) > 0 {
		domains := st.AllowedDomains
		if len(domains) > maxAllowedDomains {
			firlog.Warn("web_search allowed_domains exceeds limit, truncating", "count", len(domains), "max", maxAllowedDomains)
			domains = domains[:maxAllowedDomains]
		}
		tool["allowed_domains"] = domains
	}
	if len(st.BlockedDomains) > 0 {
		domains := st.BlockedDomains
		if len(domains) > maxBlockedDomains {
			firlog.Warn("web_search blocked_domains exceeds limit, truncating", "count", len(domains), "max", maxBlockedDomains)
			domains = domains[:maxBlockedDomains]
		}
		tool["blocked_domains"] = domains
	}
	if st.UserLocation != nil {
		tool["user_location"] = st.UserLocation
	}
	return tool
}

// formatWebSearchResult formats a web_search_tool_result content block as readable text.
func formatWebSearchResult(cb map[string]any) string {
	content, _ := cb["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch itemType {
		case "web_search_result":
			title, _ := m["title"].(string)
			url, _ := m["url"].(string)
			snippet, _ := m["page_snippet"].(string)
			if title != "" {
				b.WriteString(title)
				if url != "" {
					b.WriteString(" (")
					b.WriteString(url)
					b.WriteString(")")
				}
				b.WriteString("\n")
			}
			if snippet != "" {
				b.WriteString(snippet)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// formatWebFetchResult formats a web_fetch_tool_result content block as readable text.
func formatWebFetchResult(cb map[string]any) string {
	content, _ := cb["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch itemType {
		case "web_fetch_result":
			url, _ := m["url"].(string)
			text, _ := m["content"].(string)
			if url != "" {
				b.WriteString("Fetched: ")
				b.WriteString(url)
				b.WriteString("\n")
			}
			if text != "" {
				b.WriteString(text)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// formatCodeExecutionResult formats a code_execution_tool_result content block as readable text.
func formatCodeExecutionResult(cb map[string]any) string {
	content, _ := cb["content"].([]any)
	if len(content) == 0 {
		// Check for top-level stdout/stderr (alternative format).
		var b strings.Builder
		if stdout, _ := cb["stdout"].(string); stdout != "" {
			b.WriteString(stdout)
			b.WriteString("\n")
		}
		if stderr, _ := cb["stderr"].(string); stderr != "" {
			b.WriteString("stderr: ")
			b.WriteString(stderr)
			b.WriteString("\n")
		}
		return b.String()
	}
	var b strings.Builder
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		switch itemType {
		case "code_execution_output":
			output, _ := m["output"].(string)
			if output != "" {
				b.WriteString(output)
				b.WriteString("\n")
			}
		case "code_execution_error":
			errName, _ := m["error_name"].(string)
			errMsg, _ := m["error_message"].(string)
			b.WriteString("Error")
			if errName != "" {
				b.WriteString(" (")
				b.WriteString(errName)
				b.WriteString(")")
			}
			if errMsg != "" {
				b.WriteString(": ")
				b.WriteString(errMsg)
			}
			b.WriteString("\n")
		case "image":
			// Images from code execution (e.g. matplotlib plots) — note their presence.
			b.WriteString("[generated image]\n")
		}
	}
	return b.String()
}

// formatToolOutput formats a tool_output content block from programmatic tool calling.
func formatToolOutput(cb map[string]any) string {
	output, _ := cb["output"].(string)
	if output != "" {
		return output + "\n"
	}
	// Some tool outputs use a content array.
	content, _ := cb["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range content {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := m["text"].(string); text != "" {
			b.WriteString(text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// updateAnthropicUsage updates usage from an Anthropic usage object.
// Only updates fields that are present (non-null) to preserve values from
// earlier events (e.g. input_tokens from message_start when proxies omit
// it in message_delta).
func updateAnthropicUsage(output *ai.AssistantMessage, usage map[string]any, model *ai.Model) {
	if v, ok := usage["input_tokens"]; ok && v != nil {
		if f, ok := v.(float64); ok {
			output.Usage.Input = int(f)
		}
	}
	if v, ok := usage["output_tokens"]; ok && v != nil {
		if f, ok := v.(float64); ok {
			output.Usage.Output = int(f)
		}
	}
	if v, ok := usage["cache_read_input_tokens"]; ok && v != nil {
		if f, ok := v.(float64); ok {
			output.Usage.CacheRead = int(f)
		}
	}
	if v, ok := usage["cache_creation_input_tokens"]; ok && v != nil {
		if f, ok := v.(float64); ok {
			output.Usage.CacheWrite = int(f)
		}
	}
	output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output +
		output.Usage.CacheRead + output.Usage.CacheWrite
	ai.CalculateCost(model, &output.Usage)
}

func jsonInt(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func jsonString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
