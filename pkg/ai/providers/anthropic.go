// Ported from: packages/ai/src/providers/anthropic.ts
// Upstream hash: 1caadb2e
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// claudeCodeVersion mimics Claude Code's version for OAuth stealth mode.
const claudeCodeVersion = "2.1.2"

// claudeCodeTools are the canonical tool names from Claude Code 2.x.
var claudeCodeTools = []string{
	"Read", "Write", "Edit", "Bash", "Grep", "Glob",
	"AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "KillShell",
	"NotebookEdit", "Skill", "Task", "TaskOutput", "TodoWrite",
	"WebFetch", "WebSearch",
}

var ccToolLookup map[string]string

func init() {
	ccToolLookup = make(map[string]string, len(claudeCodeTools))
	for _, t := range claudeCodeTools {
		ccToolLookup[strings.ToLower(t)] = t
	}
}

func toClaudeCodeName(name string) string {
	if cc, ok := ccToolLookup[strings.ToLower(name)]; ok {
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

func isOAuthTokenStr(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

func supportsAdaptiveThinking(modelID string) bool {
	return strings.Contains(modelID, "opus-4-6") || strings.Contains(modelID, "opus-4.6")
}

func mapThinkingLevelToEffort(level ai.ThinkingLevel) string {
	switch level {
	case ai.ThinkingMinimal, ai.ThinkingLow:
		return "low"
	case ai.ThinkingMedium:
		return "medium"
	case ai.ThinkingHigh:
		return "high"
	case ai.ThinkingXHigh:
		return "max"
	default:
		return "high"
	}
}

func mapAnthropicStopReason(reason string) ai.StopReason {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
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

// StreamAnthropic is the raw Anthropic streaming function.
func StreamAnthropic(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
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

		oauthToken := isOAuthTokenStr(apiKey)
		params := buildAnthropicParams(model, prompt, oauthToken, options)

		payload, err := json.Marshal(params)
		if err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = fmt.Sprintf("marshal request: %v", err)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		url := strings.TrimRight(model.BaseURL, "/") + "/v1/messages"
		headers := buildAnthropicHeaders(model, apiKey, oauthToken, options)

		firlog.Debug("anthropic request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))

		sseEvents, sseErr := DefaultSSEClient.Stream(ctx, url, headers, bytes.NewReader(payload))

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

		// Track block indices from Anthropic to our content array indices
		type blockInfo struct {
			contentIdx  int
			partialJSON string
		}
		blocks := map[int]*blockInfo{}

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
			case "message_start":
				if msg, ok := raw["message"].(map[string]any); ok {
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
						output.Content[ci].ToolCall.Arguments = ai.ParseStreamingJSON(bi.partialJSON)
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
					c.ToolCall.Arguments = ai.ParseStreamingJSON(bi.partialJSON)
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
				firlog.Warn("anthropic error", "err", errMsg)
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = errMsg
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return
			}
		}

		// Check for SSE-level errors
		if err := <-sseErr; err != nil {
			firlog.Warn("anthropic SSE error", "err", err)
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		firlog.Debug("anthropic response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: output.StopReason, Message: output})
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
		apiKey = ai.GetEnvApiKey(model.Provider)
	}

	base := BuildBaseOptions(model, options, apiKey)

	if options == nil || options.Reasoning == "" {
		return StreamAnthropic(ctx, model, prompt, base)
	}

	// For Opus 4.6+: adaptive thinking
	if supportsAdaptiveThinking(model.ID) {
		// Pass effort via header (custom extension)
		if base.Headers == nil {
			base.Headers = map[string]string{}
		}
		base.Headers["x-anthropic-thinking-effort"] = mapThinkingLevelToEffort(options.Reasoning)
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

func buildAnthropicHeaders(model *ai.Model, apiKey string, oauthToken bool, options *ai.StreamOptions) map[string]string {
	betaFeatures := "fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14"

	// Add server tool betas if needed.
	if options != nil {
		seen := map[string]bool{}
		for _, st := range options.ServerTools {
			var beta string
			switch {
			case strings.HasPrefix(st.Type, "web_search"):
				beta = "web-search-2025-03-05"
			case strings.HasPrefix(st.Type, "code_execution"):
				beta = "code-execution-2025-05-22"
			case strings.HasPrefix(st.Type, "programmatic_tool_calling"):
				beta = "programmatic-tool-calling-2025-06-24"
			}
			if beta != "" && !seen[beta] {
				seen[beta] = true
				betaFeatures += "," + beta
			}
		}
	}

	headers := map[string]string{
		"accept":            "application/json",
		"anthropic-version": "2023-06-01",
		"anthropic-dangerous-direct-browser-access": "true",
	}

	if oauthToken {
		headers["anthropic-beta"] = fmt.Sprintf("claude-code-20250219,oauth-2025-04-20,%s", betaFeatures)
		headers["user-agent"] = fmt.Sprintf("claude-cli/%s (external, cli)", claudeCodeVersion)
		headers["x-app"] = "cli"
		headers["authorization"] = "Bearer " + apiKey
	} else {
		headers["anthropic-beta"] = betaFeatures
		headers["x-api-key"] = apiKey
	}

	// Merge model headers
	for k, v := range model.Headers {
		headers[k] = v
	}

	// Merge option headers (but skip our internal x-anthropic-* ones)
	if options != nil {
		for k, v := range options.Headers {
			if !strings.HasPrefix(k, "x-anthropic-thinking-") {
				headers[k] = v
			}
		}
	}

	return headers
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
	if oauthToken {
		block := map[string]any{
			"type": "text",
			"text": "You are Claude Code, Anthropic's official CLI for Claude.",
		}
		if retention != ai.CacheNone {
			block["cache_control"] = cacheControlBlock(model.BaseURL, retention)
		}
		systemBlocks = append(systemBlocks, block)
	}
	if ctx.SystemPrompt != "" {
		block := map[string]any{
			"type": "text",
			"text": ctx.SystemPrompt,
		}
		if retention != ai.CacheNone {
			block["cache_control"] = cacheControlBlock(model.BaseURL, retention)
		}
		systemBlocks = append(systemBlocks, block)
	}
	if len(systemBlocks) > 0 {
		params["system"] = systemBlocks
	}

	// Messages
	params["messages"] = convertAnthropicMessages(ctx.Messages, model, oauthToken, retention)

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
			allTools = append(allTools, convertAnthropicServerTool(st))
		}
	}
	if len(allTools) > 0 {
		params["tools"] = allTools
	}

	// Thinking (from internal headers)
	if options != nil && options.Headers != nil {
		if options.Headers["x-anthropic-thinking-enabled"] == "true" {
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

	return params
}

func cacheControlBlock(baseURL string, retention ai.CacheRetention) map[string]any {
	cc := map[string]any{"type": "ephemeral"}
	if retention == ai.CacheLong && strings.Contains(baseURL, "api.anthropic.com") {
		cc["ttl"] = "1h"
	}
	return cc
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
				params = append(params, map[string]any{
					"role":    "user",
					"content": content,
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
					if strings.TrimSpace(c.Thinking.Thinking) == "" {
						continue
					}
					if c.Thinking.ThinkingSignature == "" {
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
				content[len(content)-1]["cache_control"] = cacheControlBlock(model.BaseURL, retention)
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
			if c.IsText() {
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
	result := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
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
	maxAllowedDomains  = 10
	maxBlockedDomains  = 25
)

// convertAnthropicServerTool converts an AnthropicServerTool to the Anthropic API format.
func convertAnthropicServerTool(st ai.AnthropicServerTool) map[string]any {
	tool := map[string]any{
		"type": st.Type,
	}
	if st.Name != "" {
		tool["name"] = st.Name
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
