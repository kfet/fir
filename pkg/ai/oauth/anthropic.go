// Ported from: packages/ai/src/utils/oauth/anthropic.ts
// Upstream hash: f04d9bc4
package oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

const (
	// Encoded client ID. Decoded at init time.
	anthropicClientIDEncoded = "OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl"
	anthropicAuthorizeURL    = "https://claude.ai/oauth/authorize"
	anthropicDefaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	// anthropicManualRedirectURI is used when the user pastes the final redirect URL manually
	// (e.g., browser is on a different machine).
	anthropicManualRedirectURI = "https://platform.claude.com/oauth/code/callback"
	// anthropicCallbackAddr is the local address for the OAuth callback server.
	anthropicCallbackAddr = "127.0.0.1:53692"
	anthropicCallbackPath = "/callback"
	// anthropicRedirectURI is the local callback URI registered with Anthropic.
	anthropicRedirectURI = "http://localhost:53692/callback"
	anthropicScopes      = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
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
func (p *AnthropicProvider) Name() string             { return "Anthropic (Claude Pro/Max)" }
func (p *AnthropicProvider) UsesCallbackServer() bool { return true }

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

// loginAnthropic runs the Anthropic OAuth authorization code + PKCE flow.
// It starts a local callback server and falls back to manual code input if needed.
func loginAnthropic(callbacks LoginCallbacks) (*Credentials, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	ctx := callbacks.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// Try starting local callback server; fall back silently if port unavailable.
	redirectURI := anthropicRedirectURI
	var resultCh <-chan *callbackResult
	srv, ch, serverErr := startOAuthCallbackServer(ctx, anthropicCallbackPath, anthropicCallbackAddr)
	if serverErr == nil {
		resultCh = ch
		defer srv.Close()
	} else {
		// Port unavailable — use the manual (hosted) redirect URI so the browser
		// still lands on a page, and the user can paste the resulting URL/code.
		redirectURI = anthropicManualRedirectURI
	}

	// Build authorization URL
	params := url.Values{
		"code":                  {"true"},
		"client_id":             {anthropicClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {anthropicScopes},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {pkce.Verifier},
	}
	authURL := anthropicAuthorizeURL + "?" + params.Encode()

	// Notify user to open the auth URL
	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{
			URL:          authURL,
			Instructions: "Complete login in your browser. If the browser is on another machine, paste the final redirect URL here.",
		})
	}

	var code, state string
	usedManualRedirect := serverErr != nil // already on manual path if server failed

	if callbacks.OnManualCodeInput != nil {
		type manualResult struct {
			input string
			err   error
		}
		manualCh := make(chan manualResult, 1)

		manualStarted := false
		startManual := func() {
			if manualStarted {
				return
			}
			manualStarted = true
			go func() {
				input, err := callbacks.OnManualCodeInput()
				manualCh <- manualResult{input, err}
			}()
		}

		if resultCh != nil {
			// Race: wait briefly for the browser callback before showing the
			// manual-input prompt. This avoids flashing a paste prompt when
			// the local callback will arrive momentarily.
			delay := time.NewTimer(3 * time.Second)
			defer delay.Stop()

			for {
				select {
				case result, ok := <-resultCh:
					if ok && result != nil && result.Code != "" {
						code, state = result.Code, result.State
						// Dismiss the manual-input prompt if it was shown.
						if manualStarted && callbacks.OnDismissManualInput != nil {
							callbacks.OnDismissManualInput()
						}
					} else {
						// Server closed without result — fall back to manual.
						startManual()
						resultCh = nil // don't select again
						continue
					}
				case <-delay.C:
					// Browser hasn't responded yet — show manual input prompt.
					startManual()
					continue
				case mr := <-manualCh:
					if mr.err != nil {
						return nil, mr.err
					}
					if mr.input != "" {
						code, state = parseAuthorizationInput(mr.input)
						usedManualRedirect = true
					}
				case <-ctx.Done():
					return nil, fmt.Errorf("login cancelled")
				}
				break
			}
		} else {
			// No server; just wait for manual input.
			startManual()
			mr := <-manualCh
			if mr.err != nil {
				return nil, mr.err
			}
			code, state = parseAuthorizationInput(mr.input)
			usedManualRedirect = true
		}
	} else if resultCh != nil {
		// Just wait for the callback server.
		result, ok := <-resultCh
		if ok && result != nil {
			code, state = result.Code, result.State
		}
	}

	// Fallback: if neither path produced a code, prompt manually
	if code == "" {
		if callbacks.OnPrompt == nil {
			return nil, fmt.Errorf("missing authorization code")
		}
		input, err := callbacks.OnPrompt(Prompt{
			Message:     "Paste the authorization code or full redirect URL:",
			Placeholder: anthropicManualRedirectURI,
		})
		if err != nil {
			return nil, fmt.Errorf("prompting for code: %w", err)
		}
		code, state = parseAuthorizationInput(input)
		usedManualRedirect = true
	}

	if code == "" {
		return nil, fmt.Errorf("missing authorization code")
	}
	if state == "" {
		state = pkce.Verifier
	}
	if state != pkce.Verifier {
		return nil, fmt.Errorf("OAuth state mismatch")
	}

	if callbacks.OnProgress != nil {
		callbacks.OnProgress("Exchanging authorization code for tokens...")
	}

	if usedManualRedirect {
		redirectURI = anthropicManualRedirectURI
	}
	return exchangeAnthropicCode(code, state, pkce.Verifier, redirectURI)
}

// exchangeAnthropicCode exchanges an authorization code for tokens.
func exchangeAnthropicCode(code, state, verifier, redirectURI string) (*Credentials, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
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
		return nil, fmt.Errorf("token request failed. url=%s: %w", anthropicTokenURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", resp.StatusCode, anthropicTokenURL, string(respBody))
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tokenData); err != nil {
		return nil, fmt.Errorf("token exchange returned invalid JSON. url=%s; body=%s: %w", anthropicTokenURL, string(respBody), err)
	}

	// Calculate expiry (current time + expires_in - 5 min buffer)
	expiresAt := time.Now().UnixMilli() + tokenData.ExpiresIn*1000 - 5*60*1000

	return &Credentials{
		Refresh: tokenData.RefreshToken,
		Access:  tokenData.AccessToken,
		Expires: expiresAt,
	}, nil
}
