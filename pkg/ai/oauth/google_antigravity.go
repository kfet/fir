// Ported from: packages/ai/src/utils/oauth/google-antigravity.ts
// Upstream hash: 1caadb2e
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
	"strings"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

const (
	antigravityClientIDEncoded     = "MTA3MTAwNjA2MDU5MS10bWhzc2luMmgyMWxjcmUyMzV2dG9sb2poNGc0MDNlcC5hcHBzLmdvb2dsZXVzZXJjb250ZW50LmNvbQ=="
	antigravityClientSecretEncoded = "R09DU1BYLUs1OEZXUjQ4NkxkTEoxbUxCOHNYQzR6NnFEQWY="
	antigravityRedirectURI         = "http://localhost:51121/oauth-callback"
	antigravityAuthURL             = "https://accounts.google.com/o/oauth2/v2/auth"
	antigravityDefaultProjectID    = "rising-fact-p41fc"
)

// antigravityTokenURL can be overridden for testing.
var antigravityTokenURL = "https://oauth2.googleapis.com/token"

var antigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

var (
	antigravityClientID     string
	antigravityClientSecret string
)

func init() {
	b, _ := base64.StdEncoding.DecodeString(antigravityClientIDEncoded)
	antigravityClientID = string(b)
	b, _ = base64.StdEncoding.DecodeString(antigravityClientSecretEncoded)
	antigravityClientSecret = string(b)
}

// AntigravityProvider implements Google Antigravity OAuth (Gemini 3, Claude, GPT-OSS via Google Cloud).
type AntigravityProvider struct{}

func (p *AntigravityProvider) ID() string               { return "google-antigravity" }
func (p *AntigravityProvider) Name() string             { return "Antigravity (Gemini 3, Claude, GPT-OSS)" }
func (p *AntigravityProvider) UsesCallbackServer() bool { return true }

func (p *AntigravityProvider) Login(callbacks LoginCallbacks) (*Credentials, error) {
	return loginAntigravity(callbacks)
}

func (p *AntigravityProvider) RefreshToken(creds *Credentials) (*Credentials, error) {
	projectID, _ := creds.Extra["projectId"].(string)
	if projectID == "" {
		return nil, fmt.Errorf("Antigravity credentials missing projectId")
	}
	return refreshAntigravityToken(creds.Refresh, projectID)
}

func (p *AntigravityProvider) GetAPIKey(creds *Credentials) string {
	projectID, _ := creds.Extra["projectId"].(string)
	data, _ := json.Marshal(map[string]string{"token": creds.Access, "projectId": projectID})
	return string(data)
}

func (p *AntigravityProvider) ModifyModels(models []*ai.Model, _ *Credentials) []*ai.Model {
	return models
}

// CallbackResult holds the result from the OAuth callback server.
type CallbackResult struct {
	Code  string
	State string
}

// startCallbackServer starts a local HTTP server on port 51121 to receive the OAuth callback.
func startCallbackServer(ctx context.Context) (server *http.Server, resultCh <-chan *CallbackResult, err error) {
	srv, ch, _, listenErr := StartOAuthCallbackServer(ctx, "/oauth-callback", "127.0.0.1:51121")
	return srv, ch, listenErr
}

// parseRedirectURL extracts code and state from a redirect URL string.
func parseRedirectURL(input string) (code, state string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	// Strip shell-escape backslashes (common when pasting from terminal output).
	input = strings.ReplaceAll(input, "\\", "")
	u, err := url.Parse(input)
	if err != nil {
		return "", ""
	}
	return u.Query().Get("code"), u.Query().Get("state")
}

func loginAntigravity(callbacks LoginCallbacks) (*Credentials, error) {
	ctx := callbacks.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	// Start local callback server
	progress(callbacks, "Starting local server for OAuth callback...")
	srv, resultCh, err := startCallbackServer(ctx)
	if err != nil {
		// Server failed — fall back to manual paste only
		resultCh = nil
	}
	if srv != nil {
		defer srv.Close()
	}

	// Build authorization URL
	params := url.Values{
		"client_id":             {antigravityClientID},
		"response_type":         {"code"},
		"redirect_uri":          {antigravityRedirectURI},
		"scope":                 {strings.Join(antigravityScopes, " ")},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {pkce.Verifier},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
	}
	authURL := antigravityAuthURL + "?" + params.Encode()

	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{
			URL:          authURL,
			Instructions: "Complete the sign-in in your browser.",
		})
	}

	// Wait for callback, racing with manual code input if available
	progress(callbacks, "Waiting for OAuth callback...")

	var code string
	if callbacks.OnManualCodeInput != nil {
		// Race between browser callback (if available) and manual input
		code, err = raceCallbackAndManual(ctx, resultCh, callbacks.OnManualCodeInput, callbacks.OnDismissManualInput, pkce.Verifier)
	} else if resultCh != nil {
		// Just wait for browser callback
		select {
		case result, ok := <-resultCh:
			if !ok || result == nil {
				return nil, fmt.Errorf("no authorization code received")
			}
			if result.State != pkce.Verifier {
				return nil, fmt.Errorf("OAuth state mismatch - possible CSRF attack")
			}
			code = result.Code
		case <-ctx.Done():
			return nil, fmt.Errorf("login cancelled")
		}
	}
	if err != nil {
		return nil, err
	}
	if code == "" {
		return nil, fmt.Errorf("no authorization code received")
	}

	// Exchange code for tokens
	progress(callbacks, "Exchanging authorization code for tokens...")
	tokenData, err := exchangeAntigravityCode(code, pkce.Verifier)
	if err != nil {
		return nil, err
	}

	if tokenData.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token received. Please try again")
	}

	// Get user email (optional)
	progress(callbacks, "Getting user info...")
	email := getUserEmail(tokenData.AccessToken)

	// Discover project
	projectID := discoverProject(tokenData.AccessToken, callbacks)

	expiresAt := time.Now().UnixMilli() + int64(tokenData.ExpiresIn)*1000 - 5*60*1000

	extra := map[string]any{"projectId": projectID}
	if email != "" {
		extra["email"] = email
	}

	return &Credentials{
		Refresh: tokenData.RefreshToken,
		Access:  tokenData.AccessToken,
		Expires: expiresAt,
		Extra:   extra,
	}, nil
}

// raceCallbackAndManual waits for either the browser callback or manual code input.
// If resultCh is nil (no callback server), it falls back to manual input only.
func raceCallbackAndManual(ctx context.Context, resultCh <-chan *CallbackResult, manualInput func() (string, error), dismissManualInput func(), verifier string) (string, error) {
	if resultCh == nil {
		return manualCodeInput(manualInput, verifier)
	}

	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)

	// Don't prompt for manual input immediately — give the browser callback
	// a chance to arrive first. Only show the prompt after a short delay or
	// if the callback channel closes without a result.
	manualStarted := false
	startManual := func() {
		if manualStarted {
			return
		}
		manualStarted = true
		go func() {
			input, err := manualInput()
			manualCh <- manualResult{input, err}
		}()
	}

	// Wait briefly for the browser callback before showing manual input.
	delay := time.NewTimer(3 * time.Second)
	defer delay.Stop()

	for {
		select {
		case result, ok := <-resultCh:
			if ok && result != nil {
				if result.State != verifier {
					return "", fmt.Errorf("OAuth state mismatch - possible CSRF attack")
				}
				if manualStarted && dismissManualInput != nil {
					dismissManualInput()
				}
				return result.Code, nil
			}
			// Callback server closed without result — fall back to manual.
			startManual()
			// Continue loop to wait on manualCh.
			resultCh = nil // Don't select on it again.

		case <-delay.C:
			// Browser hasn't responded yet — show manual input prompt.
			startManual()

		case mr := <-manualCh:
			if mr.err != nil {
				return "", mr.err
			}
			code, state := parseRedirectURL(mr.input)
			if state != "" && state != verifier {
				return "", fmt.Errorf("OAuth state mismatch - possible CSRF attack")
			}
			return code, nil

		case <-ctx.Done():
			return "", fmt.Errorf("login cancelled")
		}
	}
}

// manualCodeInput prompts for manual input and parses the redirect URL.
func manualCodeInput(manualInput func() (string, error), verifier string) (string, error) {
	input, err := manualInput()
	if err != nil {
		return "", err
	}
	code, state := parseRedirectURL(input)
	if state != "" && state != verifier {
		return "", fmt.Errorf("OAuth state mismatch - possible CSRF attack")
	}
	return code, nil
}

type antigravityTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeAntigravityCode(code, verifier string) (*antigravityTokenData, error) {
	form := url.Values{
		"client_id":     {antigravityClientID},
		"client_secret": {antigravityClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {antigravityRedirectURI},
		"code_verifier": {verifier},
	}

	resp, err := oauthHTTPClient.PostForm(antigravityTokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var data antigravityTokenData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	return &data, nil
}

func refreshAntigravityToken(refreshToken, projectID string) (*Credentials, error) {
	form := url.Values{
		"client_id":     {antigravityClientID},
		"client_secret": {antigravityClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}

	resp, err := oauthHTTPClient.PostForm(antigravityTokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Antigravity token refresh failed: %s", string(body))
	}

	var data struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("parsing refresh response: %w", err)
	}

	refresh := data.RefreshToken
	if refresh == "" {
		refresh = refreshToken
	}

	return &Credentials{
		Refresh: refresh,
		Access:  data.AccessToken,
		Expires: time.Now().UnixMilli() + int64(data.ExpiresIn)*1000 - 5*60*1000,
		Extra:   map[string]any{"projectId": projectID},
	}, nil
}

func getUserEmail(accessToken string) string {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var data struct {
		Email string `json:"email"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	json.Unmarshal(body, &data)
	return data.Email
}

func discoverProject(accessToken string, callbacks LoginCallbacks) string {
	progress(callbacks, "Checking for existing project...")

	// NOTE: The User-Agent and X-Goog-Api-Client headers intentionally impersonate
	// Google's Node.js client and VS Code Cloud Shell Editor. These values are ported
	// directly from the upstream TypeScript source and are required by the Cloud Code
	// Assist API to accept requests. Changing them breaks authentication. This is a
	// known ToS risk inherited from the original client implementation.
	headers := map[string]string{
		"Authorization":     "Bearer " + accessToken,
		"Content-Type":      "application/json",
		"User-Agent":        "google-api-nodejs-client/9.15.1",
		"X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
	}

	clientMeta, _ := json.Marshal(map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	})
	headers["Client-Metadata"] = string(clientMeta)

	endpoints := []string{
		"https://cloudcode-pa.googleapis.com",
		"https://daily-cloudcode-pa.sandbox.googleapis.com",
	}

	reqBody, _ := json.Marshal(map[string]any{
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	})

	for _, endpoint := range endpoints {
		req, err := http.NewRequest("POST", endpoint+"/v1internal:loadCodeAssist", bytes.NewReader(reqBody))
		if err != nil {
			continue
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := oauthHTTPClient.Do(req)
		if err != nil {
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			continue
		}

		// Handle both string and object formats for cloudaicompanionProject
		switch p := data["cloudaicompanionProject"].(type) {
		case string:
			if p != "" {
				return p
			}
		case map[string]any:
			if id, ok := p["id"].(string); ok && id != "" {
				return id
			}
		}
	}

	progress(callbacks, "Using default project...")
	return antigravityDefaultProjectID
}

func progress(callbacks LoginCallbacks, msg string) {
	if callbacks.OnProgress != nil {
		callbacks.OnProgress(msg)
	}
}
