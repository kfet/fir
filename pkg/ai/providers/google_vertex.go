// Ported from: packages/ai/src/providers/google-vertex.ts
// Upstream hash: 41039e8d
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// --- Vertex AI configuration ---

const vertexAPIVersion = "v1"

// --- ADC (Application Default Credentials) ---

type adcCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	Type         string `json:"type"`
}

type adcTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// adcTokenCache caches the access token to avoid refreshing on every request.
var adcTokenCache struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// loadADCCredentials loads Application Default Credentials from the filesystem.
func loadADCCredentials() (*adcCredentials, error) {
	// Check GOOGLE_APPLICATION_CREDENTIALS first
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return loadADCFromFile(path)
	}

	// Fall back to default gcloud ADC path
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	adcPath := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	return loadADCFromFile(adcPath)
}

func loadADCFromFile(path string) (*adcCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read ADC credentials from %s: %w", path, err)
	}
	var creds adcCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("cannot parse ADC credentials: %w", err)
	}
	if creds.Type != "authorized_user" {
		return nil, fmt.Errorf("unsupported ADC type: %s (only authorized_user is supported)", creds.Type)
	}
	return &creds, nil
}

// getVertexAccessToken returns a fresh access token, refreshing if needed.
func getVertexAccessToken(ctx context.Context) (string, error) {
	adcTokenCache.mu.Lock()
	defer adcTokenCache.mu.Unlock()

	// Return cached token if still valid (with 60s buffer)
	if adcTokenCache.token != "" && time.Now().Add(60*time.Second).Before(adcTokenCache.expiresAt) {
		return adcTokenCache.token, nil
	}

	creds, err := loadADCCredentials()
	if err != nil {
		return "", err
	}

	// Exchange refresh token for access token
	resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"refresh_token": {creds.RefreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp adcTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}

	adcTokenCache.token = tokenResp.AccessToken
	adcTokenCache.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return tokenResp.AccessToken, nil
}

// --- Stream function ---

// StreamGoogleVertex implements streaming for the Google Vertex AI API.
func StreamGoogleVertex(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
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

		firlog.Debug("vertex request", "model", model.ID, "messageCount", len(prompt.Messages))
		if err := streamVertexHTTP(ctx, model, prompt, options, output, stream); err != nil {
			output.StopReason = ai.StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: output})
			return
		}

		firlog.Debug("vertex response complete", "model", model.ID, "stopReason", output.StopReason)
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Reason:  output.StopReason,
			Message: output,
		})
	}()

	return stream
}

func streamVertexHTTP(
	ctx context.Context,
	model *ai.Model,
	prompt ai.Context,
	options *ai.StreamOptions,
	output *ai.AssistantMessage,
	stream *ai.AssistantMessageEventStream,
) error {
	// Build request body (same format as regular Google)
	body, err := buildGoogleRequestBody(model, prompt, options)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if options != nil && options.OnPayload != nil {
		var rawBody map[string]any
		if jsonErr := json.Unmarshal(body, &rawBody); jsonErr == nil {
			if next := options.OnPayload(rawBody, model); next != nil {
				if body, err = json.Marshal(next); err != nil {
					return fmt.Errorf("re-marshaling payload: %w", err)
				}
			}
		}
	}

	apiKey := resolveVertexAPIKey(options)

	var vertexURL string
	var authHeader string

	if apiKey != "" {
		// API key auth: use the global Vertex AI Express endpoint (no project/location needed).
		vertexURL = fmt.Sprintf(
			"https://aiplatform.googleapis.com/%s/models/%s:streamGenerateContent?alt=sse",
			vertexAPIVersion, model.ID,
		)
		authHeader = "" // API key goes in x-goog-api-key header below
	} else {
		// ADC auth: requires project and location.
		project := resolveVertexProject(options)
		if project == "" {
			return fmt.Errorf("Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT")
		}
		location := resolveVertexLocation(options)
		if location == "" {
			return fmt.Errorf("Vertex AI requires a location. Set GOOGLE_CLOUD_LOCATION")
		}
		accessToken, err := getVertexAccessToken(ctx)
		if err != nil {
			return fmt.Errorf("authentication: %w", err)
		}
		authHeader = "Bearer " + accessToken
		vertexURL = fmt.Sprintf(
			"https://%s-aiplatform.googleapis.com/%s/projects/%s/locations/%s/publishers/google/models/%s:streamGenerateContent?alt=sse",
			location, vertexAPIVersion, project, location, model.ID,
		)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", vertexURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	}

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
		return fmt.Errorf("Vertex AI request failed (model=%s): connection error", model.ID)
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

	// Reuse the same response parser as the regular Google provider
	return parseGoogleResponse(resp.Body, model, output, stream)
}

// resolveVertexAPIKey returns the Vertex AI API key from options or env.
// When set, API key authentication is used instead of ADC (no project/location needed).
func resolveVertexAPIKey(options *ai.StreamOptions) string {
	if options != nil && options.ApiKey != "" {
		return options.ApiKey
	}
	return os.Getenv("GOOGLE_CLOUD_API_KEY")
}

// resolveVertexProject resolves the GCP project from options or env.
func resolveVertexProject(options *ai.StreamOptions) string {
	if options != nil {
		if v := options.Headers["x-vertex-project"]; v != "" {
			delete(options.Headers, "x-vertex-project")
			return v
		}
	}
	if v := os.Getenv("GOOGLE_CLOUD_PROJECT"); v != "" {
		return v
	}
	return os.Getenv("GCLOUD_PROJECT")
}

// resolveVertexLocation resolves the GCP location from options or env.
func resolveVertexLocation(options *ai.StreamOptions) string {
	if options != nil {
		if v := options.Headers["x-vertex-location"]; v != "" {
			delete(options.Headers, "x-vertex-location")
			return v
		}
	}
	return os.Getenv("GOOGLE_CLOUD_LOCATION")
}

// --- Simple wrapper ---

// StreamSimpleGoogleVertex wraps StreamGoogleVertex with SimpleStreamOptions.
func StreamSimpleGoogleVertex(ctx context.Context, model *ai.Model, prompt ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	base := BuildBaseOptions(model, options, "")

	if options == nil || options.Reasoning == "" {
		// No reasoning requested
		return StreamGoogleVertex(ctx, model, prompt, base)
	}

	if options.Reasoning == ai.ThinkingOff && model.Reasoning {
		if base.Headers == nil {
			base.Headers = make(map[string]string)
		}
		base.Headers["x-google-thinking-disabled"] = "true"
		return StreamGoogleVertex(ctx, model, prompt, base)
	}

	effort := ai.ThinkingLevel(ClampReasoning(options.Reasoning))

	// Gemini 3 models use level-based thinking
	if isGemini3Model(model) {
		level := mapGeminiThinkingLevel(effort, model)
		if level != "" {
			if base.Headers == nil {
				base.Headers = make(map[string]string)
			}
			base.Headers["x-thinking-level"] = level
		}
		return StreamGoogleVertex(ctx, model, prompt, base)
	}

	// Older models use budget-based thinking
	budget := getGoogleBudget(model, effort, nil)
	if budget != 0 {
		if base.Headers == nil {
			base.Headers = make(map[string]string)
		}
		base.Headers["x-thinking-budget"] = fmt.Sprintf("%d", budget)
	}
	return StreamGoogleVertex(ctx, model, prompt, base)
}

// RegisterGoogleVertex registers the Google Vertex AI provider.
func RegisterGoogleVertex(reg *ai.Registry) {
	reg.RegisterApiProvider(&ai.ApiProvider{
		Api:          ai.ApiGoogleVertex,
		Stream:       StreamGoogleVertex,
		StreamSimple: StreamSimpleGoogleVertex,
	}, "builtin")
}
