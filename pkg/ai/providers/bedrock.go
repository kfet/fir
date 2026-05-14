// Ported from: packages/ai/src/providers/amazon-bedrock.ts
// Upstream hash: 036bde0a
//
// Uses github.com/kfet/skipstone — a minimal stdlib-only client for
// Bedrock's ConverseStream API — for SigV4 signing and credential resolution
// (env, shared profile, credential_process, IRSA, ECS task creds, IMDSv2,
// STS AssumeRole + source_profile + MFA).
package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	skipstone "github.com/kfet/skipstone"
	"github.com/kfet/skipstone/creds"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/jsonparse"
	firlog "github.com/kfet/fir/pkg/log"
)

// bedrockBlockState tracks content block state during streaming.
type bedrockBlockState struct {
	contentIdx  int
	partialJSON string
}

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

		emitError := func(msg string) {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = msg
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
		}

		client, err := newBedrockClient(model)
		if err != nil {
			emitError(fmt.Sprintf("creating bedrock client: %v", err))
			return
		}

		input, err := buildConverseStreamInput(model, prompt, options)
		if err != nil {
			emitError(fmt.Sprintf("building request: %v", err))
			return
		}
		if options != nil && options.OnPayload != nil {
			if next := options.OnPayload(input, model); next != nil {
				if v, ok := next.(*skipstone.ConverseStreamInput); ok {
					input = v
				}
			}
		}

		firlog.Debug("bedrock request", "model", model.ID, "messageCount", len(prompt.Messages))
		traceWireMessages("bedrock", input)

		respStream, err := client.ConverseStream(ctx, input)
		if err != nil {
			if ctx.Err() != nil {
				output.StopReason = ai.StopReasonAborted
				output.ErrorMessage = "request aborted"
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonAborted, Error: output})
			} else {
				emitError(err.Error())
			}
			return
		}
		defer respStream.Close()

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

		blocks := map[int]*bedrockBlockState{}

		for {
			ev, err := respStream.Recv()
			if err != nil {
				// io.EOF terminates normally.
				if err.Error() == "EOF" {
					break
				}
				firlog.Warn("bedrock stream error", "err", err)
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = err.Error()
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return
			}
			if ev.APIError != nil {
				output.StopReason = ai.StopReasonError
				output.ErrorMessage = ev.APIError.Error()
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
				return
			}
			handleBedrockEvent(ev, model, output, blocks, stream)
		}

		// Prune empty/whitespace-only text blocks. Bedrock's streamer
		// creates a text block on the first `text_delta` regardless of
		// the delta's content, then accumulates into it — so a stream
		// of whitespace-only `text_delta` events leaves a
		// whitespace-only text block in stored Content. Bedrock-Claude
		// inherits Anthropic's thinking-immutability contract on
		// replay, so we must not let such a block reach storage.
		output.Content = pruneEmptyAssistantTextBlocks(output.Content)
		firlog.Debug("bedrock response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
	}()

	return stream
}

func handleBedrockEvent(
	ev skipstone.Event,
	model *ai.Model,
	output *ai.AssistantMessage,
	blocks map[int]*bedrockBlockState,
	stream *ai.AssistantMessageEventStream,
) {
	switch d := ev.Decoded.(type) {
	case skipstone.EventMessageStart:
		_ = d
	case skipstone.EventContentBlockStart:
		blockIdx := d.ContentBlockIndex
		if d.Start.ToolUse != nil {
			idx := len(output.Content)
			output.Content = append(output.Content, ai.NewToolCallContent(
				d.Start.ToolUse.ToolUseID,
				d.Start.ToolUse.Name,
				map[string]any{},
			))
			blocks[blockIdx] = &bedrockBlockState{contentIdx: idx}
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventToolcallStart,
				ContentIndex: idx,
				Partial:      output,
			})
		}
	case skipstone.EventContentBlockDelta:
		blockIdx := d.ContentBlockIndex
		bs := blocks[blockIdx]

		switch {
		case d.Delta.ToolUse != nil:
			if bs != nil {
				inp := d.Delta.ToolUse.Input
				bs.partialJSON += inp
				parsed := jsonparse.ParseStreamingJSON(bs.partialJSON)
				c := output.Content[bs.contentIdx]
				c.ToolCall.Arguments = parsed
				output.Content[bs.contentIdx] = c
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventToolcallDelta,
					ContentIndex: bs.contentIdx,
					Delta:        inp,
					Partial:      output,
				})
			}
		case d.Delta.ReasoningContent != nil:
			handleReasoningDelta(d.Delta.ReasoningContent.Text, d.Delta.ReasoningContent.Signature, blockIdx, bs, blocks, output, stream)
		default:
			// Text delta. Bedrock only sends one of {text, toolUse, reasoning}
			// on a given delta — but text is the empty-string default, so we
			// must check it last.
			text := d.Delta.Text
			if bs == nil {
				idx := len(output.Content)
				output.Content = append(output.Content, ai.NewTextContent(""))
				bs = &bedrockBlockState{contentIdx: idx}
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
	case skipstone.EventContentBlockStop:
		blockIdx := d.ContentBlockIndex
		bs := blocks[blockIdx]
		if bs == nil {
			return
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
				parsed := jsonparse.ParseStreamingJSON(bs.partialJSON)
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
	case skipstone.EventMessageStop:
		output.StopReason = mapBedrockStopReason(d.StopReason)
	case skipstone.EventMetadata:
		output.Usage.Input = d.Usage.InputTokens
		output.Usage.Output = d.Usage.OutputTokens
		output.Usage.TotalTokens = d.Usage.TotalTokens
		output.Usage.CacheRead = d.Usage.CacheReadInputTokens
		output.Usage.CacheWrite = d.Usage.CacheWriteInputTokens
		if output.Usage.TotalTokens == 0 {
			output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output
		}
		ai.CalculateCost(model, &output.Usage)
		_ = ev
	}
}

// handleReasoningDelta processes a reasoning/thinking content block delta.
func handleReasoningDelta(
	text, signature string,
	blockIdx int,
	bs *bedrockBlockState,
	blocks map[int]*bedrockBlockState,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) {
	if text != "" {
		if bs == nil {
			idx := len(output.Content)
			output.Content = append(output.Content, ai.NewThinkingContent(""))
			bs = &bedrockBlockState{contentIdx: idx}
			blocks[blockIdx] = bs
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventThinkingStart,
				ContentIndex: idx,
				Partial:      output,
			})
		}
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
	if signature != "" && bs != nil {
		c := output.Content[bs.contentIdx]
		c.Thinking.ThinkingSignature += signature
		output.Content[bs.contentIdx] = c
	}
}

// --- skipstone client construction ---

func newBedrockClient(model *ai.Model) (*skipstone.Client, error) {
	var opts []skipstone.Option

	// Region: explicit env vars > AWS_PROFILE region (resolved by skipstone) > us-east-1.
	if r := os.Getenv("AWS_REGION"); r != "" {
		opts = append(opts, skipstone.WithRegion(r))
	} else if r := os.Getenv("AWS_DEFAULT_REGION"); r != "" {
		opts = append(opts, skipstone.WithRegion(r))
	}

	// AWS_BEDROCK_SKIP_AUTH=1 — proxy/gateway scenario where SigV4 isn't enforced.
	if os.Getenv("AWS_BEDROCK_SKIP_AUTH") == "1" {
		opts = append(opts, skipstone.WithStaticCredentials("dummy-access-key", "dummy-secret-key", ""))
	} else {
		opts = append(opts, skipstone.WithCredentials(creds.DefaultChain(creds.Config{})))
	}

	if model.BaseURL != "" {
		opts = append(opts, skipstone.WithEndpoint(strings.TrimRight(model.BaseURL, "/")))
	}

	return skipstone.NewClient(opts...)
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

// --- Build ConverseStream input ---

func buildConverseStreamInput(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) (*skipstone.ConverseStreamInput, error) {
	input := &skipstone.ConverseStreamInput{
		ModelID: model.ID,
	}

	retention := resolveCacheRetention("")
	if options != nil {
		retention = resolveCacheRetention(options.CacheRetention)
	}
	canCache := supportsBedrockPromptCaching(model) && retention != ai.CacheNone

	// System prompt
	if ctx.SystemPrompt != "" {
		input.System = []skipstone.SystemBlock{
			{Text: ctx.SystemPrompt},
		}
		if canCache {
			input.System = append(input.System, skipstone.SystemBlock{
				CachePoint: bedrockCachePoint(retention),
			})
		}
	}

	// Messages
	input.Messages = convertBedrockMessages(ctx.Messages, model, canCache, retention)

	// Inference config
	if options != nil && (options.MaxTokens != nil || options.Temperature != nil) {
		ic := &skipstone.InferenceConfig{}
		if options.MaxTokens != nil {
			v := *options.MaxTokens
			ic.MaxTokens = &v
		}
		if options.Temperature != nil {
			v := *options.Temperature
			ic.Temperature = &v
		}
		input.Inference = ic
	}

	// Tools
	toolChoice := ""
	if options != nil {
		toolChoice = options.ToolChoice
	}
	if len(ctx.Tools) > 0 && toolChoice != "none" {
		tools, choice := convertBedrockToolConfig(ctx.Tools, toolChoice)
		input.Tools = tools
		input.ToolChoice = choice
	}

	// Thinking/reasoning config
	if options != nil && options.Headers != nil {
		if reasoning := options.Headers["x-bedrock-reasoning"]; reasoning != "" {
			if isBedrockAnthropicClaudeModel(model) && model.Reasoning {
				fields := buildBedrockAdditionalFields(model, reasoning, options)
				if fields != nil {
					raw, err := json.Marshal(fields)
					if err != nil {
						return nil, fmt.Errorf("marshalling additional model fields: %w", err)
					}
					input.AdditionalModelRequestFields = raw
				}
			}
		}
	}

	return input, nil
}

func convertBedrockMessages(messages []ai.Message, model *ai.Model, canCache bool, retention ai.CacheRetention) []skipstone.Message {
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

	transformed := TransformMessages(messages, model, normalizeID)
	var result []skipstone.Message

	for i := 0; i < len(transformed); i++ {
		msg := &transformed[i]

		if u := msg.AsUser(); u != nil {
			var blocks []skipstone.Block
			switch content := u.Content.(type) {
			case string:
				if strings.TrimSpace(content) != "" {
					blocks = append(blocks, skipstone.Block{Text: content})
				}
			case []any:
				for _, item := range content {
					if m, ok := item.(map[string]any); ok {
						switch m["type"] {
						case "text":
							if text, ok := m["text"].(string); ok {
								blocks = append(blocks, skipstone.Block{Text: text})
							}
						case "image":
							if model.SupportsImages() {
								blocks = append(blocks, convertImageBlock(m))
							}
						}
					}
				}
			}
			if len(blocks) > 0 {
				result = append(result, skipstone.Message{
					Role:    skipstone.RoleUser,
					Content: blocks,
				})
			}

		} else if a := msg.AsAssistant(); a != nil {
			var blocks []skipstone.Block
			for _, block := range a.Content {
				switch {
				case block.IsText():
					if strings.TrimSpace(block.Text.Text) != "" {
						blocks = append(blocks, skipstone.Block{Text: block.Text.Text})
					}
				case block.IsToolCall():
					tc := block.ToolCall
					argsJSON, err := json.Marshal(tc.Arguments)
					if err != nil {
						argsJSON = []byte("{}")
					}
					blocks = append(blocks, skipstone.Block{
						ToolUse: &skipstone.ToolUseBlock{
							ToolUseID: tc.ID,
							Name:      tc.Name,
							Input:     argsJSON,
						},
					})
				case block.IsThinking():
					if strings.TrimSpace(block.Thinking.Thinking) != "" {
						// Signatures arrive after thinking deltas. If a partial or externally
						// persisted message lacks a signature, Bedrock rejects the replayed
						// reasoning block. Fall back to plain text, matching Anthropic.
						if !supportsBedrockThinkingSignature(model) || strings.TrimSpace(block.Thinking.ThinkingSignature) == "" {
							blocks = append(blocks, skipstone.Block{Text: block.Thinking.Thinking})
						} else {
							blocks = append(blocks, skipstone.Block{
								Reasoning: &skipstone.ReasoningBlock{
									Text:      block.Thinking.Thinking,
									Signature: block.Thinking.ThinkingSignature,
								},
							})
						}
					}
				}
			}
			if len(blocks) > 0 {
				result = append(result, skipstone.Message{
					Role:    skipstone.RoleAssistant,
					Content: blocks,
				})
			}

		} else if tr := msg.AsToolResult(); tr != nil {
			// Collect consecutive tool results into a single user message
			var blocks []skipstone.Block
			for {
				tr := transformed[i].AsToolResult()
				if tr == nil {
					break
				}
				var content []skipstone.ToolResultContent
				for _, c := range tr.Content {
					if c.IsText() {
						content = append(content, skipstone.ToolResultContent{Text: c.Text})
					} else if c.IsImage() && model.SupportsImages() {
						imgBytes, err := base64.StdEncoding.DecodeString(c.Data)
						if err == nil {
							content = append(content, skipstone.ToolResultContent{
								Image: &skipstone.ImageBlock{
									Format: bedrockImageFormat(c.MimeType),
									Source: skipstone.ImageSource{Bytes: imgBytes},
								},
							})
						}
					}
				}
				if len(content) == 0 {
					content = []skipstone.ToolResultContent{{Text: ""}}
				}
				status := "success"
				if tr.IsError {
					status = "error"
				}
				blocks = append(blocks, skipstone.Block{
					ToolResult: &skipstone.ToolResult{
						ToolUseID: tr.ToolCallID,
						Content:   content,
						Status:    status,
					},
				})
				if i+1 < len(transformed) && transformed[i+1].Role() == ai.RoleToolResult {
					i++
				} else {
					break
				}
			}
			result = append(result, skipstone.Message{
				Role:    skipstone.RoleUser,
				Content: blocks,
			})
		}
	}

	// Add cache point to last user message
	if canCache && len(result) > 0 {
		last := &result[len(result)-1]
		if last.Role == skipstone.RoleUser {
			last.Content = append(last.Content, skipstone.Block{
				CachePoint: bedrockCachePoint(retention),
			})
		}
	}

	return result
}

func convertImageBlock(m map[string]any) skipstone.Block {
	data, _ := m["data"].(string)
	mime, _ := m["mimeType"].(string)
	imgBytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Fall back to raw bytes if not base64
		imgBytes = []byte(data)
	}
	return skipstone.Block{
		Image: &skipstone.ImageBlock{
			Format: bedrockImageFormat(mime),
			Source: skipstone.ImageSource{Bytes: imgBytes},
		},
	}
}

// convertBedrockToolConfig builds the tool slice + tool-choice for a request.
//
// The Tool.Parameters → InputSchema marshalling is now handled by
// skipstone, which also normalises empty / non-object schemas itself.
// We still apply bedrockToolSchema as defence-in-depth for a couple of
// fir-specific edge cases the existing tests cover.
func convertBedrockToolConfig(tools []ai.Tool, toolChoice string) ([]skipstone.Tool, *skipstone.ToolChoice) {
	out := make([]skipstone.Tool, 0, len(tools))
	for _, tool := range tools {
		schema := bedrockToolSchema(tool.Parameters)
		raw, err := json.Marshal(schema)
		if err != nil {
			raw = []byte(`{"type":"object","properties":{}}`)
		}
		out = append(out, skipstone.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: raw,
		})
	}

	var choice *skipstone.ToolChoice
	switch toolChoice {
	case "auto":
		choice = &skipstone.ToolChoice{Type: skipstone.ToolChoiceAuto}
	case "any":
		choice = &skipstone.ToolChoice{Type: skipstone.ToolChoiceAny}
	case "":
		// no explicit choice
	default:
		choice = &skipstone.ToolChoice{Type: skipstone.ToolChoiceTool, Name: toolChoice}
	}
	return out, choice
}

// bedrockToolSchema coerces a tool's Parameters map into a JSON-Schema-shaped
// object. Defence-in-depth: skipstone normalises the wire form as well,
// but this preserves behaviour for the existing fir test suite.
func bedrockToolSchema(params any) map[string]any {
	if params == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if m, ok := params.(map[string]any); ok {
		if m["type"] == nil {
			m["type"] = "object"
		}
		if m["properties"] == nil {
			m["properties"] = map[string]any{}
		}
		return m
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func buildBedrockAdditionalFields(model *ai.Model, reasoning string, options *ai.StreamOptions) map[string]any {
	if supportsBedrockAdaptiveThinking(model.ID, model.Name) {
		return map[string]any{
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": bedrockThinkingLevelToEffort(ai.ThinkingLevel(reasoning), model.ID)},
		}
	}

	budget := 1024
	if b := options.Headers["x-bedrock-thinking-budget"]; b != "" {
		fmt.Sscanf(b, "%d", &budget)
	}
	fields := map[string]any{
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		},
	}
	if options.Headers["x-bedrock-interleaved-thinking"] == "true" {
		fields["anthropic_beta"] = []string{"interleaved-thinking-2025-05-14"}
	}
	return fields
}

func bedrockCachePoint(retention ai.CacheRetention) *skipstone.CachePoint {
	cp := &skipstone.CachePoint{Type: "default"}
	if retention == ai.CacheLong {
		cp.TTL = "1h"
	}
	return cp
}

// modelMatchCandidates returns lowercased variants of the model ID and (optionally)
// model name that downstream string-contains checks can scan. Application inference
// profile ARNs in Bedrock don't contain the model name, so we also accept the
// human-readable Name field which the user provides via models.json.
func modelMatchCandidates(modelID, modelName string) []string {
	values := []string{modelID}
	if modelName != "" {
		values = append(values, modelName)
	}
	out := make([]string, 0, len(values)*2)
	for _, v := range values {
		lower := strings.ToLower(v)
		out = append(out, lower)
		repl := lower
		for _, sep := range []string{" ", "_", ".", ":"} {
			repl = strings.ReplaceAll(repl, sep, "-")
		}
		if repl != lower {
			out = append(out, repl)
		}
	}
	return out
}

func anyContains(candidates []string, needle string) bool {
	for _, s := range candidates {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func supportsBedrockPromptCaching(model *ai.Model) bool {
	candidates := modelMatchCandidates(model.ID, model.Name)
	if !anyContains(candidates, "claude") {
		if os.Getenv("AWS_BEDROCK_FORCE_CACHE") == "1" {
			return true
		}
		return false
	}
	if anyContains(candidates, "-4-") {
		return true
	}
	if anyContains(candidates, "claude-3-7-sonnet") {
		return true
	}
	if anyContains(candidates, "claude-3-5-haiku") {
		return true
	}
	return false
}

func supportsBedrockThinkingSignature(model *ai.Model) bool {
	return isBedrockAnthropicClaudeModel(model)
}

func isBedrockAnthropicClaudeModel(model *ai.Model) bool {
	candidates := modelMatchCandidates(model.ID, model.Name)
	return anyContains(candidates, "anthropic.claude") ||
		anyContains(candidates, "anthropic/claude") ||
		anyContains(candidates, "claude")
}

func supportsBedrockAdaptiveThinking(modelID, modelName string) bool {
	candidates := modelMatchCandidates(modelID, modelName)
	return anyContains(candidates, "opus-4-6") ||
		anyContains(candidates, "opus-4-7") ||
		anyContains(candidates, "sonnet-4-6")
}

func bedrockThinkingLevelToEffort(level ai.ThinkingLevel, modelID string) string {
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

// bedrockImageFormat maps MIME types to Bedrock's image format strings.
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

	rawEffort := options.Reasoning
	clampedEffort := ClampReasoning(rawEffort)

	if isBedrockAnthropicClaudeModel(model) && model.Reasoning {
		if supportsBedrockAdaptiveThinking(model.ID, model.Name) {
			base.Headers["x-bedrock-reasoning"] = string(ClampReasoningForModel(rawEffort, model))
		} else {
			maxTokens := 0
			if base.MaxTokens != nil {
				maxTokens = *base.MaxTokens
			}
			adjustedMax, thinkingBudget := AdjustMaxTokensForThinking(
				maxTokens, model.MaxTokens, clampedEffort, options.ThinkingBudgets)
			base.MaxTokens = &adjustedMax
			base.Headers["x-bedrock-reasoning"] = string(clampedEffort)
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
