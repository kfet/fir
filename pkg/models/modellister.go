package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
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

// ListerModelDefaulter is an optional interface for ModelLister implementations
// that can supply metadata for a live-listed model ID not present in the
// built-in registry. Returns nil to defer to the generic sibling-clone
// fallback. Implementations must be cheap (no network).
type ListerModelDefaulter interface {
	ModelDefaults(provider, modelID string, siblings []*ai.Model) *ai.Model
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

// ModelDefaults provides per-provider heuristics. The OpenAI lister is shared
// across several providers (openai, xai, groq, cerebras, openrouter, huggingface)
// each of which has its own naming conventions worth special-casing.
func (l *openAIModelLister) ModelDefaults(provider, modelID string, siblings []*ai.Model) *ai.Model {
	switch provider {
	case "openai":
		return cloneFromGroup(modelID, siblings, openAIFamily, humaniseID)
	case "openrouter":
		return cloneFromGroup(modelID, siblings, openRouterVendor, humaniseID)
	default:
		// xai, groq, cerebras, huggingface: rely on generic sibling-clone.
		return nil
	}
}

// cloneFromGroup picks the lexicographically-greatest sibling whose group
// (per groupFn) matches the new model's group, then clones it with the new
// ID and a humanised name (per nameFn). Returns nil if no sibling matches.
func cloneFromGroup(
	modelID string,
	siblings []*ai.Model,
	groupFn func(string) string,
	nameFn func(string) string,
) *ai.Model {
	group := groupFn(modelID)
	if group == "" {
		return nil
	}
	var match *ai.Model
	for _, s := range siblings {
		if groupFn(s.ID) == group {
			if match == nil || s.ID > match.ID {
				match = s
			}
		}
	}
	if match == nil {
		return nil
	}
	out := *match
	out.ID = modelID
	out.Name = nameFn(modelID)
	out.SWEInferred = true
	return &out
}

// openAIFamily classifies an OpenAI model ID into its family group.
func openAIFamily(id string) string {
	switch {
	case strings.HasPrefix(id, "gpt-4o"):
		return "gpt-4o"
	case strings.HasPrefix(id, "gpt-4.1"):
		return "gpt-4.1"
	case strings.HasPrefix(id, "gpt-5"):
		return "gpt-5"
	case strings.HasPrefix(id, "o1"):
		return "o1"
	case strings.HasPrefix(id, "o3"):
		return "o3"
	case strings.HasPrefix(id, "o4"):
		return "o4"
	}
	return ""
}

// openRouterVendor extracts the vendor prefix: "anthropic/claude-x" -> "anthropic".
func openRouterVendor(id string) string {
	vendor, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return vendor
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

// ModelDefaults parses Anthropic IDs of the form
// "claude-<family>-<major>-<minor>-<yyyymmdd>" and clones the most recent
// sibling of the same family. Falls back to nil for unparseable IDs.
func (l *anthropicModelLister) ModelDefaults(_ string, modelID string, siblings []*ai.Model) *ai.Model {
	return cloneFromGroup(modelID, siblings, anthropicFamily, anthropicHumanName)
}

// anthropicFamily extracts the family token: "claude-sonnet-4-7-20260601" -> "sonnet".
func anthropicFamily(id string) string {
	if !strings.HasPrefix(id, "claude-") {
		return ""
	}
	rest := strings.TrimPrefix(id, "claude-")
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "opus", "sonnet", "haiku":
		return parts[0]
	}
	return ""
}

// anthropicHumanName produces a readable name like "Claude Sonnet 4 7 (2026-06-01)".
// If no trailing date is present, falls back to humaniseID.
func anthropicHumanName(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) >= 2 && len(parts[len(parts)-1]) == 8 && allDigits(parts[len(parts)-1]) {
		date := parts[len(parts)-1]
		core := humaniseID(strings.Join(parts[:len(parts)-1], "-"))
		return fmt.Sprintf("%s (%s-%s-%s)", core, date[:4], date[4:6], date[6:8])
	}
	return humaniseID(id)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
