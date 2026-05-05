// Ported from: packages/ai/src/providers/amazon-bedrock.ts
// Upstream hash: 036bde0a
//
// Uses the AWS SDK for Go v2 (BedrockRuntime ConverseStream) for proper
// SigV4 signing and credential resolution (profiles, IAM, IRSA, ECS, etc.).
package providers

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

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

		// Build AWS SDK client
		client, err := newBedrockClient(ctx, model, options)
		if err != nil {
			emitError(fmt.Sprintf("creating bedrock client: %v", err))
			return
		}

		// Build ConverseStream input
		input, err := buildConverseStreamInput(model, prompt, options)
		if err != nil {
			emitError(fmt.Sprintf("building request: %v", err))
			return
		}
		if options != nil && options.OnPayload != nil {
			if next := options.OnPayload(input, model); next != nil {
				if v, ok := next.(*bedrockruntime.ConverseStreamInput); ok {
					input = v
				}
			}
		}

		firlog.Debug("bedrock request", "model", model.ID, "messageCount", len(prompt.Messages))

		resp, err := client.ConverseStream(ctx, input)
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

		eventStream := resp.GetStream()
		defer eventStream.Close()

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

		blocks := map[int]*bedrockBlockState{}

		for event := range eventStream.Events() {
			switch ev := event.(type) {
			case *brtypes.ConverseStreamOutputMemberMessageStart:
				// Already sent EventStart above
				_ = ev

			case *brtypes.ConverseStreamOutputMemberContentBlockStart:
				blockIdx := int(derefI32(ev.Value.ContentBlockIndex))
				if start, ok := ev.Value.Start.(*brtypes.ContentBlockStartMemberToolUse); ok {
					idx := len(output.Content)
					output.Content = append(output.Content, ai.NewToolCallContent(
						derefStr(start.Value.ToolUseId),
						derefStr(start.Value.Name),
						map[string]any{},
					))
					blocks[blockIdx] = &bedrockBlockState{contentIdx: idx}
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventToolcallStart,
						ContentIndex: idx,
						Partial:      output,
					})
				}

			case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
				blockIdx := int(derefI32(ev.Value.ContentBlockIndex))
				bs := blocks[blockIdx]

				switch delta := ev.Value.Delta.(type) {
				case *brtypes.ContentBlockDeltaMemberText:
					text := delta.Value
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

				case *brtypes.ContentBlockDeltaMemberToolUse:
					if bs != nil && delta.Value.Input != nil {
						input := *delta.Value.Input
						bs.partialJSON += input
						parsed := jsonparse.ParseStreamingJSON(bs.partialJSON)
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

				case *brtypes.ContentBlockDeltaMemberReasoningContent:
					handleReasoningDelta(delta.Value, blockIdx, bs, blocks, output, stream)
				}

			case *brtypes.ConverseStreamOutputMemberContentBlockStop:
				blockIdx := int(derefI32(ev.Value.ContentBlockIndex))
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

			case *brtypes.ConverseStreamOutputMemberMessageStop:
				output.StopReason = mapBedrockStopReason(string(ev.Value.StopReason))

			case *brtypes.ConverseStreamOutputMemberMetadata:
				if ev.Value.Usage != nil {
					output.Usage.Input = int(derefI32(ev.Value.Usage.InputTokens))
					output.Usage.Output = int(derefI32(ev.Value.Usage.OutputTokens))
					output.Usage.TotalTokens = int(derefI32(ev.Value.Usage.TotalTokens))
					if ev.Value.Usage.CacheReadInputTokens != nil {
						output.Usage.CacheRead = int(*ev.Value.Usage.CacheReadInputTokens)
					}
					if ev.Value.Usage.CacheWriteInputTokens != nil {
						output.Usage.CacheWrite = int(*ev.Value.Usage.CacheWriteInputTokens)
					}
					if output.Usage.TotalTokens == 0 {
						output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output
					}
					ai.CalculateCost(model, &output.Usage)
				}
			}
		}

		// Check for stream-level errors
		if err := eventStream.Err(); err != nil {
			firlog.Warn("bedrock stream error", "err", err)
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		// Prune empty/whitespace-only text blocks. Bedrock's streamer
		// creates a text block on the first `text_delta` regardless of
		// the delta's content, then accumulates into it — so a stream
		// of whitespace-only `text_delta` events leaves a
		// whitespace-only text block in stored Content. Bedrock-Claude
		// inherits Anthropic's thinking-immutability contract on
		// replay, so we must not let such a block reach storage. See
		// pruneEmptyAssistantTextBlocks for the full rationale.
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

// handleReasoningDelta processes a reasoning/thinking content block delta.
func handleReasoningDelta(
	delta brtypes.ReasoningContentBlockDelta,
	blockIdx int,
	bs *bedrockBlockState,
	blocks map[int]*bedrockBlockState,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) {
	switch rc := delta.(type) {
	case *brtypes.ReasoningContentBlockDeltaMemberText:
		text := rc.Value
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

	case *brtypes.ReasoningContentBlockDeltaMemberSignature:
		if bs != nil {
			c := output.Content[bs.contentIdx]
			c.Thinking.ThinkingSignature += rc.Value
			output.Content[bs.contentIdx] = c
		}
	}
}

// --- AWS SDK client construction ---

func newBedrockClient(ctx context.Context, model *ai.Model, options *ai.StreamOptions) (*bedrockruntime.Client, error) {
	// Region resolution: explicit env vars > SDK default chain.
	// When AWS_PROFILE is set, we leave region unset so the SDK can
	// resolve it from aws profile configs. Otherwise fall back to us-east-1.
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" && os.Getenv("AWS_PROFILE") == "" {
		region = "us-east-1"
	}

	var cfgOpts []func(*awsconfig.LoadOptions) error
	if region != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithRegion(region))
	}

	// Support proxies that don't need authentication
	if os.Getenv("AWS_BEDROCK_SKIP_AUTH") == "1" {
		cfgOpts = append(cfgOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("dummy-access-key", "dummy-secret-key", ""),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, cfgOpts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	var clientOpts []func(*bedrockruntime.Options)

	// Use custom base URL if set (proxy/gateway scenario)
	if model.BaseURL != "" {
		baseURL := strings.TrimRight(model.BaseURL, "/")
		clientOpts = append(clientOpts, func(o *bedrockruntime.Options) {
			o.BaseEndpoint = &baseURL
		})
	}

	return bedrockruntime.NewFromConfig(cfg, clientOpts...), nil
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

func buildConverseStreamInput(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) (*bedrockruntime.ConverseStreamInput, error) {
	input := &bedrockruntime.ConverseStreamInput{
		ModelId: strPtr(model.ID),
	}

	retention := resolveCacheRetention("")
	if options != nil {
		retention = resolveCacheRetention(options.CacheRetention)
	}
	canCache := supportsBedrockPromptCaching(model) && retention != ai.CacheNone

	// System prompt
	if ctx.SystemPrompt != "" {
		input.System = []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: ctx.SystemPrompt},
		}
		if canCache {
			input.System = append(input.System, &brtypes.SystemContentBlockMemberCachePoint{
				Value: bedrockCachePoint(retention),
			})
		}
	}

	// Messages
	input.Messages = convertBedrockMessages(ctx.Messages, model, canCache, retention)

	// Inference config
	if options != nil && (options.MaxTokens != nil || options.Temperature != nil) {
		ic := &brtypes.InferenceConfiguration{}
		if options.MaxTokens != nil {
			v := int32(*options.MaxTokens)
			ic.MaxTokens = &v
		}
		if options.Temperature != nil {
			v := float32(*options.Temperature)
			ic.Temperature = &v
		}
		input.InferenceConfig = ic
	}

	// Tools
	toolChoice := ""
	if options != nil {
		toolChoice = options.ToolChoice
	}
	if len(ctx.Tools) > 0 && toolChoice != "none" {
		input.ToolConfig = convertBedrockToolConfig(ctx.Tools, toolChoice)
	}

	// Thinking/reasoning config
	if options != nil && options.Headers != nil {
		if reasoning := options.Headers["x-bedrock-reasoning"]; reasoning != "" {
			if isBedrockAnthropicClaudeModel(model) && model.Reasoning {
				fields := buildBedrockAdditionalFields(model, reasoning, options)
				if fields != nil {
					input.AdditionalModelRequestFields = document.NewLazyDocument(fields)
				}
			}
		}
	}

	return input, nil
}

func convertBedrockMessages(messages []ai.Message, model *ai.Model, canCache bool, retention ai.CacheRetention) []brtypes.Message {
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
	var result []brtypes.Message

	for i := 0; i < len(transformed); i++ {
		msg := &transformed[i]

		if u := msg.AsUser(); u != nil {
			var blocks []brtypes.ContentBlock
			switch content := u.Content.(type) {
			case string:
				if strings.TrimSpace(content) != "" {
					blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: content})
				}
			case []any:
				for _, item := range content {
					if m, ok := item.(map[string]any); ok {
						switch m["type"] {
						case "text":
							if text, ok := m["text"].(string); ok {
								blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: text})
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
				result = append(result, brtypes.Message{
					Role:    brtypes.ConversationRoleUser,
					Content: blocks,
				})
			}

		} else if a := msg.AsAssistant(); a != nil {
			var blocks []brtypes.ContentBlock
			for _, block := range a.Content {
				switch {
				case block.IsText():
					if strings.TrimSpace(block.Text.Text) != "" {
						blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: block.Text.Text})
					}
				case block.IsToolCall():
					tc := block.ToolCall
					blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{
						Value: brtypes.ToolUseBlock{
							ToolUseId: strPtr(tc.ID),
							Name:      strPtr(tc.Name),
							Input:     document.NewLazyDocument(tc.Arguments),
						},
					})
				case block.IsThinking():
					if strings.TrimSpace(block.Thinking.Thinking) != "" {
						// Signatures arrive after thinking deltas. If a partial or externally
						// persisted message lacks a signature, Bedrock rejects the replayed
						// reasoning block. Fall back to plain text, matching Anthropic.
						if !supportsBedrockThinkingSignature(model) || strings.TrimSpace(block.Thinking.ThinkingSignature) == "" {
							blocks = append(blocks, &brtypes.ContentBlockMemberText{
								Value: block.Thinking.Thinking,
							})
						} else {
							reasoning := brtypes.ReasoningTextBlock{
								Text:      strPtr(block.Thinking.Thinking),
								Signature: strPtr(block.Thinking.ThinkingSignature),
							}
							blocks = append(blocks, &brtypes.ContentBlockMemberReasoningContent{
								Value: &brtypes.ReasoningContentBlockMemberReasoningText{
									Value: reasoning,
								},
							})
						}
					}
				}
			}
			if len(blocks) > 0 {
				result = append(result, brtypes.Message{
					Role:    brtypes.ConversationRoleAssistant,
					Content: blocks,
				})
			}

		} else if tr := msg.AsToolResult(); tr != nil {
			// Collect consecutive tool results into a single user message
			var blocks []brtypes.ContentBlock
			for {
				tr := transformed[i].AsToolResult()
				if tr == nil {
					break
				}
				var content []brtypes.ToolResultContentBlock
				for _, c := range tr.Content {
					if c.IsText() {
						content = append(content, &brtypes.ToolResultContentBlockMemberText{Value: c.Text})
					} else if c.IsImage() && model.SupportsImages() {
						imgBytes, err := base64.StdEncoding.DecodeString(c.Data)
						if err == nil {
							content = append(content, &brtypes.ToolResultContentBlockMemberImage{
								Value: brtypes.ImageBlock{
									Format: bedrockImageFormatSDK(c.MimeType),
									Source: &brtypes.ImageSourceMemberBytes{Value: imgBytes},
								},
							})
						}
					}
				}
				if len(content) == 0 {
					content = []brtypes.ToolResultContentBlock{
						&brtypes.ToolResultContentBlockMemberText{Value: ""},
					}
				}
				status := brtypes.ToolResultStatusSuccess
				if tr.IsError {
					status = brtypes.ToolResultStatusError
				}
				blocks = append(blocks, &brtypes.ContentBlockMemberToolResult{
					Value: brtypes.ToolResultBlock{
						ToolUseId: strPtr(tr.ToolCallID),
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
			result = append(result, brtypes.Message{
				Role:    brtypes.ConversationRoleUser,
				Content: blocks,
			})
		}
	}

	// Add cache point to last user message
	if canCache && len(result) > 0 {
		last := &result[len(result)-1]
		if last.Role == brtypes.ConversationRoleUser {
			last.Content = append(last.Content, &brtypes.ContentBlockMemberCachePoint{
				Value: bedrockCachePoint(retention),
			})
		}
	}

	return result
}

func convertImageBlock(m map[string]any) brtypes.ContentBlock {
	data, _ := m["data"].(string)
	mime, _ := m["mimeType"].(string)
	imgBytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Fall back to raw bytes if not base64
		imgBytes = []byte(data)
	}
	return &brtypes.ContentBlockMemberImage{
		Value: brtypes.ImageBlock{
			Format: bedrockImageFormatSDK(mime),
			Source: &brtypes.ImageSourceMemberBytes{Value: imgBytes},
		},
	}
}

func convertBedrockToolConfig(tools []ai.Tool, toolChoice string) *brtypes.ToolConfiguration {
	var sdkTools []brtypes.Tool
	for _, tool := range tools {
		sdkTools = append(sdkTools, &brtypes.ToolMemberToolSpec{
			Value: brtypes.ToolSpecification{
				Name:        strPtr(tool.Name),
				Description: strPtr(tool.Description),
				InputSchema: &brtypes.ToolInputSchemaMemberJson{
					Value: document.NewLazyDocument(tool.Parameters),
				},
			},
		})
	}

	config := &brtypes.ToolConfiguration{Tools: sdkTools}

	switch toolChoice {
	case "auto":
		config.ToolChoice = &brtypes.ToolChoiceMemberAuto{Value: brtypes.AutoToolChoice{}}
	case "any":
		config.ToolChoice = &brtypes.ToolChoiceMemberAny{Value: brtypes.AnyToolChoice{}}
	case "":
		// No explicit tool choice
	default:
		config.ToolChoice = &brtypes.ToolChoiceMemberTool{
			Value: brtypes.SpecificToolChoice{Name: strPtr(toolChoice)},
		}
	}

	return config
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

func bedrockCachePoint(retention ai.CacheRetention) brtypes.CachePointBlock {
	cp := brtypes.CachePointBlock{Type: brtypes.CachePointTypeDefault}
	if retention == ai.CacheLong {
		cp.Ttl = brtypes.CacheTTLOneHour
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
		// Normalise whitespace, underscores, periods, colons to dashes so
		// "Claude Opus 4.7" matches the canonical "opus-4-7" tokens.
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

// supportsBedrockPromptCaching determines whether cache points should be added.
//
// For base models and system-defined inference profiles the model ID / ARN
// contains the model name, so we can decide locally.
//
// For application inference profiles (whose ARNs don't contain the model name),
// we also check model.Name (user-controlled via models.json or RegisterProvider).
// As a last resort, set AWS_BEDROCK_FORCE_CACHE=1 to enable cache points.
// Amazon Nova models have automatic caching and don't need explicit cache points.
func supportsBedrockPromptCaching(model *ai.Model) bool {
	candidates := modelMatchCandidates(model.ID, model.Name)

	if !anyContains(candidates, "claude") {
		// No claude reference anywhere — likely an application inference profile
		// pointing at a non-Anthropic model, or the user didn't supply a name.
		// Allow forcing via env var.
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

// supportsBedrockThinkingSignature checks if the model supports thinking signatures.
// Checks both model ID and model name to support application inference profiles.
func supportsBedrockThinkingSignature(model *ai.Model) bool {
	return isBedrockAnthropicClaudeModel(model)
}

// isBedrockAnthropicClaudeModel reports whether a Bedrock model is an
// Anthropic Claude model. Checks ID and Name to support application inference
// profiles whose ARNs don't carry the model name.
func isBedrockAnthropicClaudeModel(model *ai.Model) bool {
	candidates := modelMatchCandidates(model.ID, model.Name)
	return anyContains(candidates, "anthropic.claude") ||
		anyContains(candidates, "anthropic/claude") ||
		anyContains(candidates, "claude")
}

// supportsBedrockAdaptiveThinking checks if the model supports adaptive thinking (Opus 4.6+, Sonnet 4.6).
// Checks both model ID and model name to support application inference profiles.
func supportsBedrockAdaptiveThinking(modelID, modelName string) bool {
	candidates := modelMatchCandidates(modelID, modelName)
	return anyContains(candidates, "opus-4-6") ||
		anyContains(candidates, "opus-4-7") ||
		anyContains(candidates, "sonnet-4-6")
}

// bedrockThinkingLevelToEffort maps thinking level to Bedrock effort value.
// Adaptive Bedrock models (Opus 4.6+, Sonnet 4.6+, Opus 4.7) all support
// "max". Only Opus 4.7 exposes a distinct "xhigh" tier; older adaptive
// models clamp xhigh down to "high".
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

// bedrockImageFormatSDK maps MIME types to Bedrock ImageFormat enum values.
func bedrockImageFormatSDK(mimeType string) brtypes.ImageFormat {
	switch mimeType {
	case "image/png":
		return brtypes.ImageFormatPng
	case "image/gif":
		return brtypes.ImageFormatGif
	case "image/webp":
		return brtypes.ImageFormatWebp
	default:
		return brtypes.ImageFormatJpeg
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

	// For adaptive models we preserve xhigh/max so bedrockThinkingLevelToEffort
	// can emit the right provider-side value (model-aware). For non-adaptive
	// budget-based models we fall back to ClampReasoning since there are no
	// separate budgets above "high".
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

// --- Helpers ---

func strPtr(s string) *string { return &s }

func derefI32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
