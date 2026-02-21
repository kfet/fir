// Ported from: packages/ai/src/providers/google.ts
// Upstream hash: 1caadb2e
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// --- Google Gemini API types ---

type googleResponse struct {
	Candidates    []googleCandidate  `json:"candidates"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata"`
}

type googleCandidate struct {
	Content      googleContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type googleContent struct {
	Parts []googlePart `json:"parts"`
	Role  string       `json:"role"`
}

type googlePart struct {
	Text             string              `json:"text,omitempty"`
	Thought          *bool               `json:"thought,omitempty"`
	ThoughtSignature string              `json:"thoughtSignature,omitempty"`
	FunctionCall     *googleFunctionCall  `json:"functionCall,omitempty"`
}

type googleFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	ID   string         `json:"id,omitempty"`
}

type googleUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThinkingTokenCount   int `json:"thinkingTokenCount"`
}

// StreamGoogle implements streaming for the Google Gemini API.
// Note: Gemini uses a single JSON response with streaming (not SSE),
// or the streamGenerateContent endpoint which returns line-delimited JSON.
func StreamGoogle(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
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

		err := streamGoogleHTTP(ctx, model, prompt, options, output, stream)
		if err != nil {
			if ctx.Err() != nil {
				output.StopReason = ai.StopReasonAborted
			} else {
				output.StopReason = ai.StopReasonError
			}
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{
				Type:   ai.EventError,
				Reason: output.StopReason,
				Error:  output,
			})
			stream.End(nil)
			return
		}

		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
		stream.End(nil)
	}()

	return stream
}

func streamGoogleHTTP(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) error {
	apiKey := ""
	if options != nil {
		apiKey = options.ApiKey
	}
	if apiKey == "" {
		apiKey = ai.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		return fmt.Errorf("no API key for provider: %s", model.Provider)
	}

	body, err := buildGoogleRequestBody(model, prompt, options)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	baseURL := model.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse",
		strings.TrimRight(baseURL, "/"), model.ID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	for k, v := range model.Headers {
		req.Header.Set(k, v)
	}
	if options != nil {
		for k, v := range options.Headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Don't wrap the original error — it may contain the full URL with the API key.
		return fmt.Errorf("Google API request failed (model=%s): connection error", model.ID)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%d: %s", resp.StatusCode, string(bodyBytes))
	}

	stream.Push(ai.AssistantMessageEvent{
		Type:    ai.EventStart,
		Partial: output,
	})

	return parseGoogleResponse(resp.Body, model, output, stream)
}

func parseGoogleResponse(
	reader io.Reader,
	model *ai.Model,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) error {
	// Google's streamGenerateContent with alt=sse returns SSE-formatted line-delimited JSON
	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	// Parse SSE data lines
	lines := strings.Split(string(bodyBytes), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		var resp googleResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue
		}

		// Process usage
		if resp.UsageMetadata != nil {
			output.Usage.Input = resp.UsageMetadata.PromptTokenCount
			output.Usage.Output = resp.UsageMetadata.CandidatesTokenCount + resp.UsageMetadata.ThinkingTokenCount
			output.Usage.CacheRead = resp.UsageMetadata.CachedContentTokenCount
			output.Usage.TotalTokens = resp.UsageMetadata.TotalTokenCount
			ai.CalculateCost(model, &output.Usage)
		}

		// Process candidates
		for _, candidate := range resp.Candidates {
			if candidate.FinishReason != "" {
				output.StopReason = mapGoogleStopReason(candidate.FinishReason)
				// Override with toolUse if tool calls are present
				for _, c := range output.Content {
					if c.IsToolCall() {
						output.StopReason = ai.StopReasonToolUse
						break
					}
				}
			}

			for _, part := range candidate.Content.Parts {
				if part.FunctionCall != nil {
					// Tool call — use provided ID, or deduplicate if needed
					idx := len(output.Content)
					id := part.FunctionCall.ID
					if id == "" || toolCallIDExists(output, id) {
						id = fmt.Sprintf("%s_%d_%d", part.FunctionCall.Name, time.Now().UnixMilli(), idx)
					}
					tc := ai.NewToolCallContent(id, part.FunctionCall.Name, part.FunctionCall.Args)
					if part.ThoughtSignature != "" {
						tc.ToolCall.ThoughtSignature = part.ThoughtSignature
					}
					output.Content = append(output.Content, tc)
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventToolcallStart,
						ContentIndex: idx,
						Partial:      output,
					})
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventToolcallEnd,
						ContentIndex: idx,
						ToolCall:     output.Content[idx].ToolCall,
						Partial:      output,
					})
				} else if part.Thought != nil && *part.Thought {
					// Thinking
					idx := len(output.Content)
					output.Content = append(output.Content, ai.NewThinkingContent(part.Text))
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingStart,
						ContentIndex: idx,
						Partial:      output,
					})
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingDelta,
						ContentIndex: idx,
						Delta:        part.Text,
						Partial:      output,
					})
					stream.Push(ai.AssistantMessageEvent{
						Type:         ai.EventThinkingEnd,
						ContentIndex: idx,
						Content:      part.Text,
						Partial:      output,
					})
				} else if part.Text != "" {
					// Text
					idx := len(output.Content)
					// Merge with existing text block if possible
					if idx > 0 && output.Content[idx-1].IsText() {
						c := output.Content[idx-1]
						c.Text.Text += part.Text
						output.Content[idx-1] = c
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextDelta,
							ContentIndex: idx - 1,
							Delta:        part.Text,
							Partial:      output,
						})
					} else {
						output.Content = append(output.Content, ai.NewTextContent(part.Text))
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextStart,
							ContentIndex: idx,
							Partial:      output,
						})
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextDelta,
							ContentIndex: idx,
							Delta:        part.Text,
							Partial:      output,
						})
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventTextEnd,
							ContentIndex: idx,
							Content:      part.Text,
							Partial:      output,
						})
					}
				}
			}
		}
	}

	return nil
}

// toolCallIDExists checks if a tool call with the given ID already exists in the output.
func toolCallIDExists(output *ai.AssistantMessage, id string) bool {
	for _, c := range output.Content {
		if c.IsToolCall() && c.ToolCall.ID == id {
			return true
		}
	}
	return false
}

func mapGoogleStopReason(reason string) ai.StopReason {
	switch reason {
	case "STOP":
		return ai.StopReasonStop
	case "MAX_TOKENS":
		return ai.StopReasonLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

// --- Request body building ---

func buildGoogleRequestBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions) ([]byte, error) {
	body := map[string]any{}

	// Generation config
	genConfig := map[string]any{}
	maxTokens := 0
	if options != nil && options.MaxTokens != nil {
		maxTokens = *options.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = model.MaxTokens
		if maxTokens > 32000 {
			maxTokens = 32000
		}
	}
	genConfig["maxOutputTokens"] = maxTokens

	if options != nil && options.Temperature != nil {
		genConfig["temperature"] = *options.Temperature
	}

	body["generationConfig"] = genConfig

	// System instruction
	if ctx.SystemPrompt != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{
				{"text": ctx.SystemPrompt},
			},
		}
	}

	// Messages
	var contents []map[string]any
	transformed := TransformMessages(ctx.Messages, model, nil)
	for _, msg := range transformed {
		if msg.AsUser() != nil {
			um := msg.AsUser()
			if s, ok := um.Content.(string); ok && strings.TrimSpace(s) != "" {
				contents = append(contents, map[string]any{
					"role":  "user",
					"parts": []map[string]any{{"text": s}},
				})
			}
		} else if msg.AsAssistant() != nil {
			am := msg.AsAssistant()
			var parts []map[string]any
			for _, block := range am.Content {
				switch {
				case block.IsText():
					parts = append(parts, map[string]any{"text": block.Text.Text})
				case block.IsToolCall():
					tc := block.ToolCall
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{
							"name": tc.Name,
							"args": tc.Arguments,
						},
					})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": parts,
				})
			}
		} else if msg.AsToolResult() != nil {
			tr := msg.AsToolResult()
			var text string
			for _, c := range tr.Content {
				if c.IsText() {
					text += c.Text
				}
			}
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{
						"functionResponse": map[string]any{
							"name": tr.ToolName,
							"response": map[string]any{
								"result": text,
							},
						},
					},
				},
			})
		}
	}
	body["contents"] = contents

	// Tools
	if len(ctx.Tools) > 0 {
		var funcDecls []map[string]any
		for _, tool := range ctx.Tools {
			funcDecls = append(funcDecls, map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			})
		}
		body["tools"] = []map[string]any{
			{"functionDeclarations": funcDecls},
		}
	}

	// Thinking config (passed via headers from StreamSimple)
	if options != nil && options.Headers != nil {
		if options.Headers["x-google-thinking-enabled"] == "true" {
			thinkingConfig := map[string]any{
				"includeThoughts": true,
			}
			if level := options.Headers["x-google-thinking-level"]; level != "" {
				thinkingConfig["thinkingLevel"] = level
			} else if budget := options.Headers["x-google-thinking-budget"]; budget != "" {
				var b int
				fmt.Sscanf(budget, "%d", &b)
				if b > 0 {
					thinkingConfig["thinkingBudget"] = b
				}
			}
			genConfig["thinkingConfig"] = thinkingConfig
		}
	}

	return json.Marshal(body)
}

// --- StreamSimple wrapper ---

func StreamSimpleGoogle(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
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

	if base.Headers == nil {
		base.Headers = map[string]string{}
	}

	if options != nil && options.Reasoning != "" && model.Reasoning {
		base.Headers["x-google-thinking-enabled"] = "true"

		effort := ClampReasoning(options.Reasoning)

		// Gemini 3 models use thinking levels
		if isGemini3Model(model) {
			level := mapGeminiThinkingLevel(effort, model)
			base.Headers["x-google-thinking-level"] = level
		} else {
			// Older models use budget-based thinking
			budget := getGoogleBudget(model, effort, options.ThinkingBudgets)
			base.Headers["x-google-thinking-budget"] = fmt.Sprintf("%d", budget)
		}
	}

	return StreamGoogle(ctx, model, prompt, base)
}

// isGemini3Model returns true for Gemini 3 models (pro or flash).
func isGemini3Model(model *ai.Model) bool {
	id := strings.ToLower(model.ID)
	return strings.Contains(id, "gemini-3")
}

// mapGeminiThinkingLevel maps our thinking levels to Gemini's thinking levels.
// Gemini 3 Pro only supports LOW and HIGH. Gemini 3 Flash supports MINIMAL-HIGH.
func mapGeminiThinkingLevel(level ai.ThinkingLevel, model *ai.Model) string {
	id := strings.ToLower(model.ID)
	isPro := strings.Contains(id, "3-pro")

	if isPro {
		// Gemini 3 Pro: only LOW and HIGH
		switch level {
		case ai.ThinkingMinimal, ai.ThinkingLow:
			return "LOW"
		case ai.ThinkingMedium, ai.ThinkingHigh, ai.ThinkingXHigh:
			return "HIGH"
		default:
			return "HIGH"
		}
	}

	// Gemini 3 Flash: MINIMAL, LOW, MEDIUM, HIGH
	switch level {
	case ai.ThinkingMinimal:
		return "MINIMAL"
	case ai.ThinkingLow:
		return "LOW"
	case ai.ThinkingMedium:
		return "MEDIUM"
	case ai.ThinkingHigh, ai.ThinkingXHigh:
		return "HIGH"
	default:
		return "MEDIUM"
	}
}

// getGoogleBudget returns the thinking budget tokens for Google models.
// Budget values match the upstream TypeScript implementation.
func getGoogleBudget(model *ai.Model, effort ai.ThinkingLevel, budgets *ai.ThinkingBudgets) int {
	if b := budgets.BudgetForLevel(effort); b > 0 {
		return b
	}

	id := strings.ToLower(model.ID)

	if strings.Contains(id, "2.5-pro") {
		switch effort {
		case ai.ThinkingMinimal:
			return 128
		case ai.ThinkingLow:
			return 2048
		case ai.ThinkingMedium:
			return 8192
		case ai.ThinkingHigh, ai.ThinkingXHigh:
			return 32768
		default:
			return 8192
		}
	}

	if strings.Contains(id, "2.5-flash") {
		switch effort {
		case ai.ThinkingMinimal:
			return 128
		case ai.ThinkingLow:
			return 2048
		case ai.ThinkingMedium:
			return 8192
		case ai.ThinkingHigh, ai.ThinkingXHigh:
			return 24576
		default:
			return 8192
		}
	}

	// Dynamic budget for other models
	return -1
}

// RegisterGoogle registers the Google Gemini provider.
func RegisterGoogle(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiGoogleGenerativeAI,
		Stream:       StreamGoogle,
		StreamSimple: StreamSimpleGoogle,
	}, "builtin")
}
