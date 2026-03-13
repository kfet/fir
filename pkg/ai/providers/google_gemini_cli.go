// Ported from: packages/ai/src/providers/google-gemini-cli.ts
// Upstream hash: c99b9940
package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/ratelimit"
	firlog "github.com/kfet/fir/pkg/log"
)

// --- Constants ---

const (
	geminiCLIDefaultEndpoint    = "https://cloudcode-pa.googleapis.com"
	antigravityDailyEndpoint    = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityAutopushEndpoint = "https://autopush-cloudcode-pa.sandbox.googleapis.com"
	defaultAntigravityVersion   = "1.18.4"
	claudeThinkingBetaHeader    = "interleaved-thinking-2025-05-14"
	geminiCLIMaxRetries         = 3
	geminiCLIBaseDelayMs        = 1000
	geminiCLIMaxEmptyRetries    = 2
	geminiCLIEmptyBaseDelayMs   = 500
	geminiCLIDefaultMaxDelayMs  = 60000
)

// toolCallCounter generates unique tool call IDs.
var toolCallCounter atomic.Int64

// --- Google thinking levels ---

// GoogleThinkingLevel mirrors Google's ThinkingLevel enum.
type GoogleThinkingLevel string

const (
	ThinkingLevelUnspecified GoogleThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	ThinkingLevelMinimal     GoogleThinkingLevel = "MINIMAL"
	ThinkingLevelLow         GoogleThinkingLevel = "LOW"
	ThinkingLevelMedium      GoogleThinkingLevel = "MEDIUM"
	ThinkingLevelHigh        GoogleThinkingLevel = "HIGH"
)

// --- Headers ---

func geminiCLIHeaders() map[string]string {
	return map[string]string{
		"User-Agent":        "google-cloud-sdk vscode_cloudshelleditor/0.1",
		"X-Goog-Api-Client": "gl-node/22.17.0",
		"Client-Metadata":   `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`,
	}
}

func antigravityHeaders() map[string]string {
	version := defaultAntigravityVersion
	return map[string]string{
		"User-Agent": fmt.Sprintf("antigravity/%s %s/%s", version, runtime.GOOS, runtime.GOARCH),
	}
}

const antigravitySystemInstruction = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding." +
	"You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question." +
	"**Absolute paths only**" +
	"**Proactiveness**"

// --- Request types ---

type cloudCodeAssistRequest struct {
	Project     string                  `json:"project"`
	Model       string                  `json:"model"`
	Request     cloudCodeAssistInnerReq `json:"request"`
	RequestType string                  `json:"requestType,omitempty"`
	UserAgent   string                  `json:"userAgent,omitempty"`
	RequestID   string                  `json:"requestId,omitempty"`
}

type cloudCodeAssistInnerReq struct {
	Contents          []GoogleContent   `json:"contents"`
	SessionID         string            `json:"sessionId,omitempty"`
	SystemInstruction *googleSysInstr   `json:"systemInstruction,omitempty"`
	GenerationConfig  map[string]any    `json:"generationConfig,omitempty"`
	Tools             []map[string]any  `json:"tools,omitempty"`
	ToolConfig        *googleToolConfig `json:"toolConfig,omitempty"`
}

type googleSysInstr struct {
	Role  string               `json:"role,omitempty"`
	Parts []googleSysInstrPart `json:"parts"`
}

type googleSysInstrPart struct {
	Text string `json:"text"`
}

type googleToolConfig struct {
	FunctionCallingConfig struct {
		Mode string `json:"mode"`
	} `json:"functionCallingConfig"`
}

// --- Response types ---

type cloudCodeAssistChunk struct {
	Response *cloudCodeAssistResp `json:"response,omitempty"`
}

type cloudCodeAssistResp struct {
	Candidates    []cloudCodeAssistCandidate `json:"candidates,omitempty"`
	UsageMetadata *geminiCLIUsageMetadata    `json:"usageMetadata,omitempty"`
}

type cloudCodeAssistCandidate struct {
	Content      *cloudCodeAssistContent `json:"content,omitempty"`
	FinishReason string                  `json:"finishReason,omitempty"`
}

type cloudCodeAssistContent struct {
	Role  string                `json:"role"`
	Parts []cloudCodeAssistPart `json:"parts,omitempty"`
}

type cloudCodeAssistPart struct {
	Text             string                   `json:"text,omitempty"`
	Thought          *bool                    `json:"thought,omitempty"`
	ThoughtSignature string                   `json:"thoughtSignature,omitempty"`
	FunctionCall     *cloudCodeAssistFuncCall `json:"functionCall,omitempty"`
}

type cloudCodeAssistFuncCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
	ID   string         `json:"id,omitempty"`
}

type geminiCLIUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

// --- Retry delay extraction ---

// extractRetryDelay extracts retry delay from error text and response headers.
// Returns delay in milliseconds, or 0 if not found.
// Text-based pattern matching is delegated to ratelimit.ExtractRetryDelayFromText.
func extractRetryDelay(errorText string, headers http.Header) int {
	normalizeDelay := func(ms int) int {
		if ms > 0 {
			return ms + 1000
		}
		return 0
	}

	if headers != nil {
		// Check Retry-After header
		if ra := headers.Get("Retry-After"); ra != "" {
			if sec, err := strconv.Atoi(ra); err == nil {
				if d := normalizeDelay(sec * 1000); d > 0 {
					return d
				}
			}
			if t, err := time.Parse(time.RFC1123, ra); err == nil {
				if d := normalizeDelay(int(time.Until(t).Milliseconds())); d > 0 {
					return d
				}
			}
		}
		// Check x-ratelimit-reset
		if rr := headers.Get("x-ratelimit-reset"); rr != "" {
			if sec, err := strconv.Atoi(rr); err == nil {
				if d := normalizeDelay(sec*1000 - int(time.Now().UnixMilli())); d > 0 {
					return d
				}
			}
		}
		// Check x-ratelimit-reset-after
		if rra := headers.Get("x-ratelimit-reset-after"); rra != "" {
			if sec, err := strconv.ParseFloat(rra, 64); err == nil {
				if d := normalizeDelay(int(sec * 1000)); d > 0 {
					return d
				}
			}
		}
	}

	// Delegate text-based pattern matching to the shared rate-limit utility.
	if d := ratelimit.ExtractRetryDelayFromText(errorText); d > 0 {
		return normalizeDelay(int(d.Milliseconds()))
	}

	return 0
}

func needsClaudeThinkingBetaHeader(model *ai.Model) bool {
	return model.Provider == "google-antigravity" && strings.HasPrefix(model.ID, "claude-") && model.Reasoning
}

func isGemini3ProModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return gemini3ProRegex.MatchString(lower)
}

func isGemini3FlashModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return gemini3FlashRegex.MatchString(lower)
}

func isGemini3ModelID(modelID string) bool {
	return isGemini3ProModel(modelID) || isGemini3FlashModel(modelID)
}

var gemini3ProRegex = regexp.MustCompile(`gemini-3(?:\.1)?-pro`)
var gemini3FlashRegex = regexp.MustCompile(`gemini-3(?:\.1)?-flash`)
var retryableTextRe = regexp.MustCompile(`(?i)service.?unavailable|other.?side.?closed`)

func isRetryableError(status int, errorText string) bool {
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
		return true
	}
	if ratelimit.IsRateLimitText(errorText) {
		return true
	}
	return retryableTextRe.MatchString(errorText)
}

func extractErrorMessage(errorText string) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(errorText), &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return errorText
}

// --- Build request ---

func buildGeminiCLIRequest(
	model *ai.Model,
	ctx ai.Context,
	projectID string,
	options *ai.StreamOptions,
	isAntigravity bool,
) *cloudCodeAssistRequest {
	contents := ConvertGoogleMessages(model, ctx)

	genConfig := map[string]any{}
	if options != nil && options.Temperature != nil {
		genConfig["temperature"] = *options.Temperature
	}
	if options != nil && options.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *options.MaxTokens
	}

	// Thinking config (passed via headers)
	if options != nil && options.Headers != nil {
		if options.Headers["x-gemini-thinking-enabled"] == "true" && model.Reasoning {
			thinkingConfig := map[string]any{"includeThoughts": true}
			if level := options.Headers["x-gemini-thinking-level"]; level != "" {
				thinkingConfig["thinkingLevel"] = level
			} else if budget := options.Headers["x-gemini-thinking-budget"]; budget != "" {
				if b, err := strconv.Atoi(budget); err == nil && b > 0 {
					thinkingConfig["thinkingBudget"] = b
				}
			}
			genConfig["thinkingConfig"] = thinkingConfig
		}
	}

	req := cloudCodeAssistInnerReq{
		Contents: contents,
	}

	if options != nil && options.SessionID != "" {
		req.SessionID = options.SessionID
	}

	if ctx.SystemPrompt != "" {
		sysInstr := &googleSysInstr{
			Parts: []googleSysInstrPart{{Text: SanitizeSurrogates(ctx.SystemPrompt)}},
		}
		req.SystemInstruction = sysInstr
	}

	if len(genConfig) > 0 {
		req.GenerationConfig = genConfig
	}

	if len(ctx.Tools) > 0 {
		useParameters := strings.HasPrefix(model.ID, "claude-")
		req.Tools = ConvertGoogleTools(ctx.Tools, useParameters)
		if options != nil && options.ToolChoice != "" {
			tc := &googleToolConfig{}
			tc.FunctionCallingConfig.Mode = MapGoogleToolChoice(options.ToolChoice)
			req.ToolConfig = tc
		}
	}

	if isAntigravity {
		existingParts := []googleSysInstrPart{}
		if req.SystemInstruction != nil {
			existingParts = req.SystemInstruction.Parts
		}
		req.SystemInstruction = &googleSysInstr{
			Role: "user",
			Parts: append([]googleSysInstrPart{
				{Text: antigravitySystemInstruction},
				{Text: fmt.Sprintf("Please ignore following [ignore]%s[/ignore]", antigravitySystemInstruction)},
			}, existingParts...),
		}
	}

	reqType := ""
	userAgent := "fir-coding-agent"
	if isAntigravity {
		reqType = "agent"
		userAgent = "antigravity"
	}

	return &cloudCodeAssistRequest{
		Project:     projectID,
		Model:       model.ID,
		Request:     req,
		RequestType: reqType,
		UserAgent:   userAgent,
		RequestID:   fmt.Sprintf("%s-%d-%s", userAgent, time.Now().UnixMilli(), randomID()),
	}
}

func randomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 9)
	rand.Read(b)
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}

// --- Stream function ---

// StreamGoogleGeminiCLI streams from the Google Gemini CLI / Cloud Code Assist API.
func StreamGoogleGeminiCLI(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
) *ai.AssistantMessageEventStream {
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

		firlog.Debug("gemini-cli request", "model", model.ID, "messageCount", len(prompt.Messages))
		err := streamGeminiCLI(ctx, model, prompt, options, output, stream)
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

		firlog.Debug("gemini-cli response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
		stream.End(nil)
	}()

	return stream
}

func streamGeminiCLI(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) error {
	apiKeyRaw := ""
	if options != nil {
		apiKeyRaw = options.ApiKey
	}
	if apiKeyRaw == "" {
		return fmt.Errorf("Google Cloud Code Assist requires OAuth authentication. Use /login to authenticate")
	}

	// Parse JSON-encoded credentials: { "token": "...", "projectId": "..." }
	var creds struct {
		Token     string `json:"token"`
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal([]byte(apiKeyRaw), &creds); err != nil {
		return fmt.Errorf("invalid Google Cloud Code Assist credentials. Use /login to re-authenticate")
	}
	if creds.Token == "" || creds.ProjectID == "" {
		return fmt.Errorf("missing token or projectId in Google Cloud credentials. Use /login to re-authenticate")
	}

	isAntigravity := model.Provider == "google-antigravity"
	baseURL := strings.TrimSpace(model.BaseURL)
	var endpoints []string
	if baseURL != "" {
		endpoints = []string{baseURL}
	} else if isAntigravity {
		endpoints = []string{antigravityDailyEndpoint, antigravityAutopushEndpoint, geminiCLIDefaultEndpoint}
	} else {
		endpoints = []string{geminiCLIDefaultEndpoint}
	}

	reqBody := buildGeminiCLIRequest(model, prompt, creds.ProjectID, options, isAntigravity)
	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	hdrs := geminiCLIHeaders()
	if isAntigravity {
		hdrs = antigravityHeaders()
	}
	hdrs["Authorization"] = "Bearer " + creds.Token
	hdrs["Content-Type"] = "application/json"
	hdrs["Accept"] = "text/event-stream"
	if needsClaudeThinkingBetaHeader(model) {
		hdrs["anthropic-beta"] = claudeThinkingBetaHeader
	}
	if options != nil {
		for k, v := range options.Headers {
			hdrs[k] = v
		}
	}

	// Retry loop with endpoint fallback.
	// On 403/404, immediately try the next endpoint (no delay).
	// On 429/5xx, retry with backoff on the same or next endpoint.
	var resp *http.Response
	var lastErr error
	var requestURL string
	endpointIndex := 0

	for attempt := 0; attempt <= geminiCLIMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("request was aborted")
		}

		requestURL = endpoints[endpointIndex] + "/v1internal:streamGenerateContent?alt=sse"

		req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(bodyJSON))
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}

		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("network error: %v", err)
			if attempt < geminiCLIMaxRetries {
				delay := geminiCLIBaseDelayMs * int(math.Pow(2, float64(attempt)))
				sleepCtx(ctx, time.Duration(delay)*time.Millisecond)
				continue
			}
			return lastErr
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			break
		}

		errorBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		errorText := string(errorBody)

		// On 403/404, cascade to the next endpoint immediately (no delay)
		if (resp.StatusCode == 403 || resp.StatusCode == 404) && endpointIndex < len(endpoints)-1 {
			endpointIndex++
			continue
		}

		if attempt < geminiCLIMaxRetries && isRetryableError(resp.StatusCode, errorText) {
			// Advance endpoint if possible
			if endpointIndex < len(endpoints)-1 {
				endpointIndex++
			}

			serverDelay := extractRetryDelay(errorText, resp.Header)
			delayMs := serverDelay
			if delayMs == 0 {
				delayMs = geminiCLIBaseDelayMs * int(math.Pow(2, float64(attempt)))
			}

			maxDelayMs := geminiCLIDefaultMaxDelayMs
			if options != nil && options.MaxRetryDelayMs != nil {
				maxDelayMs = *options.MaxRetryDelayMs
			}
			if maxDelayMs > 0 && serverDelay > 0 && serverDelay > maxDelayMs {
				return fmt.Errorf("server requested %ds retry delay (max: %ds). %s",
					serverDelay/1000, maxDelayMs/1000, extractErrorMessage(errorText))
			}

			sleepCtx(ctx, time.Duration(delayMs)*time.Millisecond)
			continue
		}

		return fmt.Errorf("Cloud Code Assist API error (%d): %s", resp.StatusCode, extractErrorMessage(errorText))
	}

	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("failed to get response after retries")
	}
	defer resp.Body.Close()

	// Empty stream retry loop
	for emptyAttempt := 0; emptyAttempt <= geminiCLIMaxEmptyRetries; emptyAttempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("request was aborted")
		}

		if emptyAttempt > 0 {
			delay := geminiCLIEmptyBaseDelayMs * int(math.Pow(2, float64(emptyAttempt-1)))
			sleepCtx(ctx, time.Duration(delay)*time.Millisecond)

			req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewReader(bodyJSON))
			if err != nil {
				return fmt.Errorf("creating retry request: %w", err)
			}
			for k, v := range hdrs {
				req.Header.Set(k, v)
			}
			retryResp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("network error on retry: %v", err)
			}
			if retryResp.StatusCode < 200 || retryResp.StatusCode >= 300 {
				body, _ := io.ReadAll(retryResp.Body)
				retryResp.Body.Close()
				return fmt.Errorf("Cloud Code Assist API error (%d): %s", retryResp.StatusCode, string(body))
			}
			resp.Body.Close()
			resp = retryResp

			// Reset output for retry
			output.Content = nil
			output.Usage = ai.Usage{}
			output.StopReason = ai.StopReasonStop
			output.ErrorMessage = ""
			output.Timestamp = time.Now().UnixMilli()
		}

		hasContent, err := parseGeminiCLIStream(ctx, resp.Body, model, output, stream)
		if err != nil {
			return err
		}
		if hasContent {
			if output.StopReason == ai.StopReasonError || output.StopReason == ai.StopReasonAborted {
				return fmt.Errorf("an unknown error occurred")
			}
			return nil
		}
	}

	return fmt.Errorf("Cloud Code Assist API returned an empty response")
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// parseGeminiCLIStream parses SSE stream from Cloud Code Assist.
// Returns true if content was received, false for empty streams.
func parseGeminiCLIStream(
	ctx context.Context,
	body io.Reader,
	model *ai.Model,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) (bool, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return false, fmt.Errorf("reading response: %w", err)
	}

	hasContent := false
	started := false
	var currentBlockType string // "text" or "thinking"

	ensureStarted := func() {
		if !started {
			stream.Push(ai.AssistantMessageEvent{
				Type:    ai.EventStart,
				Partial: output,
			})
			started = true
		}
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if ctx.Err() != nil {
			return hasContent, fmt.Errorf("request was aborted")
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}
		jsonStr := strings.TrimSpace(line[5:])
		if jsonStr == "" {
			continue
		}

		var chunk cloudCodeAssistChunk
		if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
			continue
		}

		resp := chunk.Response
		if resp == nil {
			continue
		}

		if len(resp.Candidates) > 0 {
			candidate := resp.Candidates[0]
			if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
				for _, part := range candidate.Content.Parts {
					if part.Text != "" || (part.Thought != nil && *part.Thought) {
						hasContent = true
						isThinking := part.Thought != nil && *part.Thought

						// Check if we need to switch block type
						newType := "text"
						if isThinking {
							newType = "thinking"
						}

						if currentBlockType != newType {
							// End previous block
							if currentBlockType != "" {
								idx := len(output.Content) - 1
								if currentBlockType == "text" {
									stream.Push(ai.AssistantMessageEvent{
										Type:         ai.EventTextEnd,
										ContentIndex: idx,
										Content:      output.Content[idx].Text.Text,
										Partial:      output,
									})
								} else {
									stream.Push(ai.AssistantMessageEvent{
										Type:         ai.EventThinkingEnd,
										ContentIndex: idx,
										Content:      output.Content[idx].Thinking.Thinking,
										Partial:      output,
									})
								}
							}

							// Start new block
							idx := len(output.Content)
							if isThinking {
								output.Content = append(output.Content, ai.NewThinkingContent(""))
								ensureStarted()
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventThinkingStart,
									ContentIndex: idx,
									Partial:      output,
								})
							} else {
								output.Content = append(output.Content, ai.NewTextContent(""))
								ensureStarted()
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventTextStart,
									ContentIndex: idx,
									Partial:      output,
								})
							}
							currentBlockType = newType
						}

						idx := len(output.Content) - 1
						if isThinking {
							c := output.Content[idx]
							c.Thinking.Thinking += part.Text
							if part.ThoughtSignature != "" {
								c.Thinking.ThinkingSignature = RetainThoughtSignature(
									c.Thinking.ThinkingSignature, part.ThoughtSignature)
							}
							output.Content[idx] = c
							stream.Push(ai.AssistantMessageEvent{
								Type:         ai.EventThinkingDelta,
								ContentIndex: idx,
								Delta:        part.Text,
								Partial:      output,
							})
						} else {
							c := output.Content[idx]
							c.Text.Text += part.Text
							if part.ThoughtSignature != "" {
								c.Text.TextSignature = RetainThoughtSignature(
									c.Text.TextSignature, part.ThoughtSignature)
							}
							output.Content[idx] = c
							stream.Push(ai.AssistantMessageEvent{
								Type:         ai.EventTextDelta,
								ContentIndex: idx,
								Delta:        part.Text,
								Partial:      output,
							})
						}
					}

					if part.FunctionCall != nil {
						hasContent = true
						// End current text/thinking block
						if currentBlockType != "" {
							idx := len(output.Content) - 1
							if currentBlockType == "text" {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventTextEnd,
									ContentIndex: idx,
									Content:      output.Content[idx].Text.Text,
									Partial:      output,
								})
							} else {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventThinkingEnd,
									ContentIndex: idx,
									Content:      output.Content[idx].Thinking.Thinking,
									Partial:      output,
								})
							}
							currentBlockType = ""
						}

						// Generate unique tool call ID
						providedID := part.FunctionCall.ID
						needsNewID := providedID == ""
						if !needsNewID {
							for _, b := range output.Content {
								if b.IsToolCall() && b.ToolCall.ID == providedID {
									needsNewID = true
									break
								}
							}
						}
						toolCallID := providedID
						if needsNewID {
							toolCallID = fmt.Sprintf("%s_%d_%d",
								part.FunctionCall.Name, time.Now().UnixMilli(), toolCallCounter.Add(1))
						}

						tc := ai.NewToolCallContent(toolCallID, part.FunctionCall.Name, part.FunctionCall.Args)
						if part.ThoughtSignature != "" {
							tc.ToolCall.ThoughtSignature = part.ThoughtSignature
						}
						output.Content = append(output.Content, tc)
						idx := len(output.Content) - 1
						ensureStarted()

						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventToolcallStart,
							ContentIndex: idx,
							Partial:      output,
						})
						argsJSON, _ := json.Marshal(tc.ToolCall.Arguments)
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventToolcallDelta,
							ContentIndex: idx,
							Delta:        string(argsJSON),
							Partial:      output,
						})
						stream.Push(ai.AssistantMessageEvent{
							Type:         ai.EventToolcallEnd,
							ContentIndex: idx,
							ToolCall:     tc.ToolCall,
							Partial:      output,
						})
					}
				}
			}

			if candidate.FinishReason != "" {
				output.StopReason = MapGoogleStopReason(candidate.FinishReason)
				for _, b := range output.Content {
					if b.IsToolCall() {
						output.StopReason = ai.StopReasonToolUse
						break
					}
				}
			}
		}

		if resp.UsageMetadata != nil {
			u := resp.UsageMetadata
			cacheRead := u.CachedContentTokenCount
			output.Usage = ai.Usage{
				Input:       u.PromptTokenCount - cacheRead,
				Output:      u.CandidatesTokenCount + u.ThoughtsTokenCount,
				CacheRead:   cacheRead,
				CacheWrite:  0,
				TotalTokens: u.TotalTokenCount,
			}
			ai.CalculateCost(model, &output.Usage)
		}
	}

	// End last block
	if currentBlockType != "" {
		idx := len(output.Content) - 1
		if currentBlockType == "text" {
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventTextEnd,
				ContentIndex: idx,
				Content:      output.Content[idx].Text.Text,
				Partial:      output,
			})
		} else {
			stream.Push(ai.AssistantMessageEvent{
				Type:         ai.EventThinkingEnd,
				ContentIndex: idx,
				Content:      output.Content[idx].Thinking.Thinking,
				Partial:      output,
			})
		}
	}

	return hasContent, nil
}

// --- StreamSimple wrapper ---

// StreamSimpleGoogleGeminiCLI wraps StreamGoogleGeminiCLI with simple options.
func StreamSimpleGoogleGeminiCLI(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.SimpleStreamOptions,
) *ai.AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.ApiKey
	}
	if apiKey == "" {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			output := &ai.AssistantMessage{
				Role:         ai.RoleAssistant,
				Content:      []ai.AssistantContent{},
				Api:          model.Api,
				Provider:     model.Provider,
				Model:        model.ID,
				Usage:        ai.ZeroUsage(),
				StopReason:   ai.StopReasonError,
				ErrorMessage: "Google Cloud Code Assist requires OAuth authentication. Use /login to authenticate",
				Timestamp:    time.Now().UnixMilli(),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			stream.End(nil)
		}()
		return stream
	}

	base := BuildBaseOptions(model, options, apiKey)
	if options == nil || options.Reasoning == "" {
		base.Headers = mergeHeaders(base.Headers, map[string]string{
			"x-gemini-thinking-enabled": "false",
		})
		return StreamGoogleGeminiCLI(ctx, model, prompt, base)
	}

	effort := ClampReasoning(options.Reasoning)
	if isGemini3ModelID(model.ID) {
		level := getGeminiCLIThinkingLevel(effort, model.ID)
		base.Headers = mergeHeaders(base.Headers, map[string]string{
			"x-gemini-thinking-enabled": "true",
			"x-gemini-thinking-level":   string(level),
		})
		return StreamGoogleGeminiCLI(ctx, model, prompt, base)
	}

	maxTokens, thinkingBudget := AdjustMaxTokensForThinking(
		derefInt(base.MaxTokens, 0), model.MaxTokens, effort, options.ThinkingBudgets)
	base.MaxTokens = &maxTokens
	base.Headers = mergeHeaders(base.Headers, map[string]string{
		"x-gemini-thinking-enabled": "true",
		"x-gemini-thinking-budget":  strconv.Itoa(thinkingBudget),
	})
	return StreamGoogleGeminiCLI(ctx, model, prompt, base)
}

func getGeminiCLIThinkingLevel(effort ai.ThinkingLevel, modelID string) GoogleThinkingLevel {
	if isGemini3ProModel(modelID) {
		switch effort {
		case ai.ThinkingMinimal, ai.ThinkingLow:
			return ThinkingLevelLow
		case ai.ThinkingMedium, ai.ThinkingHigh:
			return ThinkingLevelHigh
		}
	}
	switch effort {
	case ai.ThinkingMinimal:
		return ThinkingLevelMinimal
	case ai.ThinkingLow:
		return ThinkingLevelLow
	case ai.ThinkingMedium:
		return ThinkingLevelMedium
	case ai.ThinkingHigh:
		return ThinkingLevelHigh
	}
	return ThinkingLevelMedium
}

func mergeHeaders(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

func derefInt(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

// --- Registration ---

// RegisterGoogleGeminiCLI registers the google-gemini-cli and google-antigravity providers.
func RegisterGoogleGeminiCLI(r *ai.Registry) {
	r.RegisterApiProvider(&ai.ApiProvider{
		Api:          "google-gemini-cli",
		Stream:       StreamGoogleGeminiCLI,
		StreamSimple: StreamSimpleGoogleGeminiCLI,
	}, "builtin")
	r.RegisterApiProvider(&ai.ApiProvider{
		Api:          "google-antigravity",
		Stream:       StreamGoogleGeminiCLI,
		StreamSimple: StreamSimpleGoogleGeminiCLI,
	}, "builtin")
}
