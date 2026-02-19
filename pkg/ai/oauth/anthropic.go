// Ported from: packages/ai/src/utils/oauth/anthropic.ts
// Upstream hash: 1caadb2e
package oauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kfet/tau/pkg/ai"
)

const (
	// Encoded client ID. Decoded at init time.
	anthropicClientIDEncoded       = "OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl"
	anthropicAuthorizeURL          = "https://claude.ai/oauth/authorize"
	anthropicDefaultTokenURL       = "https://console.anthropic.com/v1/oauth/token"
	anthropicRedirectURI           = "https://console.anthropic.com/oauth/code/callback"
	anthropicScopes                = "org:create_api_key user:profile user:inference"
)

// anthropicTokenURL can be overridden in tests.
var anthropicTokenURL = anthropicDefaultTokenURL

// setAnthropicTokenURL overrides the token URL (for testing).
func setAnthropicTokenURL(u string) {
	anthropicTokenURL = u
}

var anthropicClientID string

func init() {
	b, err := base64.StdEncoding.DecodeString(anthropicClientIDEncoded)
	if err != nil {
		anthropicClientID = "unknown"
	} else {
		anthropicClientID = string(b)
	}
}

// AnthropicProvider implements Anthropic OAuth (Claude Pro/Max).
type AnthropicProvider struct{}

func (p *AnthropicProvider) ID() string               { return "anthropic" }
func (p *AnthropicProvider) Name() string              { return "Anthropic (Claude Pro/Max)" }
func (p *AnthropicProvider) UsesCallbackServer() bool  { return false }

func (p *AnthropicProvider) Login(callbacks LoginCallbacks) (*Credentials, error) {
	return loginAnthropic(callbacks)
}

func (p *AnthropicProvider) RefreshToken(creds *Credentials) (*Credentials, error) {
	return refreshAnthropicToken(creds.Refresh)
}

func (p *AnthropicProvider) GetAPIKey(creds *Credentials) string {
	return creds.Access
}

func (p *AnthropicProvider) ModifyModels(models []*ai.Model, _ *Credentials) []*ai.Model {
	return models
}

// loginAnthropic runs the Anthropic OAuth authorization code flow with PKCE.
func loginAnthropic(callbacks LoginCallbacks) (*Credentials, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	// Build authorization URL
	params := url.Values{
		"code":                  {"true"},
		"client_id":            {anthropicClientID},
		"response_type":        {"code"},
		"redirect_uri":         {anthropicRedirectURI},
		"scope":                {anthropicScopes},
		"code_challenge":       {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                {pkce.Verifier},
	}
	authURL := anthropicAuthorizeURL + "?" + params.Encode()

	// Notify the caller to open the URL
	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{URL: authURL})
	}

	// Prompt the user for the authorization code (format: code#state)
	if callbacks.OnPrompt == nil {
		return nil, fmt.Errorf("OnPrompt callback required for Anthropic login")
	}
	authCode, err := callbacks.OnPrompt(Prompt{Message: "Paste the authorization code:"})
	if err != nil {
		return nil, fmt.Errorf("prompting for code: %w", err)
	}

	parts := strings.SplitN(authCode, "#", 2)
	code := parts[0]
	state := ""
	if len(parts) > 1 {
		state = parts[1]
	}

	// Exchange code for tokens
	return exchangeAnthropicCode(code, state, pkce.Verifier)
}

// exchangeAnthropicCode exchanges an authorization code for tokens.
func exchangeAnthropicCode(code, state, verifier string) (*Credentials, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  anthropicRedirectURI,
		"code_verifier": verifier,
	}
	return doAnthropicTokenRequest(body)
}

// refreshAnthropicToken refreshes an expired Anthropic OAuth token.
func refreshAnthropicToken(refreshToken string) (*Credentials, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     anthropicClientID,
		"refresh_token": refreshToken,
	}
	return doAnthropicTokenRequest(body)
}

// doAnthropicTokenRequest sends a token request to the Anthropic OAuth endpoint.
func doAnthropicTokenRequest(body map[string]string) (*Credentials, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	resp, err := oauthHTTPClient.Post(anthropicTokenURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tokenData); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	// Calculate expiry (current time + expires_in - 5 min buffer)
	expiresAt := time.Now().UnixMilli() + tokenData.ExpiresIn*1000 - 5*60*1000

	return &Credentials{
		Refresh: tokenData.RefreshToken,
		Access:  tokenData.AccessToken,
		Expires: expiresAt,
	}, nil
}
