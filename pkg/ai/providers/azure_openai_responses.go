// Ported from: packages/ai/src/providers/azure-openai-responses.ts
// Upstream hash: 036bde0a
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	firlog "github.com/kfet/fir/pkg/log"
)

const defaultAzureAPIVersion = "v1"

// azureToolCallProviders is the set of providers whose tool call IDs need normalization.
var azureToolCallProviders = map[string]bool{
	"openai":                 true,
	"openai-codex":           true,
	"opencode":               true,
	"azure-openai-responses": true,
}

// parseDeploymentNameMap parses a comma-separated "modelId=deploymentName" map.
func parseDeploymentNameMap(value string) map[string]string {
	m := make(map[string]string)
	if value == "" {
		return m
	}
	for _, entry := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return m
}

// resolveDeploymentName determines the deployment name for the model.
func resolveDeploymentName(model *ai.Model, deploymentOverride string) string {
	if deploymentOverride != "" {
		return deploymentOverride
	}
	nameMap := parseDeploymentNameMap(os.Getenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP"))
	if mapped, ok := nameMap[model.ID]; ok {
		return mapped
	}
	return model.ID
}

// resolveAzureConfig determines the base URL and API version for Azure OpenAI.
func resolveAzureConfig(model *ai.Model, baseURLOverride, resourceNameOverride, apiVersionOverride string) (baseURL, apiVersion string, err error) {
	apiVersion = apiVersionOverride
	if apiVersion == "" {
		apiVersion = os.Getenv("AZURE_OPENAI_API_VERSION")
	}
	if apiVersion == "" {
		apiVersion = defaultAzureAPIVersion
	}

	baseURL = strings.TrimSpace(baseURLOverride)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("AZURE_OPENAI_BASE_URL"))
	}

	if baseURL == "" {
		resourceName := resourceNameOverride
		if resourceName == "" {
			resourceName = os.Getenv("AZURE_OPENAI_RESOURCE_NAME")
		}
		if resourceName != "" {
			baseURL = fmt.Sprintf("https://%s.openai.azure.com/openai/v1", resourceName)
		}
	}

	if baseURL == "" && model.BaseURL != "" {
		baseURL = model.BaseURL
	}

	if baseURL == "" {
		return "", "", fmt.Errorf("Azure OpenAI base URL is required. Set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME, or pass azureBaseUrl, azureResourceName, or model.baseUrl")
	}

	baseURL = normalizeAzureBaseURL(baseURL)
	return baseURL, apiVersion, nil
}

// normalizeAzureBaseURL trims trailing slashes and, for Azure OpenAI / Cognitive
// Services hosts that lack the standard `/openai/v1` path, appends it so the
// AzureOpenAI SDK can correctly construct the deployment endpoint.
//
// This matches upstream pi-mono's normalization (v0.71.x): users frequently
// configure just `https://<resource>.openai.azure.com` in models.json — the
// SDK then needs `/openai/v1` to build `/deployments/<id>/...?api-version=v1`.
func normalizeAzureBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return trimmed
	}
	host := strings.ToLower(u.Hostname())
	isAzureHost := strings.HasSuffix(host, ".openai.azure.com") || strings.HasSuffix(host, ".cognitiveservices.azure.com")
	path := strings.TrimRight(u.Path, "/")
	if isAzureHost && (path == "" || path == "/openai") {
		u.Path = "/openai/v1"
		u.RawQuery = ""
	}
	out := u.String()
	return strings.TrimRight(out, "/")
}

// StreamAzureOpenAIResponses implements streaming for the Azure OpenAI Responses API.
func StreamAzureOpenAIResponses(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()

	go func() {
		output := &ai.AssistantMessage{
			Role:       ai.RoleAssistant,
			Content:    []ai.AssistantContent{},
			API:        model.API,
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
			apiKey = options.APIKey
		}
		if apiKey == "" {
			apiKey = envkeys.GetEnvApiKey(model.Provider)
		}
		if apiKey == "" {
			apiKey = os.Getenv("AZURE_OPENAI_API_KEY")
		}
		if apiKey == "" {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = "Azure OpenAI API key is required. Set AZURE_OPENAI_API_KEY environment variable or pass it as an argument."
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		// Resolve deployment name
		deploymentOverride := ""
		if options != nil {
			deploymentOverride = options.Headers["x-azure-deployment-name"]
			delete(options.Headers, "x-azure-deployment-name")
		}
		deploymentName := resolveDeploymentName(model, deploymentOverride)

		// Resolve Azure config
		baseURLOverride := ""
		resourceNameOverride := ""
		apiVersionOverride := ""
		if options != nil {
			baseURLOverride = options.Headers["x-azure-base-url"]
			delete(options.Headers, "x-azure-base-url")
			resourceNameOverride = options.Headers["x-azure-resource-name"]
			delete(options.Headers, "x-azure-resource-name")
			apiVersionOverride = options.Headers["x-azure-api-version"]
			delete(options.Headers, "x-azure-api-version")
		}

		baseURL, _, err := resolveAzureConfig(model, baseURLOverride, resourceNameOverride, apiVersionOverride)
		if err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		body, err := buildAzureResponsesBody(model, prompt, options, deploymentName)
		if err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = fmt.Sprintf("building request: %v", err)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}
		if options != nil && options.OnPayload != nil {
			var rawBody map[string]any
			if jsonErr := json.Unmarshal(body, &rawBody); jsonErr == nil {
				if next := options.OnPayload(rawBody, model); next != nil {
					if body, err = json.Marshal(next); err != nil {
						output.StopReason = ai.StopReasonError
						output.ErrorMessage = fmt.Sprintf("re-marshaling payload: %v", err)
						stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
						return
					}
				}
			}
		}

		url := baseURL + "/responses"

		headers := BuildRequestHeaders(
			map[string]string{"api-key": apiKey},
			model, options,
		)

		firlog.Debug("azure-openai request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))
		sseEvents, sseErr := DefaultSSEClient.Stream(ctx, url, headers, bytes.NewReader(body))

		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: output})

		proc := &responsesSSEProcessor{output: output, stream: stream, model: model}
		errFromSSE := processResponsesSSEStream(proc, sseEvents, sseErr)
		if errFromSSE != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = errFromSSE.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		firlog.Debug("azure-openai response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
	}()

	return stream
}

// buildAzureResponsesBody builds the request body for the Azure OpenAI Responses API.
func buildAzureResponsesBody(model *ai.Model, ctx ai.Context, options *ai.StreamOptions, deploymentName string) ([]byte, error) {
	body := map[string]any{
		"model":  deploymentName,
		"stream": true,
	}

	// Messages (input)
	input := convertResponsesInput(model, ctx)
	body["input"] = input

	// Max tokens
	if options != nil && options.MaxTokens != nil {
		body["max_output_tokens"] = *options.MaxTokens
	}

	// Temperature
	if options != nil && options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}

	// Session ID for prompt caching
	if options != nil && options.SessionID != "" {
		body["prompt_cache_key"] = options.SessionID
	}

	// Tools
	if len(ctx.Tools) > 0 {
		body["tools"] = convertResponsesTools(ctx.Tools, false)
	}

	// Reasoning
	if model.Reasoning {
		reasoningEffort := ""
		reasoningSummary := ""
		if options != nil {
			reasoningEffort = string(options.ReasoningEffort)
			// Check for custom summary setting in headers
			reasoningSummary = options.Headers["x-azure-reasoning-summary"]
			delete(options.Headers, "x-azure-reasoning-summary")
		}

		if reasoningEffort != "" || reasoningSummary != "" {
			effort := reasoningEffort
			if effort == "" {
				effort = "medium"
			}
			summary := reasoningSummary
			if summary == "" {
				summary = "auto"
			}
			body["reasoning"] = map[string]any{
				"effort":  effort,
				"summary": summary,
			}
			body["include"] = []string{"reasoning.encrypted_content"}
		} else {
			body["reasoning"] = map[string]any{"effort": "none"}
		}
	}

	return json.Marshal(body)
}

// StreamSimpleAzureOpenAIResponses wraps StreamAzureOpenAIResponses with SimpleStreamOptions.
func StreamSimpleAzureOpenAIResponses(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == "" {
		apiKey = envkeys.GetEnvApiKey(model.Provider)
	}
	if apiKey == "" {
		apiKey = os.Getenv("AZURE_OPENAI_API_KEY")
	}
	if apiKey == "" {
		return errorStreamProvider(model, "Azure OpenAI API key is required. Set AZURE_OPENAI_API_KEY environment variable or pass it as an argument.")
	}

	base := BuildBaseOptions(model, options, apiKey)

	if options != nil && options.Reasoning != "" && model.Reasoning {
		reasoningEffort := ClampReasoningForModel(options.Reasoning, model)
		if reasoningEffort != "" {
			base.ReasoningEffort = reasoningEffort
		}
	}

	return StreamAzureOpenAIResponses(ctx, model, prompt, base)
}

// RegisterAzureOpenAIResponses registers the Azure OpenAI Responses provider.
func RegisterAzureOpenAIResponses(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiAzureOpenAIResponses,
		Stream:       StreamAzureOpenAIResponses,
		StreamSimple: StreamSimpleAzureOpenAIResponses,
	}, "builtin")
}
