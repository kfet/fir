// Ported from: packages/ai/src/utils/oauth/github-copilot.ts
// Upstream hash: f04d9bc4
package oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

const (
	githubClientIDEncoded = "SXYxLmI1MDdhMDhjODdlY2ZlOTg="
)

var (
	githubClientID string
	proxyEPRegexp  = regexp.MustCompile(`proxy-ep=([^;]+)`)

	// pollIntervalUnit is the time unit for poll intervals. Overridable in tests.
	pollIntervalUnit = time.Second

	// Test-override URLs (empty = use production URLs derived from domain).
	githubAccessTokenURLOverride  string
	githubCopilotTokenURLOverride string
	githubCopilotBaseURLOverride  string
)

// NOTE: The following headers intentionally impersonate the GitHub Copilot Chat
// VS Code extension. These values are ported directly from the upstream TypeScript
// source and are required by GitHub's OAuth and API endpoints to accept requests.
// Changing them breaks authentication. This is a known ToS risk inherited from the
// original client implementation.
var copilotHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

func init() {
	b, err := base64.StdEncoding.DecodeString(githubClientIDEncoded)
	if err != nil {
		githubClientID = "unknown"
	} else {
		githubClientID = string(b)
	}
}

// NormalizeDomain normalizes a user-supplied domain string to a hostname.
func NormalizeDomain(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	raw := trimmed
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

func githubURLs(domain string) (deviceCodeURL, accessTokenURL, copilotTokenURL string) {
	return fmt.Sprintf("https://%s/login/device/code", domain),
		fmt.Sprintf("https://%s/login/oauth/access_token", domain),
		fmt.Sprintf("https://api.%s/copilot_internal/v2/token", domain)
}

// GetBaseURLFromToken extracts the API base URL from a Copilot token.
// Token format: tid=...;exp=...;proxy-ep=proxy.individual.githubcopilot.com;...
func GetBaseURLFromToken(token string) string {
	match := proxyEPRegexp.FindStringSubmatch(token)
	if len(match) < 2 {
		return ""
	}
	proxyHost := match[1]
	apiHost := strings.Replace(proxyHost, "proxy.", "api.", 1)
	return "https://" + apiHost
}

// GetGitHubCopilotBaseURL returns the API base URL for GitHub Copilot.
func GetGitHubCopilotBaseURL(token, enterpriseDomain string) string {
	if token != "" {
		if u := GetBaseURLFromToken(token); u != "" {
			return u
		}
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}

// GitHubCopilotProvider implements GitHub Copilot OAuth (device code flow).
type GitHubCopilotProvider struct{}

func (p *GitHubCopilotProvider) ID() string               { return "github-copilot" }
func (p *GitHubCopilotProvider) Name() string             { return "GitHub Copilot" }
func (p *GitHubCopilotProvider) UsesCallbackServer() bool { return false }

func (p *GitHubCopilotProvider) Login(callbacks LoginCallbacks) (*Credentials, error) {
	return loginGitHubCopilot(callbacks)
}

func (p *GitHubCopilotProvider) RefreshToken(creds *Credentials) (*Credentials, error) {
	enterpriseDomain, _ := creds.Extra["enterpriseUrl"].(string)
	return refreshGitHubCopilotToken(creds.Refresh, enterpriseDomain)
}

func (p *GitHubCopilotProvider) GetAPIKey(creds *Credentials) string {
	return creds.Access
}

func (p *GitHubCopilotProvider) ModifyModels(models []*ai.Model, creds *Credentials) []*ai.Model {
	enterpriseDomain, _ := creds.Extra["enterpriseUrl"].(string)
	domain := ""
	if enterpriseDomain != "" {
		domain = NormalizeDomain(enterpriseDomain)
	}
	baseURL := GetGitHubCopilotBaseURL(creds.Access, domain)

	result := make([]*ai.Model, len(models))
	for i, m := range models {
		if m.Provider == "github-copilot" {
			clone := *m
			clone.BaseURL = baseURL
			result[i] = &clone
		} else {
			result[i] = m
		}
	}
	return result
}

// loginGitHubCopilot runs the GitHub Copilot device code OAuth flow.
func loginGitHubCopilot(callbacks LoginCallbacks) (*Credentials, error) {
	ctx := callbacks.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// Prompt for enterprise domain (blank = github.com)
	if callbacks.OnPrompt == nil {
		return nil, fmt.Errorf("OnPrompt callback required for GitHub Copilot login")
	}
	input, err := callbacks.OnPrompt(Prompt{
		Message:     "GitHub Enterprise URL/domain (blank for github.com)",
		Placeholder: "company.ghe.com",
		AllowEmpty:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("prompting for domain: %w", err)
	}

	if ctx.Err() != nil {
		return nil, fmt.Errorf("login cancelled")
	}

	trimmed := strings.TrimSpace(input)
	enterpriseDomain := NormalizeDomain(input)
	if trimmed != "" && enterpriseDomain == "" {
		return nil, fmt.Errorf("invalid GitHub Enterprise URL/domain")
	}
	domain := "github.com"
	if enterpriseDomain != "" {
		domain = enterpriseDomain
	}

	// Start device code flow
	device, err := startGitHubDeviceFlow(domain)
	if err != nil {
		return nil, fmt.Errorf("starting device flow: %w", err)
	}

	// Show user the verification URL and code
	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{
			URL:          device.VerificationURI,
			Instructions: fmt.Sprintf("Enter code: %s", device.UserCode),
		})
	}

	// Poll for GitHub access token
	githubToken, err := pollForGitHubAccessToken(ctx, domain, device.DeviceCode, device.Interval, device.ExpiresIn)
	if err != nil {
		return nil, err
	}

	// Exchange GitHub access token for Copilot token
	creds, err := refreshGitHubCopilotToken(githubToken, enterpriseDomain)
	if err != nil {
		return nil, fmt.Errorf("getting Copilot token: %w", err)
	}

	// Enable all models
	if callbacks.OnProgress != nil {
		callbacks.OnProgress("Enabling models...")
	}
	enableAllCopilotModels(creds.Access, enterpriseDomain)

	return creds, nil
}

// deviceCodeResponse holds the response from the GitHub device code endpoint.
type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

func startGitHubDeviceFlow(domain string) (*deviceCodeResponse, error) {
	deviceCodeURL, _, _ := githubURLs(domain)

	formBody := url.Values{
		"client_id": {githubClientID},
		"scope":     {"read:user"},
	}

	req, err := http.NewRequest("POST", deviceCodeURL, strings.NewReader(formBody.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0") // see copilotHeaders comment above

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var result deviceCodeResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing device code response: %w", err)
	}
	return &result, nil
}

func pollForGitHubAccessToken(ctx context.Context, domain, deviceCode string, intervalSec, expiresIn int) (string, error) {
	_, accessTokenURL, _ := githubURLs(domain)
	if githubAccessTokenURLOverride != "" {
		accessTokenURL = githubAccessTokenURLOverride
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	intervalMs := time.Duration(max(1, intervalSec)) * pollIntervalUnit
	// Apply a multiplier so we don't poll at exactly the server-suggested interval.
	const initialMultiplier = 1.2
	const slowDownMultiplier = 1.4
	multiplier := initialMultiplier
	slowDownResponses := 0

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", fmt.Errorf("login cancelled")
		}

		remaining := time.Until(deadline)
		waitDuration := time.Duration(float64(intervalMs) * multiplier)
		if waitDuration > remaining {
			waitDuration = remaining
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("login cancelled")
		case <-time.After(waitDuration):
		}

		formBody := url.Values{
			"client_id":   {githubClientID},
			"device_code": {deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}

		req, err := http.NewRequestWithContext(ctx, "POST", accessTokenURL, strings.NewReader(formBody.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0") // see copilotHeaders comment above

		resp, err := oauthHTTPClient.Do(req)
		if err != nil {
			return "", err
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("access token request failed (%d): %s", resp.StatusCode, string(respBody))
		}

		var raw map[string]any
		if err := json.Unmarshal(respBody, &raw); err != nil {
			return "", fmt.Errorf("parsing access token response: %w", err)
		}

		// Success: access_token present
		if at, ok := raw["access_token"].(string); ok && at != "" {
			return at, nil
		}

		// Error response
		if errStr, ok := raw["error"].(string); ok {
			switch errStr {
			case "authorization_pending":
				// Keep polling
			case "slow_down":
				slowDownResponses++
				// Use server-suggested interval if provided, otherwise add 5 s.
				if serverInterval, ok := raw["interval"].(float64); ok && serverInterval > 0 {
					intervalMs = time.Duration(serverInterval) * time.Second
				} else {
					intervalMs += 5 * pollIntervalUnit
					if intervalMs < pollIntervalUnit {
						intervalMs = pollIntervalUnit
					}
				}
				multiplier = slowDownMultiplier
			default:
				description, _ := raw["error_description"].(string)
				if description != "" {
					return "", fmt.Errorf("device flow failed: %s: %s", errStr, description)
				}
				return "", fmt.Errorf("device flow failed: %s", errStr)
			}
		}
	}

	if slowDownResponses > 0 {
		return "", fmt.Errorf("device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again.")
	}
	return "", fmt.Errorf("device flow timed out")
}

// refreshGitHubCopilotToken exchanges a GitHub access token for a Copilot API token.
func refreshGitHubCopilotToken(refreshToken, enterpriseDomain string) (*Credentials, error) {
	domain := "github.com"
	if enterpriseDomain != "" {
		domain = enterpriseDomain
	}
	_, _, copilotTokenURL := githubURLs(domain)
	if githubCopilotTokenURLOverride != "" {
		copilotTokenURL = githubCopilotTokenURLOverride
	}

	req, err := http.NewRequest("GET", copilotTokenURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	for k, v := range copilotHeaders {
		req.Header.Set(k, v)
	}

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot token request failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("parsing Copilot token response: %w", err)
	}

	token, _ := raw["token"].(string)
	expiresAt, _ := raw["expires_at"].(float64)
	if token == "" {
		return nil, fmt.Errorf("invalid Copilot token response: missing token")
	}

	extra := map[string]any{}
	if enterpriseDomain != "" {
		extra["enterpriseUrl"] = enterpriseDomain
	}

	return &Credentials{
		Refresh: refreshToken,
		Access:  token,
		Expires: int64(expiresAt)*1000 - 5*60*1000,
		Extra:   extra,
	}, nil
}

// enableAllCopilotModels attempts to enable all known GitHub Copilot models.
func enableAllCopilotModels(token, enterpriseDomain string) {
	models := ai.GetModels("github-copilot")
	for _, m := range models {
		enableCopilotModel(token, m.ID, enterpriseDomain)
	}
}

func enableCopilotModel(token, modelID, enterpriseDomain string) bool {
	baseURL := GetGitHubCopilotBaseURL(token, enterpriseDomain)
	if githubCopilotBaseURLOverride != "" {
		baseURL = githubCopilotBaseURLOverride
	}
	policyURL := fmt.Sprintf("%s/models/%s/policy", baseURL, modelID)

	body, _ := json.Marshal(map[string]string{"state": "enabled"})
	req, err := http.NewRequest("POST", policyURL, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range copilotHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("openai-intent", "chat-policy")
	req.Header.Set("x-interaction-type", "chat-policy")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
