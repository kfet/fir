// Ported from: packages/ai/scripts/generate-models.ts
// Upstream hash: 3a3e37d3
//
// Command generate-models fetches model data from external APIs and generates
// pkg/ai/models_generated.go. It is a Go port of scripts/generate-models.ts.
//
// When generate-models.ts changes upstream:
//  1. Apply equivalent logic changes to this file (cmd/generate-models/main.go)
//  2. Run: make generate-models
//  3. Verify: go build ./... && go test ./pkg/ai/...
//  4. Update the upstream hash above and in sync/.baseline-hashes
//
// Usage:
//
//	go run ./cmd/generate-models/ [-out pkg/ai/models_generated.go]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// modelSpec is the internal representation of a model used during generation.
type modelSpec struct {
	ID            string
	Name          string
	API           string
	Provider      string
	BaseURL       string
	Reasoning     bool
	Input         []string // "text", "image"
	CostInput     float64
	CostOutput    float64
	CostCacheRead float64
	CostCacheWrite float64
	ContextWindow int
	MaxTokens     int
	Headers       map[string]string
	Compat        *compatSpec
}

// compatSpec represents OpenAICompletionsCompat fields used in models.
type compatSpec struct {
	SupportsStore           *bool
	SupportsDeveloperRole   *bool
	SupportsReasoningEffort *bool
	ThinkingFormat          string
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// --- models.dev types ---

type modelsDevModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ToolCall bool   `json:"tool_call"`
	Reasoning bool  `json:"reasoning"`
	Status   string `json:"status"`
	Limit    struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Provider struct {
		NPM string `json:"npm"`
	} `json:"provider"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevData map[string]modelsDevProvider

// --- OpenRouter types ---

type openRouterModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Architecture struct {
		Modality string `json:"modality"`
	} `json:"architecture"`
	Pricing struct {
		Prompt         string `json:"prompt"`
		Completion     string `json:"completion"`
		InputCacheRead string `json:"input_cache_read"`
		InputCacheWrite string `json:"input_cache_write"`
	} `json:"pricing"`
	ContextLength int `json:"context_length"`
	TopProvider struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
}

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// --- Vercel AI Gateway types ---

type aiGatewayModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextWindow int      `json:"context_window"`
	MaxTokens     int      `json:"max_tokens"`
	Tags          []string `json:"tags"`
	Pricing       struct {
		Input          any `json:"input"`
		Output         any `json:"output"`
		InputCacheRead  any `json:"input_cache_read"`
		InputCacheWrite any `json:"input_cache_write"`
	} `json:"pricing"`
}

type aiGatewayResponse struct {
	Data []aiGatewayModel `json:"data"`
}

// --- HTTP helper ---

func fetchJSON(url string, target any) error {
	resp, err := http.Get(url) //nolint:gosec // urls are constants
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("unmarshal %s: %w", url, err)
	}
	return nil
}

// toFloat converts a JSON number/string to float64.
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(x, "%f", &f)
		return f
	}
	return 0
}

// parseFloat parses a string as float64; returns 0 on error.
func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func hasString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// --- Fetchers ---

const (
	aiGatewayModelsURL = "https://ai-gateway.vercel.sh/v1"
	aiGatewayBaseURL   = "https://ai-gateway.vercel.sh"
)

var copilotStaticHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

func fetchOpenRouterModels() ([]modelSpec, error) {
	log.Println("Fetching models from OpenRouter API...")
	var resp openRouterResponse
	if err := fetchJSON("https://openrouter.ai/api/v1/models", &resp); err != nil {
		return nil, err
	}

	var models []modelSpec
	for _, m := range resp.Data {
		if !hasString(m.SupportedParameters, "tools") {
			continue
		}

		input := []string{"text"}
		if strings.Contains(m.Architecture.Modality, "image") {
			input = append(input, "image")
		}

		// Convert pricing from $/token to $/million tokens
		models = append(models, modelSpec{
			ID:             m.ID,
			Name:           m.Name,
			API:            "openai-completions",
			BaseURL:        "https://openrouter.ai/api/v1",
			Provider:       "openrouter",
			Reasoning:      hasString(m.SupportedParameters, "reasoning"),
			Input:          input,
			CostInput:      parseFloat(m.Pricing.Prompt) * 1_000_000,
			CostOutput:     parseFloat(m.Pricing.Completion) * 1_000_000,
			CostCacheRead:  parseFloat(m.Pricing.InputCacheRead) * 1_000_000,
			CostCacheWrite: parseFloat(m.Pricing.InputCacheWrite) * 1_000_000,
			ContextWindow:  intOr(m.ContextLength, 4096),
			MaxTokens:      intOr(m.TopProvider.MaxCompletionTokens, 4096),
		})
	}
	log.Printf("Fetched %d tool-capable models from OpenRouter", len(models))
	return models, nil
}

func fetchAIGatewayModels() ([]modelSpec, error) {
	log.Println("Fetching models from Vercel AI Gateway API...")
	var resp aiGatewayResponse
	if err := fetchJSON(aiGatewayModelsURL+"/models", &resp); err != nil {
		return nil, err
	}

	var models []modelSpec
	for _, m := range resp.Data {
		if !hasString(m.Tags, "tool-use") {
			continue
		}

		input := []string{"text"}
		if hasString(m.Tags, "vision") {
			input = append(input, "image")
		}

		models = append(models, modelSpec{
			ID:             m.ID,
			Name:           stringOr(m.Name, m.ID),
			API:            "anthropic-messages",
			BaseURL:        aiGatewayBaseURL,
			Provider:       "vercel-ai-gateway",
			Reasoning:      hasString(m.Tags, "reasoning"),
			Input:          input,
			CostInput:      toFloat(m.Pricing.Input) * 1_000_000,
			CostOutput:     toFloat(m.Pricing.Output) * 1_000_000,
			CostCacheRead:  toFloat(m.Pricing.InputCacheRead) * 1_000_000,
			CostCacheWrite: toFloat(m.Pricing.InputCacheWrite) * 1_000_000,
			ContextWindow:  intOr(m.ContextWindow, 4096),
			MaxTokens:      intOr(m.MaxTokens, 4096),
		})
	}
	log.Printf("Fetched %d tool-capable models from Vercel AI Gateway", len(models))
	return models, nil
}

var copilotClaude4Re = regexp.MustCompile(`^claude-(haiku|sonnet|opus)-4([.\-]|$)`)

func loadModelsDevData() ([]modelSpec, error) {
	log.Println("Fetching models from models.dev API...")
	var data modelsDevData
	if err := fetchJSON("https://models.dev/api.json", &data); err != nil {
		return nil, err
	}

	var models []modelSpec

	// --- Amazon Bedrock ---
	if p, ok := data["amazon-bedrock"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if strings.HasPrefix(id, "ai21.jamba") {
				continue // no streaming tool support
			}
			if strings.HasPrefix(id, "mistral.mistral-7b-instruct-v0") {
				continue // no system message support
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "bedrock-converse-stream",
				Provider:       "amazon-bedrock",
				BaseURL:        "https://bedrock-runtime.us-east-1.amazonaws.com",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Anthropic ---
	if p, ok := data["anthropic"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "anthropic-messages",
				Provider:       "anthropic",
				BaseURL:        "https://api.anthropic.com",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Google ---
	if p, ok := data["google"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "google-generative-ai",
				Provider:       "google",
				BaseURL:        "https://generativelanguage.googleapis.com/v1beta",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- OpenAI ---
	if p, ok := data["openai"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-responses",
				Provider:       "openai",
				BaseURL:        "https://api.openai.com/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Groq ---
	if p, ok := data["groq"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "groq",
				BaseURL:        "https://api.groq.com/openai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Cerebras ---
	if p, ok := data["cerebras"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "cerebras",
				BaseURL:        "https://api.cerebras.ai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- xAI ---
	if p, ok := data["xai"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "xai",
				BaseURL:        "https://api.x.ai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- ZAI ---
	if p, ok := data["zai"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "zai",
				BaseURL:        "https://api.z.ai/api/coding/paas/v4",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
				Compat: &compatSpec{
					SupportsDeveloperRole: boolPtr(false),
					ThinkingFormat:        "zai",
				},
			})
		}
	}

	// --- Mistral ---
	if p, ok := data["mistral"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "mistral",
				BaseURL:        "https://api.mistral.ai/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- Hugging Face ---
	if p, ok := data["huggingface"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "openai-completions",
				Provider:       "huggingface",
				BaseURL:        "https://router.huggingface.co/v1",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
				Compat: &compatSpec{
					SupportsDeveloperRole: boolPtr(false),
				},
			})
		}
	}

	// --- OpenCode Zen ---
	if p, ok := data["opencode"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if m.Status == "deprecated" {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			var api, baseURL string
			switch m.Provider.NPM {
			case "@ai-sdk/openai":
				api = "openai-responses"
				baseURL = "https://opencode.ai/zen/v1"
			case "@ai-sdk/anthropic":
				api = "anthropic-messages"
				baseURL = "https://opencode.ai/zen"
			case "@ai-sdk/google":
				api = "google-generative-ai"
				baseURL = "https://opencode.ai/zen/v1"
			default:
				api = "openai-completions"
				baseURL = "https://opencode.ai/zen/v1"
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            api,
				Provider:       "opencode",
				BaseURL:        baseURL,
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	// --- GitHub Copilot ---
	if p, ok := data["github-copilot"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			if m.Status == "deprecated" {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}

			isCopilotClaude4 := copilotClaude4Re.MatchString(id)
			needsResponsesAPI := strings.HasPrefix(id, "gpt-5") || strings.HasPrefix(id, "oswe")

			var api string
			var compat *compatSpec
			switch {
			case isCopilotClaude4:
				api = "anthropic-messages"
			case needsResponsesAPI:
				api = "openai-responses"
			default:
				api = "openai-completions"
				compat = &compatSpec{
					SupportsStore:           boolPtr(false),
					SupportsDeveloperRole:   boolPtr(false),
					SupportsReasoningEffort: boolPtr(false),
				}
			}

			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            api,
				Provider:       "github-copilot",
				BaseURL:        "https://api.individual.githubcopilot.com",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 128000),
				MaxTokens:      intOr(m.Limit.Output, 8192),
				Headers:        copilotStaticHeaders,
				Compat:         compat,
			})
		}
	}

	// --- MiniMax variants ---
	minimaxVariants := []struct {
		key      string
		provider string
		baseURL  string
	}{
		{"minimax", "minimax", "https://api.minimax.io/anthropic"},
		{"minimax-cn", "minimax-cn", "https://api.minimaxi.com/anthropic"},
	}
	for _, variant := range minimaxVariants {
		if p, ok := data[variant.key]; ok {
			for id, m := range p.Models {
				if !m.ToolCall {
					continue
				}
				input := []string{"text"}
				if hasString(m.Modalities.Input, "image") {
					input = append(input, "image")
				}
				models = append(models, modelSpec{
					ID:             id,
					Name:           stringOr(m.Name, id),
					API:            "anthropic-messages",
					Provider:       variant.provider,
					BaseURL:        variant.baseURL,
					Reasoning:      m.Reasoning,
					Input:          input,
					CostInput:      m.Cost.Input,
					CostOutput:     m.Cost.Output,
					CostCacheRead:  m.Cost.CacheRead,
					CostCacheWrite: m.Cost.CacheWrite,
					ContextWindow:  intOr(m.Limit.Context, 4096),
					MaxTokens:      intOr(m.Limit.Output, 4096),
				})
			}
		}
	}

	// --- Kimi For Coding ---
	if p, ok := data["kimi-for-coding"]; ok {
		for id, m := range p.Models {
			if !m.ToolCall {
				continue
			}
			input := []string{"text"}
			if hasString(m.Modalities.Input, "image") {
				input = append(input, "image")
			}
			models = append(models, modelSpec{
				ID:             id,
				Name:           stringOr(m.Name, id),
				API:            "anthropic-messages",
				Provider:       "kimi-coding",
				BaseURL:        "https://api.kimi.com/coding",
				Reasoning:      m.Reasoning,
				Input:          input,
				CostInput:      m.Cost.Input,
				CostOutput:     m.Cost.Output,
				CostCacheRead:  m.Cost.CacheRead,
				CostCacheWrite: m.Cost.CacheWrite,
				ContextWindow:  intOr(m.Limit.Context, 4096),
				MaxTokens:      intOr(m.Limit.Output, 4096),
			})
		}
	}

	log.Printf("Loaded %d tool-capable models from models.dev", len(models))
	return models, nil
}

// intOr returns n if n > 0, otherwise def.
func intOr(n, def int) int {
	if n > 0 {
		return n
	}
	return def
}

// stringOr returns s if non-empty, otherwise def.
func stringOr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// hasModel returns true if any model in all with the given provider and id exists.
func hasModel(all []modelSpec, provider, id string) bool {
	for _, m := range all {
		if m.Provider == provider && m.ID == id {
			return true
		}
	}
	return false
}

// applyOverridesAndAdditions applies all the manual fixups from the TS script.
func applyOverridesAndAdditions(all []modelSpec) []modelSpec {
	// Fix incorrect cache pricing for Claude Opus 4.5 from models.dev
	for i := range all {
		if all[i].Provider == "anthropic" && all[i].ID == "claude-opus-4-5" {
			all[i].CostCacheRead = 0.5
			all[i].CostCacheWrite = 6.25
		}
	}

	// Temporary overrides for Amazon Bedrock Opus 4.6 metadata
	for i := range all {
		m := &all[i]
		if m.Provider == "amazon-bedrock" && strings.Contains(m.ID, "anthropic.claude-opus-4-6-v1") {
			m.CostCacheRead = 0.5
			m.CostCacheWrite = 6.25
			m.ContextWindow = 200000
		}
		if (m.Provider == "anthropic" || m.Provider == "opencode") && m.ID == "claude-opus-4-6" {
			m.ContextWindow = 200000
		}
		// opencode lists Claude Sonnet 4/4.5 with 1M context, actual limit is 200K
		if m.Provider == "opencode" && (m.ID == "claude-sonnet-4-5" || m.ID == "claude-sonnet-4") {
			m.ContextWindow = 200000
		}
	}

	// Add missing EU Opus 4.6 profile
	if !hasModel(all, "amazon-bedrock", "eu.anthropic.claude-opus-4-6-v1") {
		all = append(all, modelSpec{
			ID:             "eu.anthropic.claude-opus-4-6-v1",
			Name:           "Claude Opus 4.6 (EU)",
			API:            "bedrock-converse-stream",
			Provider:       "amazon-bedrock",
			BaseURL:        "https://bedrock-runtime.us-east-1.amazonaws.com",
			Reasoning:      true,
			Input:          []string{"text", "image"},
			CostInput:      5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 200000, MaxTokens: 128000,
		})
	}

	// Add missing Claude Opus 4.6
	if !hasModel(all, "anthropic", "claude-opus-4-6") {
		all = append(all, modelSpec{
			ID:             "claude-opus-4-6",
			Name:           "Claude Opus 4.6",
			API:            "anthropic-messages",
			Provider:       "anthropic",
			BaseURL:        "https://api.anthropic.com",
			Reasoning:      true,
			Input:          []string{"text", "image"},
			CostInput:      5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 200000, MaxTokens: 128000,
		})
	}

	// Add missing Claude Sonnet 4.6
	if !hasModel(all, "anthropic", "claude-sonnet-4-6") {
		all = append(all, modelSpec{
			ID:             "claude-sonnet-4-6",
			Name:           "Claude Sonnet 4.6",
			API:            "anthropic-messages",
			Provider:       "anthropic",
			BaseURL:        "https://api.anthropic.com",
			Reasoning:      true,
			Input:          []string{"text", "image"},
			CostInput:      3, CostOutput: 15, CostCacheRead: 0.3, CostCacheWrite: 3.75,
			ContextWindow: 200000, MaxTokens: 64000,
		})
	}

	// Add missing GPT models
	if !hasModel(all, "openai", "gpt-5-chat-latest") {
		all = append(all, modelSpec{
			ID: "gpt-5-chat-latest", Name: "GPT-5 Chat Latest",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: false, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: 128000, MaxTokens: 16384,
		})
	}
	if !hasModel(all, "openai", "gpt-5.1-codex") {
		all = append(all, modelSpec{
			ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 5, CostCacheRead: 0.125, CostCacheWrite: 1.25,
			ContextWindow: 400000, MaxTokens: 128000,
		})
	}
	if !hasModel(all, "openai", "gpt-5.1-codex-max") {
		all = append(all, modelSpec{
			ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: 400000, MaxTokens: 128000,
		})
	}
	if !hasModel(all, "openai", "gpt-5.3-codex-spark") {
		all = append(all, modelSpec{
			ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark",
			API: "openai-responses", Provider: "openai", BaseURL: "https://api.openai.com/v1",
			Reasoning: true, Input: []string{"text"},
			CostInput: 0, CostOutput: 0, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 128000, MaxTokens: 16384,
		})
	}

	// OpenAI Codex (ChatGPT OAuth) models
	const codexBaseURL = "https://chatgpt.com/backend-api"
	const codexContext = 272000
	const codexMaxTokens = 128000
	codexModels := []modelSpec{
		{ID: "gpt-5.1", Name: "GPT-5.1", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1 Codex Mini", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 0.25, CostOutput: 2, CostCacheRead: 0.025, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.2", Name: "GPT-5.2", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.75, CostOutput: 14, CostCacheRead: 0.175, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.75, CostOutput: 14, CostCacheRead: 0.175, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text", "image"},
			CostInput: 1.75, CostOutput: 14, CostCacheRead: 0.175, CostCacheWrite: 0,
			ContextWindow: codexContext, MaxTokens: codexMaxTokens},
		{ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark", API: "openai-codex-responses", Provider: "openai-codex",
			BaseURL: codexBaseURL, Reasoning: true, Input: []string{"text"},
			CostInput: 0, CostOutput: 0, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 128000, MaxTokens: codexMaxTokens},
	}
	all = append(all, codexModels...)

	// Add missing Grok model
	if !hasModel(all, "xai", "grok-code-fast-1") {
		all = append(all, modelSpec{
			ID: "grok-code-fast-1", Name: "Grok Code Fast 1",
			API: "openai-completions", Provider: "xai", BaseURL: "https://api.x.ai/v1",
			Reasoning: false, Input: []string{"text"},
			CostInput: 0.2, CostOutput: 1.5, CostCacheRead: 0.02, CostCacheWrite: 0,
			ContextWindow: 32768, MaxTokens: 8192,
		})
	}

	// Add "auto" alias for openrouter/auto
	if !hasModel(all, "openrouter", "auto") {
		all = append(all, modelSpec{
			ID: "auto", Name: "Auto",
			API: "openai-completions", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
			Reasoning: true, Input: []string{"text", "image"},
			CostInput: 0, CostOutput: 0, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 2000000, MaxTokens: 30000,
		})
	}

	// Gemini 3.1 Pro — announced 2026-02-19, still in preview on all Google endpoints.
	// https://blog.google/innovation-and-ai/models-and-research/gemini-models/gemini-3-1-pro/
	// The model ID is gemini-3.1-pro-preview. The google and google-vertex providers are
	// already supplied by models.dev; we only need to add Cloud Code Assist and Antigravity.

	// Google Cloud Code Assist models (Gemini CLI)
	const cloudCodeAssistEndpoint = "https://cloudcode-pa.googleapis.com"
	cloudCodeAssistModels := []modelSpec{
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro (Cloud Code Assist)", API: "google-gemini-cli",
			Provider: "google-gemini-cli", BaseURL: cloudCodeAssistEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash (Cloud Code Assist)", API: "google-gemini-cli",
			Provider: "google-gemini-cli", BaseURL: cloudCodeAssistEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash (Cloud Code Assist)", API: "google-gemini-cli",
			Provider: "google-gemini-cli", BaseURL: cloudCodeAssistEndpoint, Reasoning: false,
			Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 8192},
		{ID: "gemini-3-pro-preview", Name: "Gemini 3 Pro Preview (Cloud Code Assist)", API: "google-gemini-cli",
			Provider: "google-gemini-cli", BaseURL: cloudCodeAssistEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-3-flash-preview", Name: "Gemini 3 Flash Preview (Cloud Code Assist)", API: "google-gemini-cli",
			Provider: "google-gemini-cli", BaseURL: cloudCodeAssistEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 65535},
		// Gemini 3.1 Pro Preview is deployed on Cloud Code Assist but gated behind the
		// GEMINI_3_1_PRO_LAUNCHED experiment flag (45760185). Users will see a 404 until
		// the flag rolls out to their account.
		{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview (Cloud Code Assist)", API: "google-gemini-cli",
			Provider: "google-gemini-cli", BaseURL: cloudCodeAssistEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 65535},
	}
	all = append(all, cloudCodeAssistModels...)

	// Gemini 3.1 Pro Preview Custom Tools — variant of the google provider model that
	// enables custom tool definitions (not returned by models.dev).
	if !hasModel(all, "google", "gemini-3.1-pro-preview-customtools") {
		all = append(all, modelSpec{
			ID:            "gemini-3.1-pro-preview-customtools",
			Name:          "Gemini 3.1 Pro Preview Custom Tools",
			API:           "google-generative-ai",
			Provider:      "google",
			BaseURL:       "https://generativelanguage.googleapis.com/v1beta",
			Reasoning:     true,
			Input:         []string{"text", "image"},
			CostInput:     2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536,
		})
	}

	// Antigravity models
	const antigravityEndpoint = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityModels := []modelSpec{
		{ID: "gemini-3-pro-high", Name: "Gemini 3 Pro High (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 2.375,
			ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-3-pro-low", Name: "Gemini 3 Pro Low (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 2.375,
			ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-3.1-pro-high", Name: "Gemini 3.1 Pro High (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 2.375,
			ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-3.1-pro-low", Name: "Gemini 3.1 Pro Low (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 2.375,
			ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "gemini-3-flash", Name: "Gemini 3 Flash (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.5, CostOutput: 3, CostCacheRead: 0.5, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65535},
		{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5 (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 3, CostOutput: 15, CostCacheRead: 0.3, CostCacheWrite: 3.75,
			ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-sonnet-4-5-thinking", Name: "Claude Sonnet 4.5 Thinking (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 3, CostOutput: 15, CostCacheRead: 0.3, CostCacheWrite: 3.75,
			ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-opus-4-5-thinking", Name: "Claude Opus 4.5 Thinking (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 200000, MaxTokens: 64000},
		{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 Thinking (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 5, CostOutput: 25, CostCacheRead: 0.5, CostCacheWrite: 6.25,
			ContextWindow: 200000, MaxTokens: 128000},
		{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B Medium (Antigravity)", API: "google-gemini-cli",
			Provider: "google-antigravity", BaseURL: antigravityEndpoint, Reasoning: false,
			Input: []string{"text"}, CostInput: 0.09, CostOutput: 0.36, CostCacheRead: 0, CostCacheWrite: 0,
			ContextWindow: 131072, MaxTokens: 32768},
	}
	all = append(all, antigravityModels...)

	// Google Vertex models
	const vertexBaseURL = "https://{location}-aiplatform.googleapis.com"
	vertexModels := []modelSpec{
		{ID: "gemini-3-pro-preview", Name: "Gemini 3 Pro Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 64000},
		{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-3-flash-preview", Name: "Gemini 3 Flash Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.5, CostOutput: 3, CostCacheRead: 0.05, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-3.1-pro-preview", Name: "Gemini 3.1 Pro Preview (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 2, CostOutput: 12, CostCacheRead: 0.2, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 0.15, CostOutput: 0.6, CostCacheRead: 0.0375, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 8192},
		{ID: "gemini-2.0-flash-lite", Name: "Gemini 2.0 Flash Lite (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.075, CostOutput: 0.3, CostCacheRead: 0.01875, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 1.25, CostOutput: 10, CostCacheRead: 0.125, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.3, CostOutput: 2.5, CostCacheRead: 0.03, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-flash-lite-preview-09-2025", Name: "Gemini 2.5 Flash Lite Preview 09-25 (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.1, CostOutput: 0.4, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-2.5-flash-lite", Name: "Gemini 2.5 Flash Lite (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: true,
			Input: []string{"text", "image"}, CostInput: 0.1, CostOutput: 0.4, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1048576, MaxTokens: 65536},
		{ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 1.25, CostOutput: 5, CostCacheRead: 0.3125, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 8192},
		{ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 0.075, CostOutput: 0.3, CostCacheRead: 0.01875, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 8192},
		{ID: "gemini-1.5-flash-8b", Name: "Gemini 1.5 Flash-8B (Vertex)", API: "google-vertex",
			Provider: "google-vertex", BaseURL: vertexBaseURL, Reasoning: false,
			Input: []string{"text", "image"}, CostInput: 0.0375, CostOutput: 0.15, CostCacheRead: 0.01, CostCacheWrite: 0,
			ContextWindow: 1000000, MaxTokens: 8192},
	}
	all = append(all, vertexModels...)

	// Kimi For Coding static fallbacks
	kimiCodingModels := []modelSpec{
		{ID: "kimi-k2-thinking", Name: "Kimi K2 Thinking", API: "anthropic-messages",
			Provider: "kimi-coding", BaseURL: "https://api.kimi.com/coding", Reasoning: true,
			Input: []string{"text"}, ContextWindow: 262144, MaxTokens: 32768},
		{ID: "k2p5", Name: "Kimi K2.5", API: "anthropic-messages",
			Provider: "kimi-coding", BaseURL: "https://api.kimi.com/coding", Reasoning: true,
			Input: []string{"text"}, ContextWindow: 262144, MaxTokens: 32768},
	}
	for _, m := range kimiCodingModels {
		if !hasModel(all, "kimi-coding", m.ID) {
			all = append(all, m)
		}
	}

	return all
}

// deduplicate groups models by provider/id and returns deduped+sorted results.
// models.dev has priority over OpenRouter/AI Gateway (earlier entries win).
func deduplicate(all []modelSpec) []modelSpec {
	// Add Azure OpenAI models derived from OpenAI openai-responses models
	var azureModels []modelSpec
	for _, m := range all {
		if m.Provider == "openai" && m.API == "openai-responses" {
			az := m
			az.API = "azure-openai-responses"
			az.Provider = "azure-openai-responses"
			az.BaseURL = ""
			azureModels = append(azureModels, az)
		}
	}
	all = append(all, azureModels...)

	// Deduplicate: first seen wins
	type key struct{ provider, id string }
	seen := make(map[key]bool)
	var unique []modelSpec
	for _, m := range all {
		k := key{m.Provider, m.ID}
		if !seen[k] {
			seen[k] = true
			unique = append(unique, m)
		}
	}

	// Sort by provider then model ID for deterministic output
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Provider != unique[j].Provider {
			return unique[i].Provider < unique[j].Provider
		}
		return unique[i].ID < unique[j].ID
	})
	return unique
}

// formatFloat formats a float64 for Go source output.
// It rounds to 8 decimal places to avoid floating-point noise from
// per-token price arithmetic (e.g. 0.2 × 1e6 → 0.19999999999999998).
func formatFloat(f float64) string {
	rounded := math.Round(f*1e8) / 1e8
	return fmt.Sprintf("%g", rounded)
}

// goString escapes a string for a Go string literal (double-quoted).
func goString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// renderCompat renders a compatSpec as a Go expression.
func renderCompat(c *compatSpec) string {
	if c == nil {
		return "nil"
	}
	var fields []string
	if c.SupportsStore != nil {
		fields = append(fields, fmt.Sprintf("SupportsStore: boolRef(%v)", *c.SupportsStore))
	}
	if c.SupportsDeveloperRole != nil {
		fields = append(fields, fmt.Sprintf("SupportsDeveloperRole: boolRef(%v)", *c.SupportsDeveloperRole))
	}
	if c.SupportsReasoningEffort != nil {
		fields = append(fields, fmt.Sprintf("SupportsReasoningEffort: boolRef(%v)", *c.SupportsReasoningEffort))
	}
	if c.ThinkingFormat != "" {
		fields = append(fields, fmt.Sprintf("ThinkingFormat: %s", goString(c.ThinkingFormat)))
	}
	if len(fields) == 0 {
		return "nil"
	}
	return "&OpenAICompletionsCompat{" + strings.Join(fields, ", ") + "}"
}

// renderHeaders renders a map[string]string as a Go map literal or nil.
func renderHeaders(h map[string]string) string {
	if len(h) == 0 {
		return "nil"
	}
	// Sort keys for deterministic output
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", goString(k), goString(h[k])))
	}
	return "map[string]string{" + strings.Join(parts, ", ") + "}"
}

// renderInput renders a []string as a Go string slice literal.
func renderInput(input []string) string {
	quoted := make([]string, len(input))
	for i, s := range input {
		quoted[i] = goString(s)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// generateGoSource generates the complete Go source file from the model list.
func generateGoSource(models []modelSpec) string {
	var sb strings.Builder
	sb.WriteString("// Code generated by cmd/generate-models. DO NOT EDIT.\n")
	sb.WriteString(fmt.Sprintf("// Generated at: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("package ai\n\n")
	sb.WriteString("func init() {\n")

	for _, m := range models {
		sb.WriteString("\tRegisterModel(&Model{\n")
		sb.WriteString(fmt.Sprintf("\t\tID:            %s,\n", goString(m.ID)))
		sb.WriteString(fmt.Sprintf("\t\tName:          %s,\n", goString(m.Name)))
		sb.WriteString(fmt.Sprintf("\t\tApi:           %s,\n", goString(m.API)))
		sb.WriteString(fmt.Sprintf("\t\tProvider:      %s,\n", goString(m.Provider)))
		sb.WriteString(fmt.Sprintf("\t\tBaseURL:       %s,\n", goString(m.BaseURL)))
		sb.WriteString(fmt.Sprintf("\t\tReasoning:     %v,\n", m.Reasoning))
		sb.WriteString(fmt.Sprintf("\t\tInput:         %s,\n", renderInput(m.Input)))
		sb.WriteString(fmt.Sprintf("\t\tCost:          ModelCost{Input: %s, Output: %s, CacheRead: %s, CacheWrite: %s},\n",
			formatFloat(m.CostInput), formatFloat(m.CostOutput),
			formatFloat(m.CostCacheRead), formatFloat(m.CostCacheWrite)))
		sb.WriteString(fmt.Sprintf("\t\tContextWindow: %d,\n", m.ContextWindow))
		sb.WriteString(fmt.Sprintf("\t\tMaxTokens:     %d,\n", m.MaxTokens))
		sb.WriteString(fmt.Sprintf("\t\tHeaders:       %s,\n", renderHeaders(m.Headers)))
		compat := renderCompat(m.Compat)
		if compat != "nil" {
			sb.WriteString(fmt.Sprintf("\t\tCompat:        %s,\n", compat))
		}
		sb.WriteString("\t})\n")
	}

	sb.WriteString("}\n")
	return sb.String()
}

// repoRoot returns the root of the fir repository (parent of cmd/generate-models).
func repoRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	// filename is .../cmd/generate-models/main.go
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func main() {
	defaultOut := filepath.Join(repoRoot(), "pkg", "ai", "models_generated.go")
	out := flag.String("out", defaultOut, "output file path")
	flag.Parse()

	// Fetch from all sources
	modelsDevModels, err := loadModelsDevData()
	if err != nil {
		log.Fatalf("models.dev: %v", err)
	}

	openRouterModels, err := fetchOpenRouterModels()
	if err != nil {
		log.Printf("Warning: OpenRouter fetch failed: %v", err)
	}

	aiGatewayModels, err := fetchAIGatewayModels()
	if err != nil {
		log.Printf("Warning: AI Gateway fetch failed: %v", err)
	}

	// Combine: models.dev first (takes priority during dedup)
	all := append(modelsDevModels, openRouterModels...)
	all = append(all, aiGatewayModels...)

	// Apply manual overrides and additions
	all = applyOverridesAndAdditions(all)

	// Deduplicate and sort
	all = deduplicate(all)

	// Generate Go source
	source := generateGoSource(all)

	// Write output
	if err := os.WriteFile(*out, []byte(source), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}

	log.Printf("Generated %s", *out)

	// Print statistics
	reasoningCount := 0
	for _, m := range all {
		if m.Reasoning {
			reasoningCount++
		}
	}
	log.Printf("Model Statistics:")
	log.Printf("  Total models: %d", len(all))
	log.Printf("  Reasoning-capable models: %d", reasoningCount)

	// Per-provider counts
	providerCounts := make(map[string]int)
	for _, m := range all {
		providerCounts[m.Provider]++
	}
	providerNames := make([]string, 0, len(providerCounts))
	for p := range providerCounts {
		providerNames = append(providerNames, p)
	}
	sort.Strings(providerNames)
	for _, p := range providerNames {
		log.Printf("  %s: %d models", p, providerCounts[p])
	}
}
