// Generic declarative adapter for the Cloud Code Assist Gemini wire family.
//
// This file replaces the previous google_gemini_cli.go.  It carries no
// provider-specific literals: per-provider behaviour is supplied via a
// DeclGoogleConfig record, constructed in register_*.go files alongside each
// provider's RegisterApiProvider call.
//
// The grand vision (Phase 2 + PoC payoff) replaces these in-Go Configs with
// JSON-encoded records shipped by ext-side `provider_register` payloads,
// interpreted by the substitution engine in pkg/ai/providers/declcfg/.  For
// Phase 1e we keep the Config struct in Go and the literals in Go init blocks
// — the goal here is shape, not yet wire format.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/kfet/ai/ratelimit"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/providers/declcfg"
	firlog "github.com/kfet/fir/pkg/log"
)

// --- Constants ---

const (
	declGoogleMaxRetries        = 3
	declGoogleBaseDelayMs       = 1000
	declGoogleMaxEmptyRetries   = 2
	declGoogleEmptyBaseDelayMs  = 500
	declGoogleDefaultMaxDelayMs = 60000
	claudeThinkingBetaHeader    = "interleaved-thinking-2025-05-14"
)

// --- DeclGoogleConfig ---

// DeclGoogleConfig parameterises the generic Cloud Code Assist Gemini adapter.
// One config per provider on the family.  All fields are data — no callbacks
// — so a future Phase 2 commit can ship the same record as JSON shipped by an
// extension, with no Go code changes required.
type DeclGoogleConfig struct {
	// Endpoints are tried in order on 403/404 cascade and on retry.
	// Values support ${var} substitution.
	Endpoints []string

	// Headers carries the per-request base header set (excluding
	// Authorization / Content-Type / Accept which the adapter always sets).
	// Values support ${var} and ${fn.x()} substitution — e.g.
	// "User-Agent": "myua/1.0 ${os}/${arch}".
	Headers map[string]string

	// ConditionalHeaders are header overlays applied when their When clause
	// matches the resolved model.  Values support substitution.
	ConditionalHeaders []ConditionalHeader

	// Envelope is the JSON template for the outer request body.  The literal
	// JSON string "$inner" is replaced by the inner Gemini request body.
	// String fields support ${var}/${fn.x()} substitution.  Nil envelope =
	// identity (the inner body is sent as-is).
	Envelope json.RawMessage

	// SystemInstructionPrefix is prepended to the systemInstruction.parts on
	// every request.
	SystemInstructionPrefix []googleSysInstrPart

	// SystemInstructionRole, if non-empty, overrides the default role on the
	// systemInstruction object.
	SystemInstructionRole string

	// ReasoningHeaderPrefix is the prefix the adapter looks for in
	// options.Headers when extracting thinking-config (e.g.
	// "x-gemini-thinking-").  Different per provider so that the agent
	// session's choice of header keys matches the adapter's expectations.
	ReasoningHeaderPrefix string

	// parsedEnvelope caches Envelope decoded to a Go value (any).
	parsedEnvelope     any
	parsedEnvelopeOnce sync.Once
	parsedEnvelopeErr  error
}

// ConditionalHeader applies Set when When matches the resolved model.
type ConditionalHeader struct {
	When ConditionalHeaderMatch
	Set  map[string]string
}

// ConditionalHeaderMatch describes when a ConditionalHeader fires.
// All non-zero fields must match.  Zero-value = unconstrained on that field.
type ConditionalHeaderMatch struct {
	// ModelIDPrefix matches model.ID against this prefix when non-empty.
	ModelIDPrefix string
	// RequiresReasoning, if true, requires model.Reasoning == true.
	RequiresReasoning bool
}

func (m ConditionalHeaderMatch) matches(model *ai.Model) bool {
	if m.ModelIDPrefix != "" && !strings.HasPrefix(model.ID, m.ModelIDPrefix) {
		return false
	}
	if m.RequiresReasoning && !model.Reasoning {
		return false
	}
	return true
}

func (cfg *DeclGoogleConfig) envelope() (any, error) {
	cfg.parsedEnvelopeOnce.Do(func() {
		if len(cfg.Envelope) == 0 {
			return
		}
		cfg.parsedEnvelopeErr = json.Unmarshal(cfg.Envelope, &cfg.parsedEnvelope)
	})
	return cfg.parsedEnvelope, cfg.parsedEnvelopeErr
}

// --- Registry from Api id → DeclGoogleConfig ---

var declGoogleConfigs sync.Map // string → *DeclGoogleConfig

// RegisterDeclGoogleConfig binds a config to an Api id.  Called from each
// provider's Register* function before building the ApiProvider.
func RegisterDeclGoogleConfig(api string, cfg *DeclGoogleConfig) {
	declGoogleConfigs.Store(api, cfg)
}

func getDeclGoogleConfig(api string) *DeclGoogleConfig {
	if v, ok := declGoogleConfigs.Load(api); ok {
		return v.(*DeclGoogleConfig)
	}
	return nil
}

// --- Shared types (formerly cloudCodeAssistRequest etc.) ---

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

type cloudCodeAssistChunk struct {
	Response *cloudCodeAssistResp `json:"response,omitempty"`
}

type cloudCodeAssistResp struct {
	Candidates    []cloudCodeAssistCandidate `json:"candidates,omitempty"`
	UsageMetadata *declGoogleUsageMetadata   `json:"usageMetadata,omitempty"`
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

type declGoogleUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

// toolCallCounter generates unique tool call IDs.
var toolCallCounter atomic.Int64

// --- Google thinking levels ---

type GoogleThinkingLevel string

const (
	ThinkingLevelUnspecified GoogleThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	ThinkingLevelMinimal     GoogleThinkingLevel = "MINIMAL"
	ThinkingLevelLow         GoogleThinkingLevel = "LOW"
	ThinkingLevelMedium      GoogleThinkingLevel = "MEDIUM"
	ThinkingLevelHigh        GoogleThinkingLevel = "HIGH"
)

// --- Retry helpers ---

func extractRetryDelay(errorText string, headers http.Header) int {
	normalizeDelay := func(ms int) int {
		if ms > 0 {
			return ms + 1000
		}
		return 0
	}
	if headers != nil {
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
		if rr := headers.Get("x-ratelimit-reset"); rr != "" {
			if sec, err := strconv.Atoi(rr); err == nil {
				if d := normalizeDelay(sec*1000 - int(time.Now().UnixMilli())); d > 0 {
					return d
				}
			}
		}
		if rra := headers.Get("x-ratelimit-reset-after"); rra != "" {
			if sec, err := strconv.ParseFloat(rra, 64); err == nil {
				if d := normalizeDelay(int(sec * 1000)); d > 0 {
					return d
				}
			}
		}
	}
	if d := ratelimit.ExtractRetryDelayFromText(errorText); d > 0 {
		return normalizeDelay(int(d.Milliseconds()))
	}
	return 0
}

func isGemini3ProModel(modelID string) bool {
	return gemini3ProRegex.MatchString(strings.ToLower(modelID))
}

func isGemini3FlashModel(modelID string) bool {
	return gemini3FlashRegex.MatchString(strings.ToLower(modelID))
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

// --- Substitution context ---

// buildSubstContext builds a declcfg.Context with model.*, creds.*, os, arch.
func buildSubstContext(model *ai.Model, creds map[string]string) *declcfg.Context {
	vars := map[string]any{
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"model.id":        model.ID,
		"model.api":       model.Api,
		"model.provider":  model.Provider,
		"model.base_url":  model.BaseURL,
		"model.reasoning": model.Reasoning,
	}
	for k, v := range creds {
		vars["creds."+k] = v
	}
	return &declcfg.Context{Vars: vars}
}

// parseGoogleCreds decodes the credential string carried in
// StreamOptions.ApiKey.  If it parses as a JSON object, top-level keys are
// snake-cased and stored as creds.<key>; the alias creds.access_token is
// filled from creds.token when missing.  Otherwise the raw string populates
// both creds.api_key and creds.access_token.
func parseGoogleCreds(raw string) map[string]string {
	out := map[string]string{}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil && obj != nil {
		for k, v := range obj {
			sk := snakeCase(k)
			switch x := v.(type) {
			case string:
				out[sk] = x
			case nil:
				out[sk] = ""
			default:
				out[sk] = fmt.Sprint(x)
			}
		}
		if _, ok := out["access_token"]; !ok {
			if t, ok := out["token"]; ok {
				out["access_token"] = t
			}
		}
		return out
	}
	out["api_key"] = raw
	out["access_token"] = raw
	return out
}

// snakeCase converts camelCase / PascalCase to snake_case.  ASCII-only.
func snakeCase(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- Build request ---

func buildDeclGoogleInner(
	model *ai.Model,
	ctx ai.Context,
	options *ai.StreamOptions,
	cfg *DeclGoogleConfig,
) *cloudCodeAssistInnerReq {
	contents := ConvertGoogleMessages(model, ctx)

	thinkingPrefix := cfg.ReasoningHeaderPrefix
	if thinkingPrefix == "" {
		thinkingPrefix = "x-gemini-thinking-"
	}

	genConfig := map[string]any{}
	if options != nil && options.Temperature != nil {
		genConfig["temperature"] = *options.Temperature
	}
	if options != nil && options.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *options.MaxTokens
	}
	if options != nil && options.Headers != nil {
		hdr := func(suffix string) string { return options.Headers[thinkingPrefix+suffix] }
		if hdr("disabled") == "true" && model.Reasoning {
			genConfig["thinkingConfig"] = getDeclGoogleDisabledThinkingConfig(model.ID)
		} else if hdr("enabled") == "true" && model.Reasoning {
			thinkingConfig := map[string]any{"includeThoughts": true}
			if level := hdr("level"); level != "" {
				thinkingConfig["thinkingLevel"] = level
			} else if budget := hdr("budget"); budget != "" {
				if b, err := strconv.Atoi(budget); err == nil && b > 0 {
					thinkingConfig["thinkingBudget"] = b
				}
			}
			genConfig["thinkingConfig"] = thinkingConfig
		}
	}

	req := &cloudCodeAssistInnerReq{Contents: contents}
	if options != nil && options.SessionID != "" {
		req.SessionID = options.SessionID
	}
	if ctx.SystemPrompt != "" {
		req.SystemInstruction = &googleSysInstr{
			Parts: []googleSysInstrPart{{Text: SanitizeSurrogates(ctx.SystemPrompt)}},
		}
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

	if len(cfg.SystemInstructionPrefix) > 0 {
		existingParts := []googleSysInstrPart{}
		if req.SystemInstruction != nil {
			existingParts = req.SystemInstruction.Parts
		}
		role := cfg.SystemInstructionRole
		if role == "" && req.SystemInstruction != nil {
			role = req.SystemInstruction.Role
		}
		req.SystemInstruction = &googleSysInstr{
			Role:  role,
			Parts: append(append([]googleSysInstrPart{}, cfg.SystemInstructionPrefix...), existingParts...),
		}
	}
	return req
}

// buildDeclGoogleBody returns the final outer JSON body to send.  When the
// config carries an Envelope template it is substituted with the inner request
// at "$inner"; otherwise the inner request is returned directly.
func buildDeclGoogleBody(
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
	cfg *DeclGoogleConfig,
	creds map[string]string,
) (any, error) {
	inner := buildDeclGoogleInner(model, prompt, options, cfg)
	envelopeTemplate, err := cfg.envelope()
	if err != nil {
		return nil, fmt.Errorf("invalid envelope template: %w", err)
	}
	if envelopeTemplate == nil {
		return inner, nil
	}
	// Decode inner to any so SubstituteJSON's "$inner" sentinel produces a
	// JSON value (object) rather than a stringified blob.
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("marshal inner: %w", err)
	}
	var innerAny any
	if err := json.Unmarshal(innerJSON, &innerAny); err != nil {
		return nil, fmt.Errorf("re-decode inner: %w", err)
	}
	subCtx := buildSubstContext(model, creds)
	out, err := declcfg.SubstituteJSON(envelopeTemplate, subCtx, innerAny)
	if err != nil {
		return nil, fmt.Errorf("substitute envelope: %w", err)
	}
	return out, nil
}

// resolveDeclGoogleHeaders renders cfg.Headers + matching ConditionalHeaders
// through declcfg.Substitute.
func resolveDeclGoogleHeaders(
	cfg *DeclGoogleConfig,
	model *ai.Model,
	creds map[string]string,
) (map[string]string, error) {
	subCtx := buildSubstContext(model, creds)
	out := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		s, err := declcfg.Substitute(v, subCtx)
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", k, err)
		}
		out[k] = s
	}
	for _, ch := range cfg.ConditionalHeaders {
		if !ch.When.matches(model) {
			continue
		}
		for k, v := range ch.Set {
			s, err := declcfg.Substitute(v, subCtx)
			if err != nil {
				return nil, fmt.Errorf("conditional header %q: %w", k, err)
			}
			out[k] = s
		}
	}
	return out, nil
}

// resolveDeclGoogleEndpoints renders cfg.Endpoints through declcfg.Substitute.
func resolveDeclGoogleEndpoints(
	cfg *DeclGoogleConfig,
	model *ai.Model,
	creds map[string]string,
) ([]string, error) {
	subCtx := buildSubstContext(model, creds)
	out := make([]string, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		s, err := declcfg.Substitute(ep, subCtx)
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", ep, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// --- Stream entry points ---

// StreamDeclGoogle is the generic streamer for any Cloud Code Assist Gemini
// provider.  Resolves the registered config by model.Api.
func StreamDeclGoogle(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
) *ai.AssistantMessageEventStream {
	cfg := getDeclGoogleConfig(model.Api)
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
		if cfg == nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = fmt.Sprintf("no DeclGoogleConfig registered for api %q", model.Api)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: output.StopReason, Error: output})
			stream.End(nil)
			return
		}
		firlog.Debug("declgoogle request", "api", model.Api, "model", model.ID, "messageCount", len(prompt.Messages))
		err := streamDeclGoogle(ctx, model, prompt, options, cfg, output, stream)
		if err != nil {
			if ctx.Err() != nil {
				output.StopReason = ai.StopReasonAborted
			} else {
				output.StopReason = ai.StopReasonError
			}
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: output.StopReason, Error: output})
			stream.End(nil)
			return
		}
		firlog.Debug("declgoogle response complete", "api", model.Api, "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: output.StopReason, Message: output})
		stream.End(nil)
	}()
	return stream
}

func streamDeclGoogle(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
	cfg *DeclGoogleConfig,
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
	creds := parseGoogleCreds(apiKeyRaw)
	if creds["access_token"] == "" || creds["project_id"] == "" {
		return fmt.Errorf("invalid Google Cloud Code Assist credentials. Use /login to re-authenticate")
	}

	baseURL := strings.TrimSpace(model.BaseURL)
	var endpoints []string
	if baseURL != "" {
		endpoints = []string{baseURL}
	} else {
		eps, err := resolveDeclGoogleEndpoints(cfg, model, creds)
		if err != nil {
			return err
		}
		endpoints = eps
	}
	if len(endpoints) == 0 {
		return fmt.Errorf("no endpoints configured for api %q", model.Api)
	}

	reqBody, err := buildDeclGoogleBody(model, prompt, options, cfg, creds)
	if err != nil {
		return err
	}
	var reqBodyAny any = reqBody
	if options != nil && options.OnPayload != nil {
		if next := options.OnPayload(reqBody, model); next != nil {
			reqBodyAny = next
		}
	}
	bodyJSON, err := json.Marshal(reqBodyAny)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}
	traceWireMessages("declgoogle", bodyJSON)

	baseHdrs, err := resolveDeclGoogleHeaders(cfg, model, creds)
	if err != nil {
		return err
	}
	baseHdrs["Authorization"] = "Bearer " + creds["access_token"]
	baseHdrs["Content-Type"] = "application/json"
	baseHdrs["Accept"] = "text/event-stream"
	hdrs := BuildRequestHeaders(baseHdrs, model, options)

	var resp *http.Response
	var lastErr error
	var requestURL string
	endpointIndex := 0

	for attempt := 0; attempt <= declGoogleMaxRetries; attempt++ {
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
			if attempt < declGoogleMaxRetries {
				delay := declGoogleBaseDelayMs * int(math.Pow(2, float64(attempt)))
				if options != nil && options.OnRetry != nil {
					options.OnRetry(attempt+1, float64(delay)/1000.0, lastErr.Error())
				}
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
		if (resp.StatusCode == 403 || resp.StatusCode == 404) && endpointIndex < len(endpoints)-1 {
			endpointIndex++
			continue
		}
		if attempt < declGoogleMaxRetries && isRetryableError(resp.StatusCode, errorText) {
			if endpointIndex < len(endpoints)-1 {
				endpointIndex++
			}
			serverDelay := extractRetryDelay(errorText, resp.Header)
			delayMs := serverDelay
			if delayMs == 0 {
				delayMs = declGoogleBaseDelayMs * int(math.Pow(2, float64(attempt)))
			}
			maxDelayMs := declGoogleDefaultMaxDelayMs
			if options != nil && options.MaxRetryDelayMs != nil {
				maxDelayMs = *options.MaxRetryDelayMs
			}
			if maxDelayMs > 0 && serverDelay > 0 && serverDelay > maxDelayMs {
				return fmt.Errorf("server requested %ds retry delay (max: %ds). %s",
					serverDelay/1000, maxDelayMs/1000, extractErrorMessage(errorText))
			}

			if options != nil && options.OnRetry != nil {
				options.OnRetry(attempt+1, float64(delayMs)/1000.0,
					fmt.Sprintf("%d: %s", resp.StatusCode, extractErrorMessage(errorText)))
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

	for emptyAttempt := 0; emptyAttempt <= declGoogleMaxEmptyRetries; emptyAttempt++ {
		if ctx.Err() != nil {
			return fmt.Errorf("request was aborted")
		}
		if emptyAttempt > 0 {
			delay := declGoogleEmptyBaseDelayMs * int(math.Pow(2, float64(emptyAttempt-1)))
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
			output.Content = nil
			output.Usage = ai.Usage{}
			output.StopReason = ai.StopReasonStop
			output.ErrorMessage = ""
			output.Timestamp = time.Now().UnixMilli()
		}
		hasContent, err := parseDeclGoogleStream(ctx, resp.Body, model, output, stream)
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

func parseDeclGoogleStream(
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
	var currentBlockType string

	ensureStarted := func() {
		if !started {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})
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
						newType := "text"
						if isThinking {
							newType = "thinking"
						}
						if currentBlockType != newType {
							if currentBlockType != "" {
								idx := len(output.Content) - 1
								if currentBlockType == "text" {
									stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextEnd, ContentIndex: idx, Content: output.Content[idx].Text.Text, Partial: output})
								} else {
									stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingEnd, ContentIndex: idx, Content: output.Content[idx].Thinking.Thinking, Partial: output})
								}
							}
							idx := len(output.Content)
							if isThinking {
								output.Content = append(output.Content, ai.NewThinkingContent(""))
								ensureStarted()
								stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingStart, ContentIndex: idx, Partial: output})
							} else {
								output.Content = append(output.Content, ai.NewTextContent(""))
								ensureStarted()
								stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextStart, ContentIndex: idx, Partial: output})
							}
							currentBlockType = newType
						}
						idx := len(output.Content) - 1
						if isThinking {
							c := output.Content[idx]
							c.Thinking.Thinking += part.Text
							if part.ThoughtSignature != "" {
								c.Thinking.ThinkingSignature = RetainThoughtSignature(c.Thinking.ThinkingSignature, part.ThoughtSignature)
							}
							output.Content[idx] = c
							stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingDelta, ContentIndex: idx, Delta: part.Text, Partial: output})
						} else {
							c := output.Content[idx]
							c.Text.Text += part.Text
							if part.ThoughtSignature != "" {
								c.Text.TextSignature = RetainThoughtSignature(c.Text.TextSignature, part.ThoughtSignature)
							}
							output.Content[idx] = c
							stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, ContentIndex: idx, Delta: part.Text, Partial: output})
						}
					}
					if part.FunctionCall != nil {
						hasContent = true
						if currentBlockType != "" {
							idx := len(output.Content) - 1
							if currentBlockType == "text" {
								stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextEnd, ContentIndex: idx, Content: output.Content[idx].Text.Text, Partial: output})
							} else {
								stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingEnd, ContentIndex: idx, Content: output.Content[idx].Thinking.Thinking, Partial: output})
							}
							currentBlockType = ""
						}
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
							toolCallID = fmt.Sprintf("%s_%d_%d", part.FunctionCall.Name, time.Now().UnixMilli(), toolCallCounter.Add(1))
						}
						tc := ai.NewToolCallContent(toolCallID, part.FunctionCall.Name, part.FunctionCall.Args)
						if part.ThoughtSignature != "" {
							tc.ToolCall.ThoughtSignature = part.ThoughtSignature
						}
						output.Content = append(output.Content, tc)
						idx := len(output.Content) - 1
						ensureStarted()
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallStart, ContentIndex: idx, Partial: output})
						argsJSON, _ := json.Marshal(tc.ToolCall.Arguments)
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallDelta, ContentIndex: idx, Delta: string(argsJSON), Partial: output})
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventToolcallEnd, ContentIndex: idx, ToolCall: tc.ToolCall, Partial: output})
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

	if currentBlockType != "" {
		idx := len(output.Content) - 1
		if currentBlockType == "text" {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventTextEnd, ContentIndex: idx, Content: output.Content[idx].Text.Text, Partial: output})
		} else {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventThinkingEnd, ContentIndex: idx, Content: output.Content[idx].Thinking.Thinking, Partial: output})
		}
	}
	return hasContent, nil
}

// --- StreamSimple ---

// StreamSimpleDeclGoogle adapts SimpleStreamOptions for the generic adapter.
// Uses the registered config's ReasoningHeaderPrefix to pick header keys.
func StreamSimpleDeclGoogle(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.SimpleStreamOptions,
) *ai.AssistantMessageEventStream {
	cfg := getDeclGoogleConfig(model.Api)
	prefix := ""
	if cfg != nil {
		prefix = cfg.ReasoningHeaderPrefix
	}
	if prefix == "" {
		prefix = "x-gemini-thinking-"
	}

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
		base.Headers = mergeHeaders(base.Headers, map[string]string{prefix + "enabled": "false"})
		return StreamDeclGoogle(ctx, model, prompt, base)
	}
	if options.Reasoning == ai.ThinkingOff && model.Reasoning {
		base.Headers = mergeHeaders(base.Headers, map[string]string{prefix + "disabled": "true"})
		return StreamDeclGoogle(ctx, model, prompt, base)
	}
	effort := ClampReasoning(options.Reasoning)
	if isGemini3ModelID(model.ID) {
		level := getDeclGoogleThinkingLevel(effort, model.ID)
		base.Headers = mergeHeaders(base.Headers, map[string]string{
			prefix + "enabled": "true",
			prefix + "level":   string(level),
		})
		return StreamDeclGoogle(ctx, model, prompt, base)
	}
	maxTokens, thinkingBudget := AdjustMaxTokensForThinking(
		derefInt(base.MaxTokens, 0), model.MaxTokens, effort, options.ThinkingBudgets)
	base.MaxTokens = &maxTokens
	base.Headers = mergeHeaders(base.Headers, map[string]string{
		prefix + "enabled": "true",
		prefix + "budget":  strconv.Itoa(thinkingBudget),
	})
	return StreamDeclGoogle(ctx, model, prompt, base)
}

func getDeclGoogleThinkingLevel(effort ai.ThinkingLevel, modelID string) GoogleThinkingLevel {
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

func getDeclGoogleDisabledThinkingConfig(modelID string) map[string]any {
	if isGemini3ProModel(modelID) {
		return map[string]any{"thinkingLevel": "LOW"}
	}
	if isGemini3FlashModel(modelID) {
		return map[string]any{"thinkingLevel": "MINIMAL"}
	}
	return map[string]any{"thinkingBudget": 0}
}
