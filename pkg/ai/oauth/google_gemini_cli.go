// Ported from: packages/ai/src/utils/oauth/google-gemini-cli.ts
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
	"os"
	"strings"
	"time"

	"github.com/kfet/tau/pkg/ai"
)

const (
	geminiCLIClientIDEncoded     = "NjgxMjU1ODA5Mzk1LW9vOGZ0Mm9wcmRybnA5ZTNhcWY2YXYzaG1kaWIxMzVqLmFwcHMuZ29vZ2xldXNlcmNvbnRlbnQuY29t"
	geminiCLIClientSecretEncoded = "R09DU1BYLTR1SGdNUG0tMW83U2stZ2VWNkN1NWNsWEZzeGw="
	geminiCLIRedirectURI         = "http://localhost:8085/oauth2callback"
	geminiCLIAuthURL             = "https://accounts.google.com/o/oauth2/v2/auth"
	geminiCLICodeAssistEndpoint  = "https://cloudcode-pa.googleapis.com"

	tierFree     = "free-tier"
	tierLegacy   = "legacy-tier"
	tierStandard = "standard-tier"
)

// geminiCLITokenURL can be overridden for testing.
var geminiCLITokenURL = "https://oauth2.googleapis.com/token"

var geminiCLIScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

var (
	geminiCLIClientID     string
	geminiCLIClientSecret string
)

func init() {
	b, _ := base64.StdEncoding.DecodeString(geminiCLIClientIDEncoded)
	geminiCLIClientID = string(b)
	b, _ = base64.StdEncoding.DecodeString(geminiCLIClientSecretEncoded)
	geminiCLIClientSecret = string(b)
}

// GeminiCLIProvider implements Google Cloud Code Assist (Gemini CLI) OAuth.
type GeminiCLIProvider struct{}

func (p *GeminiCLIProvider) ID() string              { return "google-gemini-cli" }
func (p *GeminiCLIProvider) Name() string             { return "Google Cloud Code Assist (Gemini CLI)" }
func (p *GeminiCLIProvider) UsesCallbackServer() bool { return true }

func (p *GeminiCLIProvider) Login(callbacks LoginCallbacks) (*Credentials, error) {
	return loginGeminiCLI(callbacks)
}

func (p *GeminiCLIProvider) RefreshToken(creds *Credentials) (*Credentials, error) {
	projectID, _ := creds.Extra["projectId"].(string)
	if projectID == "" {
		return nil, fmt.Errorf("Google Cloud credentials missing projectId")
	}
	return refreshGeminiCLIToken(creds.Refresh, projectID)
}

func (p *GeminiCLIProvider) GetAPIKey(creds *Credentials) string {
	projectID, _ := creds.Extra["projectId"].(string)
	data, _ := json.Marshal(map[string]string{"token": creds.Access, "projectId": projectID})
	return string(data)
}

func (p *GeminiCLIProvider) ModifyModels(models []*ai.Model, _ *Credentials) []*ai.Model {
	return models
}

// startGeminiCallbackServer starts a local HTTP server on port 8085 for the OAuth callback.
func startGeminiCallbackServer(ctx context.Context) (server *http.Server, resultCh <-chan *callbackResult, err error) {
	return startOAuthCallbackServer(ctx, "/oauth2callback", "127.0.0.1:8085")
}

func loginGeminiCLI(callbacks LoginCallbacks) (*Credentials, error) {
	ctx := callbacks.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE: %w", err)
	}

	progress(callbacks, "Starting local server for OAuth callback...")
	srv, resultCh, err := startGeminiCallbackServer(ctx)
	if err != nil {
		// Server failed — fall back to manual paste only
		resultCh = nil
	}
	if srv != nil {
		defer srv.Close()
	}

	// Build authorization URL
	params := url.Values{
		"client_id":             {geminiCLIClientID},
		"response_type":        {"code"},
		"redirect_uri":         {geminiCLIRedirectURI},
		"scope":                {strings.Join(geminiCLIScopes, " ")},
		"code_challenge":       {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                {pkce.Verifier},
		"access_type":          {"offline"},
		"prompt":               {"consent"},
	}
	authURL := geminiCLIAuthURL + "?" + params.Encode()

	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{
			URL:          authURL,
			Instructions: "Complete the sign-in in your browser.",
		})
	}

	progress(callbacks, "Waiting for OAuth callback...")

	var code string
	if callbacks.OnManualCodeInput != nil {
		// Race between browser callback (if available) and manual input
		code, err = raceCallbackAndManual(ctx, resultCh, callbacks.OnManualCodeInput, pkce.Verifier)
	} else if resultCh != nil {
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
	form := url.Values{
		"client_id":     {geminiCLIClientID},
		"client_secret": {geminiCLIClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {geminiCLIRedirectURI},
		"code_verifier": {pkce.Verifier},
	}

	resp, err := oauthHTTPClient.PostForm(geminiCLITokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenData); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tokenData.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token received. Please try again")
	}

	// Get user email (optional)
	progress(callbacks, "Getting user info...")
	email := getUserEmail(tokenData.AccessToken)

	// Discover project
	projectID, err := discoverGeminiProject(tokenData.AccessToken, callbacks)
	if err != nil {
		return nil, fmt.Errorf("discovering project: %w", err)
	}

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

func refreshGeminiCLIToken(refreshToken, projectID string) (*Credentials, error) {
	form := url.Values{
		"client_id":     {geminiCLIClientID},
		"client_secret": {geminiCLIClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}

	resp, err := oauthHTTPClient.PostForm(geminiCLITokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google Cloud token refresh failed: %s", string(body))
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

// getDefaultTier returns the default tier from the allowed tiers list.
func getDefaultTier(allowedTiers []map[string]any) string {
	if len(allowedTiers) == 0 {
		return tierLegacy
	}
	for _, t := range allowedTiers {
		if isDefault, ok := t["isDefault"].(bool); ok && isDefault {
			if id, ok := t["id"].(string); ok {
				return id
			}
		}
	}
	return tierLegacy
}

// isVpcScAffectedUser checks if the error indicates a VPC Service Controls violation.
func isVpcScAffectedUser(data map[string]any) bool {
	errObj, ok := data["error"].(map[string]any)
	if !ok {
		return false
	}
	details, ok := errObj["details"].([]any)
	if !ok {
		return false
	}
	for _, d := range details {
		if detail, ok := d.(map[string]any); ok {
			if reason, ok := detail["reason"].(string); ok && reason == "SECURITY_POLICY_VIOLATED" {
				return true
			}
		}
	}
	return false
}

// discoverGeminiProject discovers or provisions a Google Cloud project for the user.
func discoverGeminiProject(accessToken string, callbacks LoginCallbacks) (string, error) {
	envProjectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if envProjectID == "" {
		envProjectID = os.Getenv("GOOGLE_CLOUD_PROJECT_ID")
	}

	// NOTE: The User-Agent header intentionally impersonates Google's Node.js client.
	// This value is ported directly from the upstream TypeScript source and is required
	// by the Cloud Code Assist API to accept requests. Changing it breaks authentication.
	// This is a known ToS risk inherited from the original client implementation.
	headers := map[string]string{
		"Authorization":      "Bearer " + accessToken,
		"Content-Type":       "application/json",
		"User-Agent":         "google-api-nodejs-client/9.15.1",
		"X-Goog-Api-Client":  "gl-node/22.17.0",
	}

	progress(callbacks, "Checking for existing Cloud Code Assist project...")

	reqBody, _ := json.Marshal(map[string]any{
		"cloudaicompanionProject": envProjectID,
		"metadata": map[string]string{
			"ideType":     "IDE_UNSPECIFIED",
			"platform":    "PLATFORM_UNSPECIFIED",
			"pluginType":  "GEMINI",
			"duetProject": envProjectID,
		},
	})

	req, err := http.NewRequest("POST", geminiCLICodeAssistEndpoint+"/v1internal:loadCodeAssist", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("loadCodeAssist: %w", err)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	resp.Body.Close()

	var data map[string]any

	if resp.StatusCode != http.StatusOK {
		// Check for VPC SC affected user
		json.Unmarshal(body, &data)
		if data != nil && isVpcScAffectedUser(data) {
			data = map[string]any{"currentTier": map[string]any{"id": tierStandard}}
		} else {
			return "", fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, string(body))
		}
	} else {
		if err := json.Unmarshal(body, &data); err != nil {
			return "", fmt.Errorf("parsing loadCodeAssist response: %w", err)
		}
	}

	// If user already has a current tier and project, use it
	if _, hasTier := data["currentTier"]; hasTier {
		if project, ok := data["cloudaicompanionProject"].(string); ok && project != "" {
			return project, nil
		}
		if envProjectID != "" {
			return envProjectID, nil
		}
		return "", fmt.Errorf("this account requires setting the GOOGLE_CLOUD_PROJECT or GOOGLE_CLOUD_PROJECT_ID environment variable. See https://goo.gle/gemini-cli-auth-docs#workspace-gca")
	}

	// User needs to be onboarded
	var allowedTiers []map[string]any
	if at, ok := data["allowedTiers"].([]any); ok {
		for _, t := range at {
			if tier, ok := t.(map[string]any); ok {
				allowedTiers = append(allowedTiers, tier)
			}
		}
	}
	tierID := getDefaultTier(allowedTiers)

	if tierID != tierFree && envProjectID == "" {
		return "", fmt.Errorf("this account requires setting the GOOGLE_CLOUD_PROJECT or GOOGLE_CLOUD_PROJECT_ID environment variable. See https://goo.gle/gemini-cli-auth-docs#workspace-gca")
	}

	progress(callbacks, "Provisioning Cloud Code Assist project (this may take a moment)...")

	onboardBody := map[string]any{
		"tierId": tierID,
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}
	if tierID != tierFree && envProjectID != "" {
		onboardBody["cloudaicompanionProject"] = envProjectID
		onboardBody["metadata"].(map[string]string)["duetProject"] = envProjectID
	}

	onboardReqBody, _ := json.Marshal(onboardBody)
	req, err = http.NewRequest("POST", geminiCLICodeAssistEndpoint+"/v1internal:onboardUser", bytes.NewReader(onboardReqBody))
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err = oauthHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("onboardUser: %w", err)
	}

	body, _ = io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("onboardUser failed (%d): %s", resp.StatusCode, string(body))
	}

	var lroData map[string]any
	if err := json.Unmarshal(body, &lroData); err != nil {
		return "", fmt.Errorf("parsing onboardUser response: %w", err)
	}

	// Poll long-running operation if not done
	done, _ := lroData["done"].(bool)
	if !done {
		opName, _ := lroData["name"].(string)
		if opName != "" {
			lroData, err = pollGeminiOperation(opName, headers, callbacks)
			if err != nil {
				return "", err
			}
		}
	}

	// Extract project ID from response
	if resp, ok := lroData["response"].(map[string]any); ok {
		if proj, ok := resp["cloudaicompanionProject"].(map[string]any); ok {
			if id, ok := proj["id"].(string); ok && id != "" {
				return id, nil
			}
		}
	}

	if envProjectID != "" {
		return envProjectID, nil
	}

	return "", fmt.Errorf("could not discover or provision a Google Cloud project. Try setting the GOOGLE_CLOUD_PROJECT or GOOGLE_CLOUD_PROJECT_ID environment variable")
}

func pollGeminiOperation(opName string, headers map[string]string, callbacks LoginCallbacks) (map[string]any, error) {
	const maxPollAttempts = 60 // 60 attempts × 5s = 5 min max
	for attempt := 0; attempt < maxPollAttempts; attempt++ {
		if attempt > 0 {
			progress(callbacks, fmt.Sprintf("Waiting for project provisioning (attempt %d)...", attempt+1))
			time.Sleep(5 * time.Second)
		}

		req, err := http.NewRequest("GET", geminiCLICodeAssistEndpoint+"/v1internal/"+opName, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := oauthHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("poll operation: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("poll operation failed (%d): %s", resp.StatusCode, string(body))
		}

		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("parsing poll response: %w", err)
		}

		if done, ok := data["done"].(bool); ok && done {
			return data, nil
		}
	}
	return nil, fmt.Errorf("operation %s timed out after %d attempts", opName, maxPollAttempts)
}
