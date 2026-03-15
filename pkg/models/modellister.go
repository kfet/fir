package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LiveModelInfo represents a model ID returned by a provider's list-models API.
type LiveModelInfo struct {
	ID string `json:"id"`
}

// ModelLister fetches available model IDs from a provider API.
type ModelLister interface {
	// ListModels returns the model IDs available for this provider.
	ListModels(ctx context.Context, baseURL, apiKey string) ([]LiveModelInfo, error)
}

// --- OpenAI-compatible lister (covers OpenAI, xAI, Groq, Cerebras, OpenRouter, Huggingface) ---

type openAIModelLister struct{}

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (l *openAIModelLister) ListModels(ctx context.Context, baseURL, apiKey string) ([]LiveModelInfo, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClientForListing.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("list models: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result openAIModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]LiveModelInfo, len(result.Data))
	for i, m := range result.Data {
		models[i] = LiveModelInfo{ID: m.ID}
	}
	return models, nil
}

// --- Anthropic lister ---

type anthropicModelLister struct{}

type anthropicModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	HasMore bool `json:"has_more"`
}

func (l *anthropicModelLister) ListModels(ctx context.Context, baseURL, apiKey string) ([]LiveModelInfo, error) {
	base := strings.TrimRight(baseURL, "/") + "/v1/models"
	var all []LiveModelInfo
	afterID := ""

	for {
		url := base + "?limit=100"
		if afterID != "" {
			url += "&after_id=" + afterID
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := httpClientForListing.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("list models: HTTP %d: %s", resp.StatusCode, string(body))
		}

		var result anthropicModelsResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}

		for _, m := range result.Data {
			all = append(all, LiveModelInfo{ID: m.ID})
		}

		if !result.HasMore || len(result.Data) == 0 {
			break
		}
		afterID = result.Data[len(result.Data)-1].ID
	}

	return all, nil
}

// --- Google Generative AI lister ---

type googleModelLister struct{}

type googleModelsResponse struct {
	Models []struct {
		Name string `json:"name"` // e.g. "models/gemini-2.0-flash"
	} `json:"models"`
}

func (l *googleModelLister) ListModels(ctx context.Context, baseURL, apiKey string) ([]LiveModelInfo, error) {
	url := strings.TrimRight(baseURL, "/") + "/models?pageSize=100&key=" + apiKey
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClientForListing.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("list models: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result googleModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]LiveModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		// Strip "models/" prefix: "models/gemini-2.0-flash" -> "gemini-2.0-flash"
		id := strings.TrimPrefix(m.Name, "models/")
		models = append(models, LiveModelInfo{ID: id})
	}
	return models, nil
}

// --- Shared HTTP client with short timeout ---

var httpClientForListing = &http.Client{
	Timeout: 10 * time.Second,
}

// --- Provider -> Lister mapping ---

// GetModelLister returns a ModelLister for the given provider, or nil if
// live listing is not supported for that provider.
func GetModelLister(provider string) ModelLister {
	switch provider {
	case "openai", "xai", "groq", "cerebras", "openrouter", "huggingface":
		return &openAIModelLister{}
	case "anthropic":
		return &anthropicModelLister{}
	case "google":
		return &googleModelLister{}
	default:
		return nil
	}
}
